package playlist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/quality"
	"github.com/ramonskie/groovearr/internal/sanitize"
)

// Service orchestrates playlist import and sync.
type Service struct {
	srcReg              *Registry
	store               library.Store
	downloadReg         *download.Registry
	downloadSvc         *download.Service
	matcher             *matching.Engine
	metadataResolver    *metadata.MetadataResolver
	cfgFn               func() config.Config
	log                 *slog.Logger
	qualityProfileStore quality.ProfileStore
	syncMu              sync.Mutex
	syncing             map[int64]bool // playlistIDs currently being synced
	autoSyncSem         chan struct{}  // limits concurrent auto-sync goroutines (capacity 3)
	folderMu            sync.Mutex    // serializes conflict-resolve + folder mkdir
}

// NewService creates a playlist service.
func NewService(srcReg *Registry, store library.Store, downloadReg *download.Registry, downloadSvc *download.Service, cfgFn func() config.Config, qualityProfileStore quality.ProfileStore, metadataResolver *metadata.MetadataResolver, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		srcReg:              srcReg,
		store:               store,
		downloadReg:         downloadReg,
		downloadSvc:         downloadSvc,
		matcher:             matching.New(),
		metadataResolver:    metadataResolver,
		cfgFn:               cfgFn,
		log:                 logger,
		qualityProfileStore: qualityProfileStore,
		syncing:             make(map[int64]bool),
		autoSyncSem:         make(chan struct{}, 3),
	}
}

// Sources returns all registered playlist sources.
func (s *Service) Sources() []Source {
	return s.srcReg.Configured()
}

// RefreshSources rebuilds the playlist source registry from download plugins.
// Call after config changes that may add/remove/modify playlist-capable sources.
func (s *Service) RefreshSources(downloadReg *download.Registry) {
	reg := NewRegistry()
	for _, p := range downloadReg.All() {
		if psp, ok := p.(PlaylistSourceProvider); ok {
			if p.IsConfigured() {
				if err := reg.Register(psp.PlaylistSource()); err != nil {
					s.log.Error("register plugin failed", "name", p.Name(), "error", err, "component", "playlist")
				}
			}
		}
	}
	s.srcReg = reg
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
func (s *Service) ImportPlaylist(ctx context.Context, sourceName, sourcePlaylistID string, syncMode string) (*ImportResult, error) {
	if syncMode == "" {
		syncMode = "mirror"
	}
	src := s.srcReg.Get(sourceName)
	if src == nil {
		return nil, fmt.Errorf("playlist source %q not found", sourceName)
	}

	trackInfos, playlistName, err := src.GetPlaylistTracks(ctx, sourcePlaylistID)
	if err != nil {
		return nil, fmt.Errorf("fetch playlist tracks: %w", err)
	}

	// Try to enrich with user playlist metadata (description, cover, owner).
	// Non-fatal — free-mode sources may not support listing user playlists.
	playlists, err := src.GetUserPlaylists(ctx)
	if err != nil {
		playlists = nil
	}

	playlistRecord, err := s.upsertPlaylist(ctx, sourceName, sourcePlaylistID, playlistName, playlists, len(trackInfos), syncMode)
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
			ISRC:          info.ISRC,
		}

		if trackID := s.findInLibrary(ctx, info); trackID != 0 {
			pt.TrackID = &trackID
			result.Linked++
		} else {
			result.Unmatched++
		}

		if err := s.store.UpsertPlaylistTrack(ctx, &pt); err != nil {
			s.log.Error("save track failed", "title", info.Title, "error", err, "component", "playlist")
		}
		result.Tracks = append(result.Tracks, pt)
	}

	s.log.Info("imported", "name", playlistRecord.Name, "total_tracks", len(trackInfos), "linked", result.Linked, "unmatched", result.Unmatched, "component", "playlist")

	// Build playlist folder from linked tracks (background context — outlives request).
	s.log.Info("building playlist folder", "playlist_id", playlistRecord.ID, "component", "playlist")
	go s.buildPlaylistFolder(context.Background(), playlistRecord.ID)

	// Auto-trigger downloads for unmatched tracks on first import.
	if result.Unmatched > 0 {
		go func() {
			if _, err := s.DownloadMissing(context.Background(), playlistRecord.ID); err != nil {
				s.log.Error("auto-download failed", "name", playlistRecord.Name, "error", err, "component", "playlist")
			}
		}()
	}

	return result, nil
}

