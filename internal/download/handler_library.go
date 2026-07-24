package download

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// LibraryImporterHandler creates artist, album, and track records in the
// library store for a single downloaded file, similar to how the scanner
// handles discovered audio files.
type LibraryImporterHandler struct {
	libStore library.Store
	log      *slog.Logger
}

// NewLibraryImporterHandler creates a handler that imports downloaded files
// into the music library by creating/updating artist, album, and track records.
func NewLibraryImporterHandler(libStore library.Store, logger *slog.Logger) *LibraryImporterHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &LibraryImporterHandler{libStore: libStore, log: logger}
}

// Handle creates artist/album/track records for the downloaded file.
// Metadata is extracted from the file path (post-renamer) and audio tags.
func (h *LibraryImporterHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" {
		return fmt.Errorf("library importer: no file path for %s", record.ID)
	}

	// Check if already imported by file path (renamer always produces absolute paths).
	existing, err := h.libStore.GetTrackByFilePath(ctx, record.FilePath)
	if err == nil && existing != nil {
		record.LibraryTrackID = existing.ID
		// Update size from filesystem so SSE completion shows real numbers.
		if fi, err := os.Stat(record.FilePath); err == nil {
			record.Size = fi.Size()
			record.Transferred = fi.Size()
		}
		return nil
	}

	trackTitle, artistName, albumTitle := h.extractMetadata(record)

	// Get file info for size.
	fi, err := os.Stat(record.FilePath)
	if err != nil {
		h.log.Error("stat file failed", "path", record.FilePath, "error", err, "component", "library_importer")
		return fmt.Errorf("library importer: stat file: %w", err)
	}

	// Update download record with actual file size for SSE completion event.
	record.Size = fi.Size()
	record.Transferred = fi.Size()

	track := &domain.Track{
		Title:       trackTitle,
		TrackNumber: record.TrackNumber,
		DiscNumber:  record.DiscNumber,
		FilePath:    record.FilePath,
		FileSize:    fi.Size(),
		ISRC:        record.ISRC,
	}

	// Copy external IDs from record to track.
	if record.TrackID != "" {
		if track.ExternalIDs == nil {
			track.ExternalIDs = make(map[string]string)
		}
		track.ExternalIDs[record.SourceName] = record.TrackID
	}

	trackID, err := h.libStore.ImportTrack(ctx, track, artistName, albumTitle, record.Year, nil)
	if err != nil {
		h.log.Error("import track failed", "artist", artistName, "title", trackTitle, "error", err, "component", "library_importer")
		return fmt.Errorf("library importer: import track: %w", err)
	}

	record.LibraryTrackID = trackID
	return nil
}

// extractMetadata pulls artist/album/title from the record and file path.
func (h *LibraryImporterHandler) extractMetadata(record *domain.DownloadRecord) (title, artist, album string) {
	artist = record.Artist
	album = record.Album
	title = record.Title

	// Fall back to file path parsing when ALL fields are missing.
	if artist == "" && album == "" && title == "" {
		artist, album, title = library.ParseFileMetadata(record.FilePath)
	}

	if artist == "" {
		artist = library.DefaultArtistName
	}
	// album stays empty when unknown — post-import enrichment (MetadataEnrichmentHandler)
	// will attempt to resolve it via MusicBrainz. "Unknown Album" is dishonest metadata.
	if title == "" {
		title = filepath.Base(record.FilePath)
	}

	return title, artist, album
}
