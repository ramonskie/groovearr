package download

import (
	"context"
	"log"
	"path/filepath"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/tagging"
)

// TagWriterHandler writes ID3v2 (MP3) and Vorbis comment (FLAC) tags on
// a downloaded file after it has been renamed into the library.
type TagWriterHandler struct{}

// NewTagWriterHandler creates a handler that writes audio metadata tags.
func NewTagWriterHandler() *TagWriterHandler {
	return &TagWriterHandler{}
}

// Handle writes metadata tags from the record's metadata fields and embeds
// cover art if cover.jpg exists in the same directory.
func (h *TagWriterHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" {
		return nil
	}

	artist := record.Artist
	album := record.Album
	title := record.Title
	if artist == "" {
		return nil
	}

	coverPath := filepath.Join(filepath.Dir(record.FilePath), "cover.jpg")

	if err := tagging.WriteTags(record.FilePath, artist, album, title, coverPath); err != nil {
		log.Printf("tag writer: %s: %v", filepath.Base(record.FilePath), err)
	}

	return nil // non-fatal
}