// ─── Download Missing ─────────────────────────────────────────────────

// requeueCooldown is how long an exhausted-failed download (the monitor gave
// up after MaxRetries) must sit before the periodic playlist sync re-arms it.
// Without the gate, every sync would reset the retry budget and a permanently
// unavailable track would hammer the providers forever. With it, a stuck track
// is retried at most once per window (the monitor still paces the intra-window
// attempts with its own exponential backoff).
var requeueCooldown = 24 * time.Hour

// DownloadMissing queues downloads for all unmatched tracks in a playlist.
// Idempotent: tracks already added to the download pipeline (queued,
// downloading, importing, or failed-but-still-within-retry-budget) are
// skipped, so repeated calls — e.g. from the periodic playlist sync — never
// double-queue. A failed record whose retries are exhausted (the monitor gave
// up) is re-armed in place via Service.Retry once it has sat failed for
// requeueCooldown, reusing the record and the monitor's retry loop instead of
// creating a duplicate. Ignored (user-cancelled) records are never re-armed.
func (s *Service) DownloadMissing(ctx context.Context, playlistID int64) (int, error) {
	tracks, err := s.store.GetPlaylistTracks(ctx, playlistID)
	if err != nil {
		return 0, err
	}

	playlistIDStr := strconv.FormatInt(playlistID, 10)

	// Build the set of tracks already present in the download pipeline for
	// this playlist, keyed by ISRC and by artist|title. A track is "already
	// added" if a pipeline record exists in a state that will still make
	// progress (queued, downloading, importing, or a failed state still within
	// the monitor's automatic retry budget). A failed record whose retries are
	// exhausted (RetryCount >= MaxRetries) blocks only until requeueCooldown
	// elapses; once it has, the sync re-arms it (record ID recorded in rearm).
	// Ignored records (user-cancelled) always block: cancelling is explicit.
	already := map[string]bool{}
	rearm := map[string]string{} // dedup key → exhausted-failed record ID
	if existing, err := s.downloadSvc.ListByPlaylist(ctx, playlistIDStr); err == nil {
		now := time.Now().UTC()
		for _, d := range existing {
			exhausted := (d.State == download.StateFailed || d.State == download.StateFailedPending) && d.RetryCount >= download.MaxRetries
			if exhausted && now.Sub(d.UpdatedAt) >= requeueCooldown {
				if d.ISRC != "" {
					rearm["isrc:"+d.ISRC] = d.ID
				}
				if d.Artist != "" && d.Title != "" {
					rearm["at:"+strings.ToLower(d.Artist)+"|"+strings.ToLower(d.Title)] = d.ID
				}
				continue
			}
			if d.ISRC != "" {
				already["isrc:"+d.ISRC] = true
			}
			if d.Artist != "" && d.Title != "" {
				already["at:"+strings.ToLower(d.Artist)+"|"+strings.ToLower(d.Title)] = true
			}
		}
	} else {
		s.log.Warn("download missing: list existing failed, proceeding without dedup",
			"error", err, "component", "playlist")
	}

	queued := 0

	for _, pt := range tracks {
		if pt.TrackID != nil {
			continue
		}

		// Skip tracks already added to the download pipeline in an earlier sync.
		if pt.ISRC != "" && already["isrc:"+pt.ISRC] {
			s.log.Debug("playlist track already queued via ISRC, skipping",
				"artist", pt.Artist, "title", pt.Title, "isrc", pt.ISRC, "component", "playlist")
			continue
		}
		// The artist|title fallback only applies to tracks that carry no ISRC.
		// An ISRC-bearing track dedups by its own ISRC: a same-titled sibling
		// with a different ISRC is a distinct release and must still be queued.
		if pt.Artist != "" && pt.Title != "" && pt.ISRC == "" && already["at:"+strings.ToLower(pt.Artist)+"|"+strings.ToLower(pt.Title)] {
			s.log.Debug("playlist track already queued, skipping",
				"artist", pt.Artist, "title", pt.Title, "component", "playlist")
			continue
		}

		// ISRC dedup: skip if a library track with the same ISRC already exists.
		if pt.ISRC != "" {
			if existing, _ := s.store.GetTrackByISRC(ctx, pt.ISRC); existing != nil {
				s.log.Info("playlist track already imported via ISRC, skipping",
					"artist", pt.Artist, "title", pt.Title, "isrc", pt.ISRC, "component", "playlist")
				continue
			}
		}

		// An exhausted-failed record past the cooldown window is re-armed in
		// place — the monitor picks it up again with a fresh retry budget.
		// The artist|title re-arm only applies to ISRC-less tracks, mirroring
		// the dedup above: an ISRC-bearing track re-arms its own ISRC record.
		if pt.ISRC == "" {
			if id := rearm["at:"+strings.ToLower(pt.Artist)+"|"+strings.ToLower(pt.Title)]; id != "" {
				if err := s.downloadSvc.Retry(ctx, id); err != nil {
					s.log.Warn("re-arm exhausted download failed",
						"download_id", id, "artist", pt.Artist, "title", pt.Title, "error", err, "component", "playlist")
					continue
				}
				s.log.Info("playlist re-armed exhausted download",
					"download_id", id, "artist", pt.Artist, "title", pt.Title, "component", "playlist")
				queued++
				continue
			}
		}
		if id := rearm["isrc:"+pt.ISRC]; id != "" && pt.ISRC != "" {
			if err := s.downloadSvc.Retry(ctx, id); err != nil {
				s.log.Warn("re-arm exhausted download failed",
					"download_id", id, "artist", pt.Artist, "title", pt.Title, "error", err, "component", "playlist")
				continue
			}
			s.log.Info("playlist re-armed exhausted download",
				"download_id", id, "artist", pt.Artist, "title", pt.Title, "component", "playlist")
			queued++
			continue
		}

		_, dlErr := s.downloadSvc.QueuePending(ctx, download.Meta{
			Artist:      pt.Artist,
			Album:       pt.Album,
			Title:       pt.Title,
			TrackNumber: pt.Position,
			ISRC:        pt.ISRC,
			PlaylistID:  playlistIDStr,
		})
		if dlErr != nil {
			s.log.Error("queue pending failed", "artist", pt.Artist, "title", pt.Title, "error", dlErr, "component", "playlist")
			continue
		}
		queued++
	}

	s.log.Info("download missing: queued", "count", queued, "component", "playlist")

	// Rebuild the playlist folder once the queued downloads complete.
	// syncPlaylistGuarded → SyncPlaylist waits for downloads to reach a
	// terminal state, re-links newly imported tracks, then rebuilds the
	// playlist folder. Without this, tracks downloaded for unmatched
	// playlist entries never appear in the playlist folder.
	if queued > 0 {
		go s.syncPlaylistGuarded(playlistID)
	}

	return queued, nil
}

