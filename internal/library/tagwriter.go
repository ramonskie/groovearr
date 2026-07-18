// Package library provides file scanning, path resolution, and post-download hooks.
package library

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bogem/id3v2/v2"
	flac "github.com/go-flac/go-flac"
	"github.com/go-flac/flacpicture"
	"github.com/ramonskie/groovearr/internal/domain"
)

// NewTagWriterHook creates a post-download hook that writes ID3v2 (MP3) and
// Vorbis comment (FLAC) tags into downloaded audio files. Metadata is parsed
// from the post-renamer file path. Cover art is embedded from cover.jpg in
// the same directory if it exists.
//
// The hook is format-aware and source-agnostic. File path is returned unchanged.
func NewTagWriterHook() func(ctx context.Context, record domain.DownloadRecord) (string, error) {
	return func(ctx context.Context, record domain.DownloadRecord) (string, error) {
		if record.FilePath == "" {
			return record.FilePath, nil
		}

		ext := strings.ToLower(filepath.Ext(record.FilePath))
		artist, album, title := parseMetadataFromFilename(filepath.Base(record.FilePath))
		if artist == "" {
			return record.FilePath, nil
		}

		coverPath := filepath.Join(filepath.Dir(record.FilePath), "cover.jpg")

		var err error
		switch ext {
		case ".mp3":
			err = writeID3v2(record.FilePath, artist, album, title, coverPath)
		case ".flac":
			err = writeFLACTags(record.FilePath, artist, album, title, coverPath)
		default:
			return record.FilePath, nil
		}

		if err != nil {
			log.Printf("tag writer: %s: %v", filepath.Base(record.FilePath), err)
		}

		return record.FilePath, nil
	}
}

// writeID3v2 opens an MP3 file, clears existing tags, writes fresh metadata,
// embeds cover art, and saves.
func writeID3v2(path, artist, album, title, coverPath string) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("id3v2 open: %w", err)
	}
	defer tag.Close()

	// Start fresh — delete all existing frames.
	tag.DeleteAllFrames()

	tag.SetArtist(artist)
	tag.SetAlbum(album)
	tag.SetTitle(title)

	// Embed cover art if available.
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

// writeFLACTags opens a FLAC file, writes Vorbis comments, embeds cover art, and saves.
func writeFLACTags(path, artist, album, title, coverPath string) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return fmt.Errorf("flac parse: %w", err)
	}

	// Build Vorbis comment tags.
	tags := map[string]string{
		"ARTIST": artist,
		"ALBUM":  album,
		"TITLE":  title,
	}
	setVorbisComments(f, tags)

	// Embed cover art if available.
	if data, err := os.ReadFile(coverPath); err == nil {
		if err := addFLACCover(f, data); err != nil {
			log.Printf("tag writer: flac cover: %v", err)
		}
	}

	if err := f.Save(path); err != nil {
		return fmt.Errorf("flac save: %w", err)
	}
	return nil
}

// setVorbisComments replaces or inserts a Vorbis comment block in a FLAC file.
func setVorbisComments(f *flac.File, tags map[string]string) {
	blockData := marshalVorbisComment("Groovearr", tags)

	newBlock := &flac.MetaDataBlock{
		Type: flac.VorbisComment,
		Data: blockData,
	}

	// Replace existing VorbisComment block, or insert after StreamInfo (index 0).
	for i, meta := range f.Meta {
		if meta.Type == flac.VorbisComment {
			f.Meta[i] = newBlock
			return
		}
	}
	f.Meta = append(f.Meta[:1], append([]*flac.MetaDataBlock{newBlock}, f.Meta[1:]...)...)
}

// marshalVorbisComment encodes a Vorbis comment block in binary format.
// Format: vendor length (uint32 LE) + vendor string + comment count (uint32 LE) + comments.
func marshalVorbisComment(vendor string, tags map[string]string) []byte {
	var buf []byte

	// Vendor string.
	vendorB := []byte(vendor)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(vendorB)))
	buf = append(buf, lenBuf...)
	buf = append(buf, vendorB...)

	// Comment count.
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(tags)))
	buf = append(buf, lenBuf...)

	// Each tag as "KEY=VALUE" in sorted order for deterministic output.
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

// addFLACCover replaces any existing picture blocks, then adds a front cover picture
// from the given image data.
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

	// Remove any existing picture blocks to avoid duplicates on re-processing.
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
