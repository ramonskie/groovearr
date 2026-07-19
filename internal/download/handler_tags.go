package download

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bogem/id3v2/v2"
	flac "github.com/go-flac/go-flac"
	"github.com/go-flac/flacpicture"
	"github.com/ramonskie/groovearr/internal/domain"
)

// TagWriterHandler writes ID3v2 (MP3) and Vorbis comment (FLAC) tags on
// a downloaded file after it has been renamed into the library.
type TagWriterHandler struct{}

// NewTagWriterHandler creates a handler that writes audio metadata tags.
func NewTagWriterHandler() *TagWriterHandler {
	return &TagWriterHandler{}
}

// Handle writes metadata tags from the record's metadata fields and embeds
// cover art if cover.jpg exists in the same directory. Returns nil on tag
// write errors to avoid breaking the import chain.
func (h *TagWriterHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" {
		return nil
	}

	ext := strings.ToLower(filepath.Ext(record.FilePath))

	// Use record metadata (populated by renamer handler), not filename parsing.
	artist := record.Artist
	album := record.Album
	title := record.Title
	if artist == "" {
		return nil
	}

	coverPath := filepath.Join(filepath.Dir(record.FilePath), "cover.jpg")

	var err error
	switch ext {
	case ".mp3":
		err = writeID3v2(record.FilePath, artist, album, title, coverPath)
	case ".flac":
		err = writeFLACTags(record.FilePath, artist, album, title, coverPath)
	default:
		return nil
	}

	if err != nil {
		// Non-fatal — continue the import chain.
		return nil
	}

	return nil
}

// writeID3v2 opens an MP3 file, clears existing tags, writes metadata,
// embeds cover art, and saves.
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

// writeFLACTags opens a FLAC file, writes Vorbis comments, embeds cover art, and saves.
func writeFLACTags(path, artist, album, title, coverPath string) error {
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
		if err := addFLACCover(f, data); err != nil {
			// Non-fatal.
		}
	}

	if err := f.Save(path); err != nil {
		return fmt.Errorf("flac save: %w", err)
	}
	return nil
}

// setVorbisComments replaces or inserts a Vorbis comment block.
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

// marshalVorbisComment encodes a Vorbis comment block in binary format.
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

// addFLACCover replaces existing picture blocks and adds a front cover.
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