// syncPlaylistGuarded runs a background sync with a 15-minute timeout. Used
// by the auto-sync worker and the download-missing rebuild, which have no
// caller-owned context.
func (s *Service) syncPlaylistGuarded(playlistID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := s.SyncPlaylistGuarded(ctx, playlistID); err != nil {
		s.log.Error("background sync failed", "playlist_id", playlistID, "error", err, "component", "playlist")
	}
}

// SyncPlaylistGuarded runs SyncPlaylist under a per-playlist mutex to prevent
// concurrent syncs of the same playlist (e.g., from double-click, overlapping
// auto-sync, or a job-triggered sync racing a background rebuild). Returns
// started=false when a sync for this playlist is already in progress.
func (s *Service) SyncPlaylistGuarded(ctx context.Context, playlistID int64) (bool, error) {
	s.syncMu.Lock()
	if s.syncing[playlistID] {
		s.syncMu.Unlock()
		s.log.Warn("sync already in progress, skipping", "playlist_id", playlistID, "component", "playlist")
		return false, nil
	}
	s.syncing[playlistID] = true
	s.syncMu.Unlock()

	defer func() {
		s.syncMu.Lock()
		delete(s.syncing, playlistID)
		s.syncMu.Unlock()
	}()

	if err := s.SyncPlaylist(ctx, playlistID); err != nil {
		return true, err
	}
	return true, nil
}

