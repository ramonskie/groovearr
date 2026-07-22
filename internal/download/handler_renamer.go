package download

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// FileRenamerHandler renames a downloaded file into the library directory
// using the library.Renamer path resolver.
type FileRenamerHandler struct {
	renamer *library.Renamer
	store   DownloadStore
	log     *slog.Logger
}

// NewFileRenamerHandler creates a handler that moves downloaded files into
// the library directory structure after each download completes.
func NewFileRenamerHandler(renamer *library.Renamer, store DownloadStore, logger *slog.Logger) *FileRenamerHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &FileRenamerHandler{renamer: renamer, store: store, log: logger}
}

// Handle calls library.Renamer.Rename using the record's embedded metadata
// and persists the new file path to the store.
func (h *FileRenamerHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" {
		return fmt.Errorf("renamer: no file path in download record %s", record.ID)
	}

	// Resolve the actual file on disk. slskd may save the file at a different
	// path than the predicted FilePath (strips @@user/ prefixes, etc.).
	srcPath := h.resolveSourcePath(record.FilePath, record.Filename)

	meta := library.FileMeta{
		Artist:   record.Artist,
		Album:    record.Album,
		Title:    record.Title,
		Year:     record.Year,
		TrackNum: record.TrackNumber,
		DiscNum:  record.DiscNumber,
	}

	newPath, err := h.renamer.Rename(srcPath, meta)
	if err != nil {
		h.log.Error("rename failed", "filename", record.Filename, "error", err, "component", "renamer")
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

// resolveSourcePath returns the actual file path on disk. If the predicted
// path doesn't exist, it searches for a file with the same base name.
func (h *FileRenamerHandler) resolveSourcePath(filePath, filename string) string {
	if _, err := os.Stat(filePath); err == nil {
		return filePath
	}

	// Predicted path doesn't exist — search for the actual file by base name.
	// Normalize Windows backslashes for filepath.Base compatibility on Linux.
	normalized := strings.ReplaceAll(filename, "\\", "/")
	base := filepath.Base(normalized)

	// Determine the download root from the file path (usually /downloads).
	downloadRoot := filepath.Dir(filePath)
	for {
		parent := filepath.Dir(downloadRoot)
		if parent == downloadRoot || parent == "." || parent == "/" {
			break
		}
		downloadRoot = parent
	}

	var found string
	filepath.WalkDir(downloadRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Base(p), base) {
			found = p
			return filepath.SkipAll
		}
		return nil
	})

	if found != "" {
		return found
	}

	// Not found — return original path (will fail with clear error).
	return filePath
}
