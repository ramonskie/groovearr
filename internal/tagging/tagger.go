// Package tagging provides shared audio metadata tag writing for MP3 (ID3v2)
// and FLAC (Vorbis comments) formats.
package tagging

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/bogem/id3v2/v2"
	flac "github.com/go-flac/go-flac"
	"github.com/go-flac/flacpicture"
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
	// Quick pre-check: verify FLAC magic bytes before attempting a full parse.
	// Deezer downloads sometimes produce files that go-flac can't handle.
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	magic := make([]byte, 4)
	n, _ := io.ReadFull(fh, magic)
	fh.Close()
	if n < 4 || string(magic) != "fLaC" {
		return nil // not a valid FLAC file, skip silently
	}

	f, err := flac.ParseFile(path)
	if err != nil {
		return fmt.Errorf("flac parse: %w", err)
	}

	tags := map[string]string{
		"ARTIST": artist,
		"ALBUM":  album,
		"TITLE":  title,
	}
	setVorbisComments(f, tags)

	if data, err := os.ReadFile(coverPath); err == nil {
		_ = addFLACCover(f, data) // non-fatal
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

func addFLACCover(f *flac.File, imgData []byte) error {
	pic, err := flacpicture.NewFromImageData(
		flacpicture.PictureTypeFrontCover,
		"Front cover",
		imgData,
		"image/jpeg",
	)
	if err != nil {
		return err
	}
	picMeta := pic.Marshal()

	meta := f.Meta[:0]
	for _, m := range f.Meta {
		if m.Type != flac.Picture {
			meta = append(meta, m)
		}
	}
	f.Meta = meta
	f.Meta = append(f.Meta, &picMeta)
	return nil
}