// findAndQueueDownload searches across configured sources for a matching track
// and queues the best candidate via the download service.
// Metadata (artist, album, title) comes from the playlist track — authoritative,
// never from the search result's filename.
func (s *Service) findAndQueueDownload(ctx context.Context, title, artist, album string, durationMs int64, excludeSource string, playlistID int64, isrc string) (downloadID, sourceName string, confidence float64, err error) {
	orch := download.NewOrchestrator(s.downloadReg, s.log)
	var defaultProfile *quality.QualityProfile
	if s.qualityProfileStore != nil {
		p, err := s.qualityProfileStore.LoadProfileByID(ctx, nil)
		if err != nil {
			s.log.Warn("failed to load default quality profile, download proceeds unfiltered", "error", err, "component", "playlist")
		}
		defaultProfile = p
	}

	best, err := orch.FindBestMatch(ctx, title, artist, album, durationMs, excludeSource, defaultProfile)
	if err != nil {
		return "", "", 0, err
	}

	username := best.Track.Username

	// Build DownloadMeta with authoritative playlist metadata.
	meta := download.Meta{
		Artist:      artist,
		Album:       album,
		Title:       title,
		TrackNumber: best.Track.TrackNumber,
		ISRC:        isrc,
		PlaylistID:  strconv.FormatInt(playlistID, 10),
		Bitrate:     best.Track.Bitrate,
		Format:      best.Track.Quality,
	}

	// Enrich with album name + cover art from metadata providers (MusicBrainz/CoverArtArchive).
	// Always call EnrichMetadata — it resolves album from artist+title when album is empty,
	// then searches for cover art. Only requires artist+title to function.
	if s.metadataResolver != nil && meta.Artist != "" && meta.Title != "" {
		if enriched, enrichErr := s.metadataResolver.EnrichMetadata(ctx, meta.Artist, meta.Title, meta.Album, meta.Year); enrichErr == nil {
			if meta.Album == "" && enriched.Album != "" {
				meta.Album = enriched.Album
			}
			if enriched.CoverURL != "" {
				meta.CoverURL = enriched.CoverURL
			}
		} else {
			s.log.Warn("metadata enrichment failed, queueing with partial metadata",
				"artist", meta.Artist, "title", meta.Title, "error", enrichErr, "component", "playlist")
		}
	}

	id, dlErr := s.downloadSvc.Queue(ctx, best.SourceName, username, best.Track.Filename, best.Track.Size, meta)
	if dlErr != nil {
		return "", "", best.Score, fmt.Errorf("queue download: %w", dlErr)
	}

	return id, best.SourceName, best.Score, nil
}

// ─── Sync (re-link) ───────────────────────────────────────────────────

