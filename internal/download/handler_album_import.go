// Package download provides download management and orchestration.
package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
)

// AlbumImportHandler processes completed album downloads. Its only job is
// album-specific logic: scan the download folder, resolve tracks against the
// actual file count, match files to expected tracks, then feed each matched
// track through the standard import chain.
type AlbumImportHandler struct {
	chain         []ImportHandler
	trackResolver TrackResolver
	dlStore       Store
	libStore      libraryStore
	db            *sql.DB
	log           *slog.Logger
}

// libraryStore is the minimal interface for querying library data.
type libraryStore interface {
	GetTrack(ctx context.Context, id int64) (*domain.Track, error)
	GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error)
	GetAlbum(ctx context.Context, id int64) (*domain.Album, error)
}

// TrackResolver resolves the expected track listing for an album release.
// Returns the tracks, the resolved MusicBrainz release MBID, and any error.
type TrackResolver func(ctx context.Context, sourceName, artist, album string, fileCount int, torrentTitle string) (tracks []domain.ExpectedTrack, mbid string, err error)

// NewAlbumImportHandler creates an AlbumImportHandler that delegates all
// per-track processing to the standard import handler chain.
func NewAlbumImportHandler(chain []ImportHandler, resolver TrackResolver, dlStore Store, libStore libraryStore, db *sql.DB, logger *slog.Logger) *AlbumImportHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AlbumImportHandler{
		chain:         chain,
		trackResolver: resolver,
		dlStore:       dlStore,
		libStore:      libStore,
		db:            db,
		log:           logger,
	}
}

