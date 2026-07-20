package playlist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/sanitize"
)

// Service orchestrates playlist import and sync.
type Service struct {
	srcReg      *Registry
	store       library.Store
	downloadReg *download.Registry
	downloadSvc *download.DownloadService
	matcher     *matching.Engine
	cfgFn       func() config.Config
	syncMu      sync.Mutex
	syncing     map[int64]bool // playlistIDs currently being synced
}

// NewService creates a playlist service.
func NewService(srcReg *Registry, store library.Store, downloadReg *download.Registry, downloadSvc *download.DownloadService, cfgFn func() config.Config) *Service {
	return &Service{
		srcReg:      srcReg,
		store:       store,
		downloadReg: downloadReg,
		downloadSvc: downloadSvc,
		matcher:     matching.New(),
		cfgFn:       cfgFn,
		syncing:     make(map[int64]bool),
	}
}

// Sources returns all registered playlist sources.
func (s *Service) Sources() []Source {
	return s.srcReg.Configured()
}

// BrowseSource fetches all playlists from a source and marks which are already imported.
func (s *Service) BrowseSource(ctx context.Context, sourceName string) ([]SourcePlaylistItem, error) {
	src := s.srcReg.Get(sourceName)
	if src == nil {
		return nil, fmt.Errorf("playlist source %q not found", sourceName)
	}

	playlists, err := src.GetUserPlaylists(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch playlists: %w", err)
	}

	imported, _ := s.store.ListPlaylists(ctx)
	importedIDs := make(map[string]bool)
	for _, p := range imported {
		if p.Source == sourceName {
			importedIDs[p.SourcePlaylistID] = true
		}
	}

	var out []SourcePlaylistItem
	for _, p := range playlists {
		out = append(out, SourcePlaylistItem{
			SourceID:    p.SourceID,
			Name:        p.Name,
			Description: p.Description,
			TrackCount:  p.TrackCount,
			CoverURL:    p.CoverURL,
			OwnerName:   p.OwnerName,
			Imported:    importedIDs[p.SourceID],
		})
	}
	return out, nil
}

// ─── Import ───────────────────────────────────────────────────────────

// ImportResult holds the result of a playlist import.
type ImportResult struct {
	Playlist  *domain.Playlist
	Tracks    []domain.PlaylistTrack
	Linked    int
	Unmatched int
}

// ImportPlaylist imports a playlist from a source: saves tracks and links existing library matches.
// Imports playlist metadata and tracks from a source.
// On first import, unmatched tracks are automatically queued for download.
func (s *Service) ImportPlaylist(ctx context.Context, sourceName, sourcePlaylistID string) (*ImportResult, error) {
	src := s.srcReg.Get(sourceName)
	if src == nil {
		return nil, fmt.Errorf("playlist source %q not found", sourceName)
	}

	trackInfos, playlistName, err := src.GetPlaylistTracks(ctx, sourcePlaylistID)
	if err != nil {
		return nil, fmt.Errorf("fetch playlist tracks: %w", err)
	}

	playlists, err := src.GetUserPlaylists(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch user playlists: %w", err)
	}

	playlistRecord, err := s.upsertPlaylist(ctx, sourceName, sourcePlaylistID, playlistName, playlists, len(trackInfos))
	if err != nil {
		return nil, err
	}

	result := &ImportResult{Playlist: playlistRecord}

	if err := s.store.DeletePlaylistTracks(ctx, playlistRecord.ID); err != nil {
		return nil, fmt.Errorf("clear playlist tracks: %w", err)
	}

	for i, info := range trackInfos {
		pt := domain.PlaylistTrack{
			PlaylistID:    playlistRecord.ID,
			Position:      i + 1,
			SourceTrackID: info.SourceTrackID,
			Title:         info.Title,
			Artist:        info.Artist,
			Album:         info.Album,
			DurationMs:    info.DurationMs,
			ISRC:         info.ISRC,
		}

		if trackID := s.findInLibrary(ctx, info); trackID != 0 {
			pt.TrackID = &trackID
			result.Linked++
		} else {
			result.Unmatched++
		}

		if err := s.store.UpsertPlaylistTrack(ctx, &pt); err != nil {
			log.Printf("playlist: save track %s: %v", info.Title, err)
		}
		result.Tracks = append(result.Tracks, pt)
	}

	log.Printf("playlist: imported %q — %d tracks (%d linked, %d unmatched)",
		playlistRecord.Name, len(trackInfos), result.Linked, result.Unmatched)

	// Build playlist folder from linked tracks (background context — outlives request).
	go s.buildPlaylistFolder(context.Background(), playlistRecord.ID)

	// Auto-trigger downloads for unmatched tracks on first import.
	if result.Unmatched > 0 {
		go func() {
			if _, err := s.DownloadMissing(context.Background(), playlistRecord.ID); err != nil {
				log.Printf("playlist: auto-download %q: %v", playlistRecord.Name, err)
			}
		}()
	}

	return result, nil
}