// SyncPlaylist re-links playlist tracks to library tracks (downloaded since the
// last sync) and queues downloads for anything still unmatched. It never scans
// the filesystem — the library is the single source of truth.
func (s *Service) SyncPlaylist(ctx context.Context, playlistID int64) error {
	p, err := s.store.GetPlaylist(ctx, playlistID)
	if err != nil || p == nil {
		return errors.New("playlist not found")
	}

	// Wait for any in-progress downloads to complete before scanning.
	if err := s.waitForDownloads(ctx); err != nil {
		s.log.Error("download wait failed", "error", err, "component", "playlist")
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
					s.log.Info("track moved", "title", info.Title, "old_pos", old, "new_pos", newPos, "component", "playlist")
				}
			}

			s.store.DeletePlaylistTracks(ctx, playlistID)
			for i, info := range trackInfos {
				pt := domain.PlaylistTrack{
					PlaylistID: playlistID, Position: i + 1,
					SourceTrackID: info.SourceTrackID,
					Title:         info.Title, Artist: info.Artist,
					Album: info.Album, DurationMs: info.DurationMs,
					ISRC: info.ISRC,
				}
				if trackID := s.findInLibrary(ctx, info); trackID != 0 {
					pt.TrackID = &trackID
				}
				s.store.UpsertPlaylistTrack(ctx, &pt)
			}
			p.TrackCount = len(trackInfos)
		}
	}

	// Re-link: resolve which tracks now exist in the library (downloaded by
	// the pipeline since the last sync). No filesystem scan is performed — the
	// library is the single source of truth and downloads are imported by the
	// download pipeline, never by scanning the download staging directory.
	tracks, _ := s.store.GetPlaylistTracks(ctx, playlistID)
	stillUnmatched := 0
	for i := range tracks {
		if tracks[i].TrackID != nil {
			continue
		}
		info := TrackInfo{
			SourceTrackID: tracks[i].SourceTrackID,
			Title:         tracks[i].Title, Artist: tracks[i].Artist,
			DurationMs: tracks[i].DurationMs,
			ISRC:       tracks[i].ISRC,
		}
		if trackID := s.findInLibrary(ctx, info); trackID != 0 {
			tracks[i].TrackID = &trackID
			s.store.UpsertPlaylistTrack(ctx, &tracks[i])
		} else {
			stillUnmatched++
		}
	}

	p.SyncedAt = time.Now().UTC().Format(time.RFC3339)
	s.store.UpsertPlaylist(ctx, p)

	s.log.Info("synced", "name", p.Name, "tracks", p.TrackCount, "unmatched", stillUnmatched, "component", "playlist")

	// Queue downloads for tracks that are still unmatched. Idempotent — tracks
	// already in the download pipeline are skipped, so the periodic sync simply
	// re-checks until every song is satisfied.
	if stillUnmatched > 0 {
		if _, err := s.DownloadMissing(ctx, playlistID); err != nil {
			s.log.Error("download missing failed during sync", "playlist_id", playlistID, "error", err, "component", "playlist")
		}
	}

	// Build playlist folder.
	// Uses background context — the caller may cancel ctx after SyncPlaylist returns.
	s.log.Info("building playlist folder", "playlist_id", playlistID, "component", "playlist")
	go s.buildPlaylistFolder(context.Background(), playlistID)
	return nil
}

// shortSourceID returns the first 8 characters of a source playlist ID,
// enough to disambiguate same-name playlists in folder names.
func shortSourceID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// resolvePlaylistDisplay derives the display-time fields NameConflict and
// FolderName. Same-name playlists from the same source are flagged as a
// conflict and get an ID-suffixed folder so their files never share a
// directory. Callers: the folder builder (authoritative for on-disk naming)
// and the API list handler (so the UI reports the same folder).
func (s *Service) resolvePlaylistDisplay(ctx context.Context, p *domain.Playlist) {
	if p == nil {
		return
	}
	n, err := s.store.CountPlaylistsByName(ctx, p.Source, p.Name)
	p.NameConflict = err == nil && n > 1
	p.FolderName = sanitize.DirName(p.Name)
	if p.NameConflict {
		p.FolderName = fmt.Sprintf("%s (%s)", p.FolderName, shortSourceID(p.SourcePlaylistID))
	}
}

// ResolvePlaylistDisplay exposes resolvePlaylistDisplay to the API layer.
func (s *Service) ResolvePlaylistDisplay(ctx context.Context, p *domain.Playlist) {
	s.resolvePlaylistDisplay(ctx, p)
}