// Handle imports a completed album download by scanning the folder, matching
// files to expected tracks, and feeding each track through the standard
// import handler chain.
func (h *AlbumImportHandler) Handle(ctx context.Context, record *Record) error {
	if !record.IsAlbum() {
		return fmt.Errorf("album import: record %s is not an album download", record.ID)
	}

	folderPath := record.FilePath
	if folderPath == "" {
		folderPath = record.FolderPath
	}

	// 1. Find audio files in the downloaded folder.
	audioFiles, err := h.scanAudioFiles(folderPath)
	if err != nil {
		return fmt.Errorf("album import: scan folder: %w", err)
	}
	if len(audioFiles) == 0 {
		return fmt.Errorf("album import: no audio files found in %s", folderPath)
	}

	// 2. Resolve expected tracks using the actual file count to pick
	// the best matching MusicBrainz release (e.g., deluxe vs standard).
	tracks := record.AlbumTracks
	if h.trackResolver != nil {
		h.log.Info("album import: resolving tracks with file count",
			"file_count", len(audioFiles), "component", "album_import")
		resolved, mbid, err := h.trackResolver(ctx, record.SourceName, record.Artist, record.Album, len(audioFiles), record.Filename)
		if err != nil {
			h.log.Warn("album import: track resolution failed, falling back to filename matching",
				"error", err, "component", "album_import")
		} else if len(resolved) > 0 {
			tracks = resolved
			record.AlbumMBID = mbid
			h.log.Info("album import: tracks resolved",
				"count", len(resolved), "mbid", mbid, "component", "album_import")
		}
	}

	// 3. Match files to expected tracks.
	matches := h.matchFiles(audioFiles, tracks)

	// 3. Feed each matched file through the standard import chain.
	var importedIDs []int64
	for i, m := range matches {
		artist := m.track.Artist
		if artist == "" {
			artist = record.Artist
		}

		// Create a synthetic single-track record for the chain to process.
		// The chain sets LibraryTrackID during LibraryImporterHandler.Handle().
		synth := &Record{
			ID:          fmt.Sprintf("%s-t%02d", record.ID, i+1),
			SourceName:  record.SourceName,
			State:       StateImporting,
			DisplayName: fmt.Sprintf("%s - %s", artist, m.track.Title),
			FilePath:    m.filePath,
			Artist:      artist,
			Album:       record.Album,
			Title:       m.track.Title,
			TrackNumber: m.track.TrackNumber,
			Year:        record.Year,
			CoverURL:    record.CoverURL,
			AlbumMBID:   record.AlbumMBID,
		}

		// Insert into store so handlers that call store.Update() (e.g.,
		// FileRenamerHandler) work correctly. We'll remove it after the chain.
		if err := h.dlStore.Insert(ctx, synth); err != nil {
			h.log.Warn("album import: failed to insert synthetic record",
				"track", m.track.Title, "error", err, "component", "album_import")
			continue
		}

		// Run the standard import chain.
		var chainErr error
		for _, handler := range h.chain {
			if err := handler.Handle(ctx, synth); err != nil {
				chainErr = err
				break
			}
		}

		if chainErr != nil {
			h.log.Warn("album import: import chain failed for track",
				"track", m.track.Title, "error", chainErr, "component", "album_import")
		}

		// Delete synthetic record — it was only needed for the chain to run.
		// The real library record lives in libStore.
		_ = h.dlStore.Delete(ctx, synth.ID)

		if synth.LibraryTrackID > 0 {
			importedIDs = append(importedIDs, synth.LibraryTrackID)
		}
	}

	// 4. Link imported tracks to parent record.
	if len(importedIDs) > 0 {
		record.ImportedTrackIDs = importedIDs
		if err := h.dlStore.Update(ctx, record); err != nil {
			return fmt.Errorf("album import: store update: %w", err)
		}
	}

	// 5. Update discovery cache so the UI shows the actual downloaded tracks,
	// not stale discovery provider data (e.g., standard vs deluxe edition).
	h.updateDiscoveryCache(ctx, importedIDs)

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

// scanAudioFiles finds all audio files in a directory, recursing into
// subdirectories (e.g., CD1/, CD2/ in multi-disc torrents).
func (h *AlbumImportHandler) scanAudioFiles(folderPath string) ([]string, error) {
	exts := map[string]bool{
		".flac": true, ".mp3": true, ".ogg": true, ".wav": true,
		".m4a": true, ".aac": true, ".wma": true, ".opus": true,
		".dsf": true, ".dff": true, ".aiff": true, ".ape": true, ".wv": true,
	}
	var files []string
	err := filepath.WalkDir(folderPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if exts[strings.ToLower(filepath.Ext(d.Name()))] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// matchFiles pairs filesystem audio files with expected tracks using
// track number heuristics and title comparison.
// When expected tracks are unavailable or insufficient, falls back to
// filename-only matching using track number and cleaned title.
func (h *AlbumImportHandler) matchFiles(files []string, tracks []domain.ExpectedTrack) []fileMatch {
	byNumber := make(map[int]domain.ExpectedTrack)
	for _, t := range tracks {
		byNumber[t.TrackNumber] = t
	}

	var matches []fileMatch
	tracked := make(map[int]bool) // avoid reusing an expected track for multiple files

fileLoop:
	for _, f := range files {
		base := filepath.Base(f)
		trackNum := extractTrackNumber(base)

		// Try expected track lookup.
		if track, ok := byNumber[trackNum]; ok && !tracked[trackNum] {
			tracked[trackNum] = true
			matches = append(matches, fileMatch{
				filePath: f,
				track:    albumImportTrack{TrackNumber: track.TrackNumber, Artist: track.Artist, Title: track.Title},
			})
			continue
		}

		// Position-based fallback.
		if trackNum > 0 && trackNum <= len(tracks) && !tracked[trackNum] {
			track := tracks[trackNum-1]
			if track.TrackNumber > 0 {
				tracked[trackNum] = true
				matches = append(matches, fileMatch{
					filePath: f,
					track:    albumImportTrack{TrackNumber: track.TrackNumber, Artist: track.Artist, Title: track.Title},
				})
				continue
			}
		}

		// Title substring match.
		if trackNum <= 0 {
			for _, t := range tracks {
				if t.Title != "" && !tracked[t.TrackNumber] && strings.Contains(strings.ToLower(base), strings.ToLower(t.Title)) {
					tracked[t.TrackNumber] = true
					matches = append(matches, fileMatch{
						filePath: f,
						track:    albumImportTrack{TrackNumber: t.TrackNumber, Artist: t.Artist, Title: t.Title},
					})
					continue fileLoop
				}
			}
		}

		// Fallback: match by filename when expected tracks are unavailable.
		// Extract track title from filename: "01. Battery.mp3" → "Battery".
		cleanedTitle := cleanTrackFilename(base)
		if cleanedTitle == "" {
			h.log.Warn("album import: could not match file to expected track",
				"file", base, "component", "album_import")
			continue
		}

		h.log.Info("album import: fallback match (no expected track metadata)",
			"file", base, "track_num", trackNum, "title", cleanedTitle, "component", "album_import")

		matches = append(matches, fileMatch{
			filePath: f,
			track:    albumImportTrack{TrackNumber: trackNum, Title: cleanedTitle},
		})
	}
	return matches
}

// cleanTrackFilename strips the track number prefix and extension from a filename
// to extract a clean track title. "01. Battery.mp3" → "Battery".
func cleanTrackFilename(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	for i, ch := range base {
		if ch == '.' || ch == ' ' || ch == '-' || ch == '_' {
			if i > 0 {
				rest := strings.TrimSpace(base[i+1:])
				// Strip additional leading separators (e.g. " - Battery" → "Battery").
				rest = strings.TrimLeft(rest, " .-_")
				if rest != "" {
					return rest
				}
			}
		}
	}
	return base
}

// extractTrackNumber tries to find a track number at the beginning of a filename.
// Handles formats: "01 - Title.flac", "1. Title.flac", "01 Title.flac".
// Returns 0 if no plausible track number found (caps at 99 to avoid
// misidentifying numeric song titles like "99 Red Balloons" or "1979").
func extractTrackNumber(name string) int {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if idx := strings.IndexAny(base, " .-_"); idx > 0 {
		if n, err := strconv.Atoi(base[:idx]); err == nil && n > 0 && n <= 99 {
			return n
		}
	}
	if n, err := strconv.Atoi(base); err == nil && n > 0 && n <= 99 {
		return n
	}
	return 0
}

// updateDiscoveryCache rebuilds the album_discovery_cache with the actual
// library tracks so the UI shows what was really downloaded, not stale
// discovery provider data (e.g., standard edition vs deluxe/redux).
func (h *AlbumImportHandler) updateDiscoveryCache(ctx context.Context, trackIDs []int64) {
	if len(trackIDs) == 0 || h.libStore == nil {
		return
	}

	// Get the album ID from the first imported track.
	firstTrack, err := h.libStore.GetTrack(ctx, trackIDs[0])
	if err != nil || firstTrack == nil {
		return
	}
	album, err := h.libStore.GetAlbum(ctx, firstTrack.AlbumID)
	if err != nil || album == nil {
		return
	}

	// Get all tracks in the album from the library.
	libTracks, err := h.libStore.GetTracksByAlbum(ctx, album.ID)
	if err != nil {
		h.log.Warn("album import: cache update failed to get tracks",
			"album_id", album.ID, "error", err, "component", "album_import")
		return
	}

	// Build discovery-compatible track entries.
	type cacheTrack struct {
		Title       string `json:"title"`
		TrackNumber int    `json:"track_number"`
		DurationMs  int64  `json:"duration_ms"`
	}
	cacheTracks := make([]cacheTrack, 0, len(libTracks))
	for _, t := range libTracks {
		cacheTracks = append(cacheTracks, cacheTrack{
			Title:       t.Title,
			TrackNumber: t.TrackNumber,
			DurationMs:  int64(t.Duration),
		})
	}
	tracksJSON, err := json.Marshal(cacheTracks)
	if err != nil {
		h.log.Warn("album import: cache update marshal failed", "error", err, "component", "album_import")
		return
	}

	// Write to discovery cache via the download store's DB connection.
	db := h.db
	if db == nil {
		return
	}
	_, err = db.ExecContext(ctx,
		`INSERT OR REPLACE INTO album_discovery_cache (album_id, provider_name, provider_album_id, tracks_json, cached_at)
		 VALUES (?, 'library', '', ?, ?)`,
		album.ID, string(tracksJSON), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		h.log.Warn("album import: cache update write failed",
			"album_id", album.ID, "error", err, "component", "album_import")
		return
	}
	h.log.Info("album import: discovery cache updated",
		"album_id", album.ID, "tracks", len(cacheTracks), "component", "album_import")
}