// ─── Download Missing ─────────────────────────────────────────────────

// DownloadMissing queues downloads for all unmatched tracks in a playlist.
func (s *Service) DownloadMissing(ctx context.Context, playlistID int64) (int, error) {
	tracks, err := s.store.GetPlaylistTracks(ctx, playlistID)
	if err != nil {
		return 0, err
	}

	queued := 0
	for _, pt := range tracks {
		if pt.TrackID != nil {
			continue
		}

		// Search across all configured sources for a match and queue it.
		id, _, _, dlErr := s.findAndQueueDownload(ctx, pt.Title, pt.Artist, pt.DurationMs, "", playlistID)
		if dlErr != nil {
			log.Printf("playlist: download %s - %s: %v", pt.Artist, pt.Title, dlErr)
			continue
		}
		_ = id
		queued++
	}

	log.Printf("playlist: download missing: queued %d tracks", queued)

	// Trigger background sync — waits for downloads, re-links, builds playlist folder.
	if queued > 0 {
		go s.syncPlaylistGuarded(playlistID)
	}

	return queued, nil
}

// syncPlaylistGuarded runs SyncPlaylist with a per-playlist mutex to prevent
// concurrent syncs of the same playlist (e.g., from double-click or overlapping
// auto-sync).
func (s *Service) syncPlaylistGuarded(playlistID int64) {
	s.syncMu.Lock()
	if s.syncing[playlistID] {
		s.syncMu.Unlock()
		log.Printf("playlist: sync %d already in progress, skipping", playlistID)
		return
	}
	s.syncing[playlistID] = true
	s.syncMu.Unlock()

	defer func() {
		s.syncMu.Lock()
		delete(s.syncing, playlistID)
		s.syncMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := s.SyncPlaylist(ctx, playlistID); err != nil {
		log.Printf("playlist: background sync %d failed: %v", playlistID, err)
	}
}

// findAndQueueDownload searches across configured sources for a matching track
// and queues the best candidate via the download service.
func (s *Service) findAndQueueDownload(ctx context.Context, title, artist string, durationMs int64, excludeSource string, playlistID int64) (downloadID, sourceName string, confidence float64, err error) {
	orch := download.NewOrchestrator(s.downloadReg, func() config.QualityConfig {
		return s.cfgFn().Quality
	})

	best, err := orch.FindBestMatch(ctx, title, artist, durationMs, excludeSource)
	if err != nil {
		return "", "", 0, err
	}

	username := best.Track.Username

	id, dlErr := s.downloadSvc.Queue(ctx, best.SourceName, username, best.Track.Filename, best.Track.Size, download.DownloadMeta{
		Artist:      best.Track.Artist,
		Album:       best.Track.Album,
		Title:       best.Track.Title,
		TrackNumber: best.Track.TrackNumber,
		PlaylistID:  strconv.FormatInt(playlistID, 10),
	})
	if dlErr != nil {
		return "", "", best.Score, fmt.Errorf("queue download: %w", dlErr)
	}

	return id, best.SourceName, best.Score, nil
}

// ─── Sync (re-link) ───────────────────────────────────────────────────

// SyncPlaylist re-scans the library and links any newly imported tracks.
func (s *Service) SyncPlaylist(ctx context.Context, playlistID int64) error {
	p, err := s.store.GetPlaylist(ctx, playlistID)
	if err != nil || p == nil {
		return errors.New("playlist not found")
	}

	// Wait for any in-progress downloads to complete before scanning.
	if err := s.waitForDownloads(ctx); err != nil {
		log.Printf("playlist: download wait: %v", err)
		// Continue anyway — scanner will pick up whatever is already done.
	}

	// Refresh track list from source (catches reordering, additions, removals).
	src := s.srcReg.Get(p.Source)
	if src != nil {
		trackInfos, _, fetchErr := src.GetPlaylistTracks(ctx, p.SourcePlaylistID)
		if fetchErr == nil && len(trackInfos) > 0 {
			// Compare old vs new positions for logging.
			oldTracks, _ := s.store.GetPlaylistTracks(ctx, playlistID)
			oldPos := make(map[string]int) // sourceTrackID → old position
			for _, ot := range oldTracks {
				oldPos[ot.SourceTrackID] = ot.Position
			}
			for i, info := range trackInfos {
				newPos := i + 1
				if old, ok := oldPos[info.SourceTrackID]; ok && old != newPos {
					log.Printf("playlist: %s moved position %d → %d", info.Title, old, newPos)
				}
			}

			s.store.DeletePlaylistTracks(ctx, playlistID)
			for i, info := range trackInfos {
				pt := domain.PlaylistTrack{
					PlaylistID: playlistID, Position: i + 1,
					SourceTrackID: info.SourceTrackID,
					Title: info.Title, Artist: info.Artist,
					Album: info.Album, DurationMs: info.DurationMs,
				}
				if trackID := s.findInLibrary(ctx, info); trackID != 0 {
					pt.TrackID = &trackID
				}
				s.store.UpsertPlaylistTrack(ctx, &pt)
			}
			p.TrackCount = len(trackInfos)
		}
	}

	// Scan download, library, and playlist paths to import newly downloaded files.
	cfg := s.cfgFn()
	scanner := library.NewScanner(s.store)
	for _, path := range []string{cfg.Library.DownloadPath, cfg.Library.LibraryPath, cfg.Library.PlaylistPath} {
		if path == "" {
			continue
		}
		scanner.ScanPath(ctx, path)
	}

	// Re-link tracks after scan.
	tracks, _ := s.store.GetPlaylistTracks(ctx, playlistID)
	for i := range tracks {
		if tracks[i].TrackID != nil {
			continue
		}
		info := TrackInfo{
			SourceTrackID: tracks[i].SourceTrackID,
			Title: tracks[i].Title, Artist: tracks[i].Artist,
			DurationMs: tracks[i].DurationMs,
		}
		if trackID := s.findInLibrary(ctx, info); trackID != 0 {
			tracks[i].TrackID = &trackID
			s.store.UpsertPlaylistTrack(ctx, &tracks[i])
		}
	}

	p.SyncedAt = time.Now().UTC().Format(time.RFC3339)
	s.store.UpsertPlaylist(ctx, p)

	log.Printf("playlist: synced %q — %d tracks", p.Name, p.TrackCount)

	// Build playlist folder.
	// Uses background context — the caller may cancel ctx after SyncPlaylist returns.
	go s.buildPlaylistFolder(context.Background(), playlistID)
	return nil
}

// buildPlaylistFolder creates the playlist directory structure from linked tracks.
func (s *Service) buildPlaylistFolder(ctx context.Context, playlistID int64) {
	playlist, err := s.store.GetPlaylist(ctx, playlistID)
	if err != nil || playlist == nil {
		return
	}
	tracks, err := s.store.GetPlaylistTracks(ctx, playlistID)
	if err != nil {
		return
	}

	cfg := s.cfgFn()
	root := cfg.Library.PlaylistPath

	linkedCount := 0
	for _, pt := range tracks {
		if pt.TrackID != nil {
			linkedCount++
		}
	}
	log.Printf("playlist: build folder %q → %s — %d/%d tracks linked",
		playlist.Name, root, linkedCount, len(tracks))

	if root == "" {
		log.Printf("playlist: build folder %q skipped — playlist_path not configured", playlist.Name)
		return
	}
	template := cfg.Library.PlaylistTemplate
	if template == "" {
		template = "{position:02d} {artist} - {title}"
	}

	// Create playlist directory.
	playlistDir := filepath.Join(root, sanitize.DirName(playlist.Name))
	if err := os.MkdirAll(playlistDir, 0o755); err != nil {
		log.Printf("playlist: mkdir %s: %v", playlistDir, err)
		return
	}

	renamer := library.NewPlaylistRenamer(template, playlistDir)
	written := 0

	for _, pt := range tracks {
		if pt.TrackID == nil {
			continue
		}
		// Get the library track to find its file path.
		track, err := s.store.GetTrack(ctx, *pt.TrackID)
		if err != nil || track == nil || track.FilePath == "" {
			continue
		}

		ext := strings.TrimPrefix(filepath.Ext(track.FilePath), ".")
		destPath := renamer.ResolvePath(pt.Position, pt.Artist, pt.Title, ext)
		if destPath == "" || destPath == track.FilePath {
			continue
		}

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			log.Printf("playlist: mkdir %s: %v", filepath.Dir(destPath), err)
			continue
		}

		// Copy file to playlist folder (library copy stays intact).
		if err := copyFile(track.FilePath, destPath); err != nil {
			log.Printf("playlist: copy %s → %s: %v", track.FilePath, destPath, err)
			continue
		}
		written++
	}

	log.Printf("playlist: built folder %q — %d tracks", playlist.Name, written)

	// Clean up orphaned files from old positions.
	keepFiles := make(map[string]bool)
	for _, pt := range tracks {
		if pt.TrackID == nil {
			continue
		}
		track, _ := s.store.GetTrack(ctx, *pt.TrackID)
		if track == nil {
			continue
		}
		ext := strings.TrimPrefix(filepath.Ext(track.FilePath), ".")
		destPath := renamer.ResolvePath(pt.Position, pt.Artist, pt.Title, ext)
		if destPath != "" {
			keepFiles[filepath.Base(destPath)] = true
		}
	}
	entries, _ := os.ReadDir(playlistDir)
	for _, e := range entries {
		if !e.IsDir() && !keepFiles[e.Name()] {
			path := filepath.Join(playlistDir, e.Name())
			if err := os.Remove(path); err == nil {
				log.Printf("playlist: removed orphaned file %s", e.Name())
			}
		}
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────

// waitForDownloads polls the download service until all downloads reach a terminal state,
// or until the context is cancelled or a timeout is reached.
func (s *Service) waitForDownloads(ctx context.Context) error {
	const (
		pollInterval = 2 * time.Second
		maxWait      = 5 * time.Minute
	)
	deadline := time.Now().Add(maxWait)

	for {
		downloads, err := s.downloadSvc.List(ctx)
		if err != nil {
			return err
		}
		pending := false
		for _, d := range downloads {
			if !d.State.Terminal() {
				pending = true
				break
			}
		}
		if !pending {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for downloads")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// upsertPlaylist finds or creates a playlist record.
func (s *Service) upsertPlaylist(ctx context.Context, source, sourceID, sourceName string, candidates []PlaylistInfo, trackCount int) (*domain.Playlist, error) {
	existing, _ := s.store.GetPlaylistBySourceID(ctx, source, sourceID)
	if existing != nil {
		existing.TrackCount = trackCount
		if _, err := s.store.UpsertPlaylist(ctx, existing); err != nil {
			return nil, err
		}
		return existing, nil
	}

	var name, desc, cover, owner string
	for _, p := range candidates {
		if p.SourceID == sourceID {
			name = p.Name
			desc = p.Description
			cover = p.CoverURL
			owner = p.OwnerName
			break
		}
	}
	if name == "" {
		name = sourceName // from pagePlaylist DATA
	}
	if name == "" {
		name = fmt.Sprintf("%s playlist %s", source, sourceID)
	}

	p := &domain.Playlist{
		Source:           source,
		SourcePlaylistID: sourceID,
		Name:             name,
		Description:      desc,
		TrackCount:       trackCount,
		CoverURL:         cover,
		OwnerName:        owner,
		IsPublic:         true,
	}
	id, err := s.store.UpsertPlaylist(ctx, p)
	if err != nil {
		return nil, err
	}
	p.ID = id
	return p, nil
}

// findInLibrary searches the library for a matching track.
func (s *Service) findInLibrary(ctx context.Context, info TrackInfo) int64 {
	tracks, err := s.store.SearchTracks(ctx, info.Title, 20)
	if err != nil {
		return 0
	}

	artistNorm := strings.ToLower(strings.TrimSpace(info.Artist))
	artistCache := make(map[int64]string)
	for _, t := range tracks {
		if _, ok := artistCache[t.ArtistID]; ok {
			continue
		}
		artist, _ := s.store.GetArtist(ctx, t.ArtistID)
		if artist != nil {
			artistCache[t.ArtistID] = artist.Name
		}
	}

	// Exact title + artist match.
	for _, t := range tracks {
		name := artistCache[t.ArtistID]
		if strings.ToLower(strings.TrimSpace(name)) == artistNorm &&
			strings.EqualFold(t.Title, info.Title) {
			return t.ID
		}
	}

	// Fuzzy duration-based matching.
	for _, t := range tracks {
		name := artistCache[t.ArtistID]
		if name == "" {
			continue
		}
		score, _ := s.matcher.ScoreTrackMatch(
			info.Title, []string{info.Artist}, info.DurationMs,
			t.Title, []string{name}, t.Duration,
		)
		if score >= 0.85 {
			return t.ID
		}
	}

	return 0
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = io.Copy(d, s)
	return err
}