// buildPlaylistFolder creates the playlist directory structure from linked tracks.
func (s *Service) buildPlaylistFolder(ctx context.Context, playlistID int64) {
	defer s.log.Info("playlist folder build done", "playlist_id", playlistID, "component", "playlist")
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
	s.log.Info("build folder", "name", playlist.Name, "path", root, "linked", linkedCount, "total", len(tracks), "component", "playlist")

	if root == "" {
		s.log.Warn("build folder skipped: no playlist_path", "name", playlist.Name, "component", "playlist")
		return
	}
	template := cfg.Library.PlaylistTemplate
	if template == "" {
		template = "{position:02d} {artist} - {title}"
	}

	// Create playlist directory. The folder name is resolved conflict-aware:
	// same-name playlists from the same source get an ID suffix so their
	// files never share a directory. The mutex is held from the conflict
	// check through MkdirAll so two concurrent folder builds can't both
	// pick the unsuffixed name.
	s.folderMu.Lock()
	s.resolvePlaylistDisplay(ctx, playlist)
	playlistDir := filepath.Join(root, playlist.FolderName)
	err = os.MkdirAll(playlistDir, 0o755)
	s.folderMu.Unlock()
	if err != nil {
		s.log.Error("mkdir failed", "path", playlistDir, "error", err, "component", "playlist")
		return
	}
	if playlist.NameConflict {
		// Same-name playlists from the same source previously shared the plain
		// folder. The plain directory may still exist with stale copies; there
		// is no safe way to attribute old files to one playlist, so cleanup is
		// manual.
		s.log.Warn("playlist folder name conflict: using suffixed folder", "name", playlist.Name, "folder", playlist.FolderName, "legacy_folder", sanitize.DirName(playlist.Name), "playlist_id", playlist.ID, "component", "playlist")
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
			s.log.Error("mkdir failed", "path", filepath.Dir(destPath), "error", err, "component", "playlist")
			continue
		}

		// Copy file to playlist folder (library copy stays intact).
		if err := copyFile(track.FilePath, destPath); err != nil {
			s.log.Error("copy failed", "src", track.FilePath, "dst", destPath, "error", err, "component", "playlist")
			continue
		}
		written++
	}

	s.log.Info("folder built", "name", playlist.Name, "tracks", written, "component", "playlist")

	// Clean up orphaned files from old positions (mirror mode only).
	// Empty/unset SyncMode defaults to mirror for backward compatibility.
	if playlist.SyncMode == "" || playlist.SyncMode == domain.SyncModeMirror {
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
					s.log.Info("removed orphaned file", "file", e.Name(), "component", "playlist")
				}
			}
		}
	} else {
		s.log.Info("skipping orphan cleanup (sync_mode=append)", "name", playlist.Name, "component", "playlist")
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
func (s *Service) upsertPlaylist(ctx context.Context, source, sourceID, sourceName string, candidates []PlaylistInfo, trackCount int, syncMode string) (*domain.Playlist, error) {
	existing, _ := s.store.GetPlaylistBySourceID(ctx, source, sourceID)
	if existing != nil {
		existing.TrackCount = trackCount
		// Only update SyncMode if caller explicitly set it — re-imports
		// that don't specify a mode preserve the existing choice.
		if syncMode != "" {
			existing.SyncMode = domain.SyncMode(syncMode)
		}
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
		SyncMode:         domain.SyncMode(syncMode),
	}
	id, err := s.store.UpsertPlaylist(ctx, p)
	if err != nil {
		return nil, err
	}
	// Re-read to get store defaults (SyncMode resolved to "mirror", UpdatedAt, etc.)
	created, err := s.store.GetPlaylist(ctx, id)
	if err != nil || created == nil {
		p.ID = id
		return p, nil
	}
	return created, nil
}

// findInLibrary searches the library for a matching track.
func (s *Service) findInLibrary(ctx context.Context, info TrackInfo) int64 {
	// ISRC is the most reliable identifier — try it first.
	if info.ISRC != "" {
		if t, err := s.store.GetTrackByISRC(ctx, info.ISRC); err == nil && t != nil {
			return t.ID
		}
	}

	// Title search with normalized query — strip common annotations that
	// differ between source metadata and library titles.
	query := info.Title
	if idx := strings.IndexAny(query, "(-["); idx > 0 {
		query = strings.TrimSpace(query[:idx])
	}
	tracks, err := s.store.SearchTracks(ctx, query, 20)
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

// ─── Retry worker ─────────────────────────────────────────────────────

// StartRetryWorker runs a periodic goroutine that scans for failedPending
// download records and retries search resolution. Runs until ctx is cancelled.
func (s *Service) StartRetryWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	s.log.Info("pending retry worker started", "interval", interval, "max_retries", download.MaxRetries, "component", "playlist")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("pending retry worker panicked", "panic", r, "component", "playlist")
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.log.Info("pending retry worker stopped", "component", "playlist")
				return
			case <-ticker.C:
				s.retryPendingDownloads(ctx)
			}
		}
	}()
}

