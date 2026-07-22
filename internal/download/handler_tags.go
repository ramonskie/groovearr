package download

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/tagging"
)

// TagWriterHandler writes ID3v2 (MP3) and Vorbis comment (FLAC) tags on
// a downloaded file after it has been renamed into the library.
type TagWriterHandler struct {
	log    *slog.Logger
	tagger *tagging.Tagger
}

// NewTagWriterHandler creates a handler that writes audio metadata tags.
func NewTagWriterHandler(logger *slog.Logger) *TagWriterHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TagWriterHandler{log: logger, tagger: tagging.New(logger)}
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

	if err := h.tagger.WriteTags(record.FilePath, artist, album, title, coverPath); err != nil {
		h.log.Warn("tag write failed", "file", filepath.Base(record.FilePath), "error", err, "component", "tag_writer")
	}

	return nil // non-fatal
}
