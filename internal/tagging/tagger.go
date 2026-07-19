// Package tagging provides shared audio metadata tag writing for MP3 (ID3v2)
// and FLAC (Vorbis comments) formats.
package tagging

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bogem/id3v2/v2"
	flac "github.com/go-flac/go-flac/v2"
)

// WriteTags writes ID3v2 or Vorbis comment tags to the file at path based on
// the file extension. Cover art is embedded from coverPath if the file exists.
// Returns nil on success, or an error if the file format is unrecognized or
// the tag write fails.
func WriteTags(path, artist, album, title, coverPath string) error {
	ext := strings.ToLower(strings.TrimPrefix(path[strings.LastIndex(path, "."):], ""))
	// Fallback: extract proper extension.
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		ext = strings.ToLower(path[idx:])
	}

	switch ext {
	case ".mp3":
		return writeID3v2(path, artist, album, title, coverPath)
	case ".flac":
		return writeFLACTags(path, artist, album, title, coverPath)
	default:
		return nil // non-audio or unsupported — no-op
	}
}

func writeID3v2(path, artist, album, title, coverPath string) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("id3v2 open: %w", err)
	}
	defer tag.Close()

	tag.DeleteAllFrames()
	tag.SetArtist(artist)
	tag.SetAlbum(album)
	tag.SetTitle(title)

	if data, err := os.ReadFile(coverPath); err == nil {
		tag.AddAttachedPicture(id3v2.PictureFrame{
			Encoding:    id3v2.EncodingUTF8,
			MimeType:    "image/jpeg",
			PictureType: id3v2.PTFrontCover,
			Description: "Front cover",
			Picture:     data,
		})
	}

	if err := tag.Save(); err != nil {
		return fmt.Errorf("id3v2 save: %w", err)
	}
	return nil
}

func writeFLACTags(path, artist, album, title, coverPath string) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return fmt.Errorf("flac parse: %w", err)
	}
	defer f.Close()

	tags := map[string]string{
		"ARTIST": artist,
		"ALBUM":  album,
		"TITLE":  title,
	}
	setVorbisComments(f, tags)

	if data, err := os.ReadFile(coverPath); err == nil {
		_ = setFLACCover(f, data) // non-fatal
	}

	if err := f.Save(path); err != nil {
		return fmt.Errorf("flac save: %w", err)
	}
	return nil
}

func setVorbisComments(f *flac.File, tags map[string]string) {
	blockData := marshalVorbisComment("Groovearr", tags)
	newBlock := &flac.MetaDataBlock{Type: flac.VorbisComment, Data: blockData}

	for i, meta := range f.Meta {
		if meta.Type == flac.VorbisComment {
			f.Meta[i] = newBlock
			return
		}
	}
	f.Meta = append(f.Meta[:1], append([]*flac.MetaDataBlock{newBlock}, f.Meta[1:]...)...)
}

func marshalVorbisComment(vendor string, tags map[string]string) []byte {
	var buf []byte

	vendorB := []byte(vendor)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(vendorB)))
	buf = append(buf, lenBuf...)
	buf = append(buf, vendorB...)

	binary.LittleEndian.PutUint32(lenBuf, uint32(len(tags)))
	buf = append(buf, lenBuf...)

	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tag := []byte(k + "=" + tags[k])
		binary.LittleEndian.PutUint32(lenBuf, uint32(len(tag)))
		buf = append(buf, lenBuf...)
		buf = append(buf, tag...)
	}

	return buf
}

// setFLACCover replaces any existing picture blocks and embeds a front cover.
// Implements FLAC METADATA_BLOCK_PICTURE per the FLAC spec (section 7).
func setFLACCover(f *flac.File, imgData []byte) error {
	// Build picture block: type(4) + mime_len(4) + mime + desc_len(4) + desc +
	// width(4) + height(4) + color_depth(4) + colors_used(4) + data_len(4) + data.
	picBuf := make([]byte, 4+4+len("image/jpeg")+4+len("Front cover")+4+4+4+4+4+len(imgData))
	off := 0
	binary.BigEndian.PutUint32(picBuf[off:], 3) // Front cover
	off += 4
	mime := "image/jpeg"
	binary.BigEndian.PutUint32(picBuf[off:], uint32(len(mime)))
	off += 4
	copy(picBuf[off:], mime)
	off += len(mime)
	desc := "Front cover"
	binary.BigEndian.PutUint32(picBuf[off:], uint32(len(desc)))
	off += 4
	copy(picBuf[off:], desc)
	off += len(desc)
	// width, height, color depth, colors used — 0 means "not specified"
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], uint32(len(imgData)))
	off += 4
	copy(picBuf[off:], imgData)

	picBlock := &flac.MetaDataBlock{Type: flac.Picture, Data: picBuf}

	// Remove existing picture blocks.
	meta := f.Meta[:0]
	for _, m := range f.Meta {
		if m.Type != flac.Picture {
			meta = append(meta, m)
		}
	}
	f.Meta = meta
	f.Meta = append(f.Meta, picBlock)
	return nil
}