// retryPendingDownloads lists failedPending records and re-attempts search
// resolution for those whose RetryAfter has passed and haven't hit MaxRetries.
func (s *Service) retryPendingDownloads(ctx context.Context) {
	failedPending, err := s.downloadSvc.ListByState(ctx, download.StateFailedPending)
	if err != nil {
		s.log.Error("pending retry worker: list failed", "error", err, "component", "playlist")
		return
	}
	if len(failedPending) == 0 {
		return
	}

	orch := download.NewOrchestrator(s.downloadReg, s.log)
	var defaultProfile *quality.QualityProfile
	if s.qualityProfileStore != nil {
		p, err := s.qualityProfileStore.LoadProfileByID(ctx, nil)
		if err != nil {
			s.log.Warn("pending retry worker: failed to load quality profile", "error", err, "component", "playlist")
		}
		defaultProfile = p
	}

	retried := 0
	for i := range failedPending {
		rec := &failedPending[i]

		if rec.RetryCount >= download.MaxRetries {
			// Belt and suspenders — transition to terminal failed so the
			// record doesn't stay stuck in failedPending forever.
			rec.State = download.StateFailed
			rec.Error = fmt.Sprintf("search resolution failed after %d retries", rec.RetryCount)
			_ = s.downloadSvc.UpdateDownload(ctx, rec)
			continue
		}
		if rec.RetryAfter != "" {
			retryAfter, parseErr := time.Parse(time.RFC3339, rec.RetryAfter)
			if parseErr == nil && time.Now().UTC().Before(retryAfter) {
				continue
			}
		}
		if rec.Artist == "" || rec.Title == "" {
			s.log.Warn("pending retry worker: missing artist/title, marking failed",
				"download_id", rec.ID, "component", "playlist")
			rec.State = download.StateFailed
			rec.Error = "missing artist or title metadata — cannot resolve download source"
			_ = s.downloadSvc.UpdateDownload(ctx, rec)
			continue
		}

		// ISRC dedup: skip if a library track with the same ISRC already exists.
		if rec.ISRC != "" {
			if existing, _ := s.store.GetTrackByISRC(ctx, rec.ISRC); existing != nil {
				s.log.Info("pending retry worker: track already imported via ISRC, skipping",
					"artist", rec.Artist, "title", rec.Title, "isrc", rec.ISRC, "component", "playlist")
				rec.State = download.StateIgnored
				_ = s.downloadSvc.UpdateDownload(ctx, rec)
				continue
			}
		}

		best, searchErr := orch.FindBestMatch(ctx, rec.Title, rec.Artist, rec.Album, 0, "", defaultProfile)
		if searchErr != nil {
			s.log.Warn("pending retry worker: search failed",
				"artist", rec.Artist, "title", rec.Title, "error", searchErr, "component", "playlist")

			rec.RetryCount++
			if rec.RetryCount > download.MaxRetries {
				rec.State = download.StateFailed
				rec.Error = fmt.Sprintf("search resolution failed after %d retries: %s", rec.RetryCount, searchErr.Error())
			} else {
				backoffMin := 1 << (rec.RetryCount - 1)
				if backoffMin > 30 {
					backoffMin = 30
				}
				rec.RetryAfter = time.Now().UTC().Add(time.Duration(backoffMin) * time.Minute).Format(time.RFC3339)
				rec.Error = searchErr.Error()
			}
			_ = s.downloadSvc.UpdateDownload(ctx, rec)
			continue
		}

		// Update record with resolved source info.
		username := best.Track.Username
		if username == "" {
			username = best.SourceName
		}
		rec.SourceName = best.SourceName
		rec.Username = username
		rec.Filename = best.Track.Filename
		rec.Size = best.Track.Size
		rec.Bitrate = best.Track.Bitrate
		rec.Format = best.Track.Quality
		rec.State = download.StateQueued
		rec.Error = ""
		rec.RetryAfter = ""
		rec.DisplayName = rec.Artist + " - " + rec.Title

		// Enrich metadata.
		if s.metadataResolver != nil && rec.Artist != "" && rec.Title != "" {
			if enriched, enrichErr := s.metadataResolver.EnrichMetadata(ctx, rec.Artist, rec.Title, rec.Album, rec.Year); enrichErr == nil {
				if rec.Album == "" && enriched.Album != "" {
					rec.Album = enriched.Album
				}
				if enriched.CoverURL != "" {
					rec.CoverURL = enriched.CoverURL
				}
			}
		}

		if err := s.downloadSvc.UpdateDownload(ctx, rec); err != nil {
			s.log.Error("pending retry worker: update failed", "download_id", rec.ID, "error", err, "component", "playlist")
			continue
		}

		retried++
	}
	if retried > 0 {
		s.log.Info("pending retry worker: retried", "count", retried, "component", "playlist")
	}
}

