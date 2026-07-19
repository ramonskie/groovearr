package download

import (
	"context"
	"fmt"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// FileRenamerHandler renames a downloaded file into the library directory
// using the library.Renamer path resolver.
type FileRenamerHandler struct {
	renamer *library.Renamer
	store   DownloadStore
}

// NewFileRenamerHandler creates a handler that moves downloaded files into
// the library directory structure after each download completes.
func NewFileRenamerHandler(renamer *library.Renamer, store DownloadStore) *FileRenamerHandler {
	return &FileRenamerHandler{renamer: renamer, store: store}
}

// Handle calls library.Renamer.Rename using the record's embedded metadata
// and persists the new file path to the store.
func (h *FileRenamerHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" {
		return fmt.Errorf("renamer: no file path in download record %s", record.ID)
	}

	meta := library.FileMeta{
		Artist:   record.Artist,
		Album:    record.Album,
		Title:    record.Title,
		Year:     record.Year,
		TrackNum: record.TrackNumber,
		DiscNum:  record.DiscNumber,
	}

	newPath, err := h.renamer.Rename(record.FilePath, meta)
	if err != nil {
		return fmt.Errorf("renamer: rename %s: %w", record.Filename, err)
	}

	if newPath != record.FilePath {
		record.FilePath = newPath
		if err := h.store.Update(ctx, record); err != nil {
			return fmt.Errorf("renamer: store update: %w", err)
		}
	}

	return nil
}
