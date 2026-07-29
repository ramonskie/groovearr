// Package download provides download management and orchestration.
package download

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/tagging"
)

// AlbumImportHandler processes completed album downloads: scans the
// downloaded folder, matches files to expected tracks, tags and renames
// each file, and links all imported library tracks to the parent
// download record.
type AlbumImportHandler struct {
	tagger      *tagging.Tagger
	renamer     *library.Renamer // standard template
	compRenamer *library.Renamer // compilation template
	coverHandler *CoverArtHandler // downloads cover art (may be nil)
	libStore    library.Store
	dlStore     Store
	log         *slog.Logger
}

// NewAlbumImportHandler creates an AlbumImportHandler.
// compRenamer may be nil — compilations then use the standard renamer.
// coverHandler may be nil — cover art download is skipped.
func NewAlbumImportHandler(tagger *tagging.Tagger, renamer, compRenamer *library.Renamer,
	coverHandler *CoverArtHandler, libStore library.Store, dlStore Store, logger *slog.Logger) *AlbumImportHandler {

	if logger == nil {
		logger = slog.Default()
	}
	return &AlbumImportHandler{
		tagger:       tagger,
		renamer:      renamer,
		compRenamer:  compRenamer,
		coverHandler: coverHandler,
		libStore:     libStore,
		dlStore:      dlStore,
		log:          logger,
	}
}

// Handle imports a completed album download.
func (h *AlbumImportHandler) Handle(ctx context.Context, record *Record) error {
	if !record.IsAlbum() {
		return fmt.Errorf("album import: record %s is not an album download", record.ID)
	}

	folderPath := record.FilePath // set by DownloadClient.GetStatus via UpdateProgress
	if folderPath == "" {
		folderPath = record.FolderPath // fallback if set directly
	}

	// 1. Download cover art if available and handler is configured.
	if h.coverHandler != nil && record.CoverURL != "" {
		// Temporarily set FilePath to the folder for cover download.
		saved := record.FilePath
		record.FilePath = folderPath
		_ = h.coverHandler.Handle(ctx, record)
		record.FilePath = saved
	}

	// 2. Find audio files in the downloaded folder.
	audioFiles, err := h.scanAudioFiles(folderPath)
	if err != nil {
		return fmt.Errorf("album import: scan folder: %w", err)
	}
	if len(audioFiles) == 0 {
		return fmt.Errorf("album import: no audio files found in %s", folderPath)
	}

	// 2. Match files to expected tracks.
	matches := h.matchFiles(audioFiles, record.AlbumTracks)

	// 3. Select renamer.
	isComp := record.IsCompilation()
	rn := h.renamer
	if isComp && h.compRenamer != nil {
		rn = h.compRenamer
	}

	// 4. Import each matched file.
	var importedIDs []int64
	for _, m := range matches {
		artist := m.track.Artist
		if artist == "" {
			artist = record.Artist
		}

		// Tag the file. Cover art download is handled separately by the
		// CoverArtHandler in the main import chain. WriteTags only uses
		// coverPath to embed existing local files.
		if err := h.tagger.WriteTags(m.filePath, artist, record.Album, m.track.Title, ""); err != nil {
			h.log.Warn("album import: tag write failed", "file", m.filePath, "error", err, "component", "album_import")
		}

		// Rename into library.
		dest, err := rn.Rename(m.filePath, library.FileMeta{
			Artist:   artist,
			Album:    record.Album,
			Title:    m.track.Title,
			TrackNum: m.track.TrackNumber,
			Year:     record.Year,
		})
		if err != nil {
			h.log.Warn("album import: rename failed", "file", m.filePath, "error", err, "component", "album_import")
			continue
		}

		// Add to library via ImportTrack which handles artist/album creation.
		trackID, err := h.libStore.ImportTrack(ctx, &domain.Track{
			Title:       m.track.Title,
			TrackNumber: m.track.TrackNumber,
			FilePath:    dest,
		}, artist, record.Album, record.Year, nil)
		if err != nil {
			h.log.Warn("album import: library upsert failed", "artist", artist, "title", m.track.Title, "error", err, "component", "album_import")
			continue
		}
		importedIDs = append(importedIDs, trackID)
	}

	// 5. Link imported tracks to parent record.
	if len(importedIDs) > 0 {
		record.ImportedTrackIDs = importedIDs
		if err := h.dlStore.Update(ctx, record); err != nil {
			return fmt.Errorf("album import: store update: %w", err)
		}
	}

	h.log.Info("album import complete",
		"album", record.Album,
		"artist", record.Artist,
		"files_found", len(audioFiles),
		"files_imported", len(importedIDs),
		"component", "album_import",
	)

	return nil
}

// fileMatch pairs a filesystem path with an expected track.
type fileMatch struct {
	filePath string
	track    albumImportTrack
}

// albumImportTrack is a lightweight copy of expected track metadata.
type albumImportTrack struct {
	TrackNumber int
	Artist      string
	Title       string
}

// scanAudioFiles finds all audio files in a directory (non-recursive).
func (h *AlbumImportHandler) scanAudioFiles(folderPath string) ([]string, error) {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}
	exts := map[string]bool{
		".flac": true, ".mp3": true, ".ogg": true, ".wav": true,
		".m4a": true, ".aac": true, ".wma": true, ".opus": true,
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if exts[strings.ToLower(filepath.Ext(e.Name()))] {
			files = append(files, filepath.Join(folderPath, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// matchFiles pairs filesystem audio files with expected tracks using
// track number heuristics and title comparison.
func (h *AlbumImportHandler) matchFiles(files []string, tracks []domain.ExpectedTrack) []fileMatch {
	// Build lookup: trackNumber → track.
	byNumber := make(map[int]domain.ExpectedTrack)
	for _, t := range tracks {
		byNumber[t.TrackNumber] = t
	}

	var matches []fileMatch
	for _, f := range files {
		base := filepath.Base(f)

		// Extract track number from filename.
		trackNum := extractTrackNumber(base)

		// Look up expected track.
		track, ok := byNumber[trackNum]
		if !ok && trackNum > 0 && trackNum <= len(tracks) {
			// Fallback to position-based match.
			track = tracks[trackNum-1]
		}
		if !ok && trackNum <= 0 {
			// Try title substring match if no number found.
			for _, t := range tracks {
				if t.Title != "" && strings.Contains(strings.ToLower(base), strings.ToLower(t.Title)) {
					track = t
					break
				}
			}
		}
		if track.TrackNumber == 0 {
			h.log.Warn("album import: could not match file to expected track",
				"file", base, "component", "album_import")
			continue
		}

		matches = append(matches, fileMatch{
			filePath: f,
			track:    albumImportTrack{TrackNumber: track.TrackNumber, Artist: track.Artist, Title: track.Title},
		})
	}
	return matches
}

// extractTrackNumber tries to find a track number at the beginning of a filename.
// Handles formats: "01 - Title.flac", "1. Title.flac", "01 Title.flac".
// Returns 0 if no plausible track number found (caps at 99 to avoid
// misidentifying numeric song titles like "99 Red Balloons" or "1979").
func extractTrackNumber(name string) int {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	// "01 - Title"
	if idx := strings.IndexAny(base, " .-_"); idx > 0 {
		if n, err := strconv.Atoi(base[:idx]); err == nil && n > 0 && n <= 99 {
			return n
		}
	}
	// Pure number prefix — only for short numbers (plausible track counts).
	if n, err := strconv.Atoi(base); err == nil && n > 0 && n <= 99 {
		return n
	}
	return 0
}