// ─── Auto-sync worker ──────────────────────────────────────────────────

// StartAutoSyncWorker runs a periodic goroutine that syncs all playlists
// with AutoSync=true against their upstream source. Runs until ctx is
// cancelled. Uses per-playlist mutex via syncPlaylistGuarded to prevent
// overlapping syncs.
func (s *Service) StartAutoSyncWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	s.log.Info("auto-sync worker started", "interval", interval, "component", "playlist")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("auto-sync worker panicked", "panic", r, "component", "playlist")
			}
		}()
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				s.log.Info("auto-sync worker stopped", "component", "playlist")
				return
			case <-timer.C:
				s.syncAutoPlaylists(ctx)
				timer.Reset(interval)
			}
		}
	}()
}

// syncAutoPlaylists lists all playlists with AutoSync=true and triggers a
// sync for each. Errors are logged individually — one failing playlist
// does not block others.
func (s *Service) syncAutoPlaylists(ctx context.Context) {
	playlists, err := s.store.ListPlaylists(ctx)
	if err != nil {
		s.log.Error("auto-sync: list playlists failed", "error", err, "component", "playlist")
		return
	}

	var candidates []domain.Playlist
	for _, p := range playlists {
		if p.AutoSync {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return
	}

	s.log.Info("auto-sync: checking playlists", "total", len(candidates), "component", "playlist")
	for _, p := range candidates {
		if p.Source == "" || p.SourcePlaylistID == "" {
			s.log.Warn("auto-sync: skipping playlist with no source",
				"playlist_id", p.ID, "name", p.Name, "component", "playlist")
			continue
		}
		// Fire each sync in its own goroutine so the ticker loop returns
		// immediately. A semaphore caps concurrent auto-syncs at 3 to avoid
		// overwhelming upstream APIs. syncPlaylistGuarded has its own
		// per-playlist mutex to prevent overlapping syncs of the same playlist.
		go func(pid int64) {
			defer func() {
				if r := recover(); r != nil {
					s.log.Error("auto-sync goroutine panicked", "playlist_id", pid, "panic", r, "component", "playlist")
				}
			}()
			select {
			case s.autoSyncSem <- struct{}{}:
				defer func() { <-s.autoSyncSem }()
			case <-ctx.Done():
				return
			}
			s.syncPlaylistGuarded(pid)
		}(p.ID)
	}
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
