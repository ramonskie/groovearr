package tracking

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
)

// ─── Monitor primitives ───────────────────────────────────────────────

// SetArtistMonitor updates the monitored flag and monitor mode of a tracked
// artist. mode must be one of all|future|none; anything else is rejected with
// a wrapped error before any store write. Mode none implies monitored=false:
// "none" means the artist is tracked without acquisition (the same invariant
// AddArtist enforces).
func (s *Service) SetArtistMonitor(ctx context.Context, artistID int64, monitored bool, mode domain.MonitorMode) error {
	if !validMonitorMode(mode) {
		return fmt.Errorf("tracking: invalid monitor mode %q (want all|future|none)", mode)
	}
	artist, err := s.store.GetTrackedArtist(ctx, artistID)
	if err != nil {
		return fmt.Errorf("get tracked artist %d: %w", artistID, err)
	}
	if artist == nil {
		return fmt.Errorf("tracking: tracked artist %d not found", artistID)
	}
	if mode == domain.MonitorModeNone {
		monitored = false
	}
	if err := s.store.UpdateArtistMonitor(ctx, artistID, monitored, mode); err != nil {
		return fmt.Errorf("update artist monitor %d: %w", artistID, err)
	}
	return s.recomputeArtistAlbums(ctx, artistID, monitored, mode)
}

// recomputeArtistAlbums re-derives the monitored flag for every album owned by
// the artist after an artist-level monitor change. Ignored albums are an
// explicit per-album skip and are left untouched regardless of the new mode;
// UpdateAlbumMonitor is only called for an album whose value actually changes.
func (s *Service) recomputeArtistAlbums(ctx context.Context, artistID int64, monitored bool, mode domain.MonitorMode) error {
	albums, err := s.store.ListTrackedAlbums(ctx, artistID)
	if err != nil {
		return fmt.Errorf("list tracked albums %d: %w", artistID, err)
	}
	nowYear := time.Now().UTC().Year()
	for _, album := range albums {
		if album.Status == domain.AlbumStatusIgnored {
			continue
		}
		want := desiredMonitored(monitored, mode, album.Year, nowYear)
		if album.Monitored == want {
			continue
		}
		if err := s.store.UpdateAlbumMonitor(ctx, album.ID, want); err != nil {
			return fmt.Errorf("update album monitor %d: %w", album.ID, err)
		}
	}
	return nil
}

// SetAlbumMonitored toggles the monitored flag of a single tracked album. An
// unknown album returns ErrAlbumNotFound so the handler can answer 404 rather
// than a 500.
func (s *Service) SetAlbumMonitored(ctx context.Context, albumID int64, monitored bool) error {
	if albumID == 0 {
		return errors.New("tracking: album ID is required")
	}
	album, err := s.store.GetTrackedAlbum(ctx, albumID)
	if err != nil {
		return fmt.Errorf("get tracked album %d: %w", albumID, err)
	}
	if album == nil {
		return fmt.Errorf("%w: album %d", ErrAlbumNotFound, albumID)
	}
	if err := s.store.UpdateAlbumMonitor(ctx, albumID, monitored); err != nil {
		return fmt.Errorf("update album monitor %d: %w", albumID, err)
	}
	return nil
}

// SetAlbumStatus sets a tracked album's acquisition status to wanted or
// ignored. It is the writer the PATCH album handler uses to let a user skip
// (ignored) or re-arm (wanted) a release. Any other status is rejected with
// ErrAlbumStatusInvalid before any write; an unknown album returns
// ErrAlbumNotFound so the handler can answer 404.
func (s *Service) SetAlbumStatus(ctx context.Context, albumID int64, status domain.AlbumStatus) error {
	switch status {
	case domain.AlbumStatusWanted, domain.AlbumStatusIgnored:
	default:
		return fmt.Errorf("%w: %q (want wanted|ignored)", ErrAlbumStatusInvalid, status)
	}
	album, err := s.store.GetTrackedAlbum(ctx, albumID)
	if err != nil {
		return fmt.Errorf("get tracked album %d: %w", albumID, err)
	}
	if album == nil {
		return fmt.Errorf("%w: %d", ErrAlbumNotFound, albumID)
	}
	if err := s.store.MarkAlbumStatus(ctx, albumID, status); err != nil {
		return fmt.Errorf("set album status %d: %w", albumID, err)
	}
	return nil
}

// DeleteTrackedArtist removes a tracked artist; the store cascades to its
// discovered albums (ON DELETE CASCADE).
func (s *Service) DeleteTrackedArtist(ctx context.Context, artistID int64) error {
	if artistID == 0 {
		return errors.New("tracking: artist ID is required")
	}
	if err := s.store.DeleteTrackedArtist(ctx, artistID); err != nil {
		return fmt.Errorf("delete tracked artist %d: %w", artistID, err)
	}
	return nil
}

// ─── Refresh ──────────────────────────────────────────────────────────

// RefreshArtist re-fetches an artist's discography through the discovery
// registry only (provider-agnostic; AGENTS §1), reconciles it, and records
// last_refreshed_at (via ReconcileAlbums → TouchArtistRefreshed). A
// cooling-down provider is skipped and metadata.ErrRateLimited parks it in the
// shared bucket through the same helper AddArtist uses. When
// tracking.auto_search_missing is enabled, SearchMissing runs for the artist
// after reconcile; the live config closure is read on every call (AGENTS §9).
func (s *Service) RefreshArtist(ctx context.Context, artistID int64) (*RefreshResult, error) {
	artist, err := s.store.GetTrackedArtist(ctx, artistID)
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %d: %w", artistID, err)
	}
	if artist == nil {
		return nil, fmt.Errorf("tracking: tracked artist %d not found", artistID)
	}
	if s.discoveryReg == nil {
		return nil, errors.New("tracking: discovery registry not configured")
	}
	provider := s.discoveryReg.Get(artist.ProviderName)
	if provider == nil {
		return nil, fmt.Errorf("tracking: discovery provider %q not registered", artist.ProviderName)
	}

	before, err := s.trackedProviderIDs(ctx, artistID)
	if err != nil {
		return nil, err
	}
	albums, err := s.fetchArtistAlbums(ctx, provider, artist.ProviderName, artist.ProviderArtistID)
	if err != nil {
		return nil, err
	}
	if err := s.ReconcileAlbums(ctx, artistID, albums); err != nil {
		return nil, err
	}
	result, err := s.refreshResult(ctx, artistID, before, len(albums))
	if err != nil {
		return nil, err
	}

	if s.autoSearchEnabled() {
		if _, err := s.SearchMissing(ctx, artistID); err != nil {
			s.log.Warn("refresh auto-search failed", "artist_id", artistID, "error", err, "component", "tracking")
		}
	}
	return result, nil
}

// trackedProviderIDs snapshots the provider album IDs already tracked, so a
// refresh can tell which releases are new.
func (s *Service) trackedProviderIDs(ctx context.Context, artistID int64) (map[string]bool, error) {
	albums, err := s.store.ListTrackedAlbums(ctx, artistID)
	if err != nil {
		return nil, fmt.Errorf("list tracked albums %d: %w", artistID, err)
	}
	ids := make(map[string]bool, len(albums))
	for _, a := range albums {
		ids[a.ProviderAlbumID] = true
	}
	return ids, nil
}

// refreshResult computes the post-reconcile summary from the persisted rows.
func (s *Service) refreshResult(ctx context.Context, artistID int64, before map[string]bool, seen int) (*RefreshResult, error) {
	albums, err := s.store.ListTrackedAlbums(ctx, artistID)
	if err != nil {
		return nil, fmt.Errorf("list tracked albums %d: %w", artistID, err)
	}
	result := &RefreshResult{AlbumsSeen: seen}
	for _, a := range albums {
		wanted := a.Monitored && a.Status == domain.AlbumStatusWanted
		if wanted {
			result.Missing++
			if !before[a.ProviderAlbumID] {
				result.NewlyWanted++
			}
		}
	}
	return result, nil
}

// autoSearchEnabled resolves the live tracking.auto_search_missing setting.
func (s *Service) autoSearchEnabled() bool {
	cfg := s.cfg()
	return cfg.Tracking.AutoSearchMissing != nil && *cfg.Tracking.AutoSearchMissing
}

// ─── Search missing ───────────────────────────────────────────────────

// searchBatchSize caps how many wanted albums one SearchMissing run processes.
// A discography larger than this is completed over later runs: a processed
// album is marked downloading (or skipped/re-armed), so ListWanted stops
// returning it and the next run picks up the remainder. <= 0 means unlimited.
// It is a var so tests can shorten the batch.
var searchBatchSize = 5

// SearchMissing queues downloads for an artist's monitored WANTED albums.
// Album acquisition is delegated to the single canonical policy in
// internal/download (QueueAlbumWithFallback); tracking resolves each album's
// tracks best-effort and forwards them, and never re-implements the
// album-first / per-track decision.
//
// Each run processes at most searchBatchSize albums, never-searched first then
// oldest (last_searched_at NULLS FIRST, then Year, then ID), so a large
// discography does not flood the queue in one pass and repeated runs rotate
// forward even when an album produces no work. The count left for later runs is
// reported as SearchResult.Remaining.
//
// Idempotency: the canonical policy dedups internally; SearchMissing
// additionally consults ActiveDownloadFinder so an album already in the
// pipeline is skipped before any provider search. An exhausted-failed record
// (retries spent) is re-armed in place via Retry once it has sat failed for
// requeueCooldown, mirroring playlist.Service.DownloadMissing — no duplicate
// record is created. On a successful queue the album is marked downloading.
//
// Stalled albums: an album whose download record is an exhausted failure but
// whose tracked status still reads `downloading` is first reset to `wanted`
// (requeueStalled) because ListWanted excludes downloading rows. Without that
// reset the re-arm branch below could never be reached. downloading → wanted
// is the ONLY allowed status regression and is failure-driven.
func (s *Service) SearchMissing(ctx context.Context, artistID int64) (*SearchResult, error) {
	artist, err := s.store.GetTrackedArtist(ctx, artistID)
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %d: %w", artistID, err)
	}
	if artist == nil {
		return nil, fmt.Errorf("tracking: tracked artist %d not found", artistID)
	}
	if s.queuer == nil {
		return nil, errors.New("tracking: download queuer not configured")
	}

	idx := s.downloadIndex(ctx)
	if err := s.requeueStalled(ctx, artist, idx); err != nil {
		return nil, err
	}
	wanted, err := s.ListWanted(ctx, artistID)
	if err != nil {
		return nil, err
	}
	toProcess, remaining := searchBatch(sortWanted(wanted))
	result := &SearchResult{Remaining: remaining}
	for _, album := range toProcess {
		s.processWanted(ctx, artist, album, idx, result)
	}
	s.log.Info("search missing complete", "artist_id", artistID, "queued", result.Queued, "skipped", result.Skipped, "errors", result.Errors, "remaining", result.Remaining, "component", "tracking")
	return result, nil
}

// requeueStalled resets albums stuck at `downloading` back to `wanted` when the
// download index classifies their record as a re-armable exhausted failure
// (idx.rearm). ListWanted excludes downloading rows, so without this step a
// failed download leaves the album permanently stranded and the re-arm branch
// in processWanted unreachable. downloading → wanted is the only allowed
// lifecycle regression, and only for a failure whose retry budget is spent.
func (s *Service) requeueStalled(ctx context.Context, artist *domain.TrackedArtist, idx downloadIndex) error {
	albums, err := s.store.ListTrackedAlbums(ctx, artist.ID)
	if err != nil {
		return fmt.Errorf("list tracked albums %d: %w", artist.ID, err)
	}
	for _, album := range albums {
		if album.Status != domain.AlbumStatusDownloading {
			continue
		}
		if idx.rearm[downloadKey(artist.Name, album.Title)] == "" {
			continue
		}
		if err := s.store.MarkAlbumStatus(ctx, album.ID, domain.AlbumStatusWanted); err != nil {
			return fmt.Errorf("requeue stalled album %d: %w", album.ID, err)
		}
		s.log.Info("requeued stalled downloading album", "album_id", album.ID, "album", album.Title, "component", "tracking")
	}
	return nil
}

// sortWanted orders the batch so never-searched albums come first
// (last_searched_at NULLS FIRST), then oldest by Year, then by ID. The slice is
// owned by the caller (ListWanted returns a fresh copy), so it is sorted in
// place; a processed album is stamped with last_searched_at, so a capped run
// rotates forward instead of re-picking the same stalled albums forever.
func sortWanted(albums []domain.TrackedAlbum) []domain.TrackedAlbum {
	sort.Slice(albums, func(i, j int) bool {
		a, b := albums[i], albums[j]
		if (a.LastSearchedAt == nil) != (b.LastSearchedAt == nil) {
			return a.LastSearchedAt == nil
		}
		if a.LastSearchedAt != nil && !a.LastSearchedAt.Equal(*b.LastSearchedAt) {
			return a.LastSearchedAt.Before(*b.LastSearchedAt)
		}
		if a.Year != b.Year {
			return a.Year < b.Year
		}
		return a.ID < b.ID
	})
	return albums
}

// searchBatch limits one run to searchBatchSize albums and reports how many
// wanted albums were left for later runs. A non-positive searchBatchSize means
// unlimited. The slice is expected to be already sorted by sortWanted.
func searchBatch(wanted []domain.TrackedAlbum) ([]domain.TrackedAlbum, int) {
	if searchBatchSize <= 0 || len(wanted) <= searchBatchSize {
		return wanted, 0
	}
	return wanted[:searchBatchSize], len(wanted) - searchBatchSize
}

// processWanted queues, skips, or re-arms one wanted album and folds the outcome
// into result. Every processed album is stamped with last_searched_at (all
// branches, via defer) so a capped run rotates past stalled albums.
//
// Outcomes: a re-armable exhausted record is retried, marked downloading, and
// counted as Queued; an album already in the pipeline is skipped (Skipped); a
// canonical-policy success increments Queued (and marks downloading when it
// queued anything) and its per-track errors increment Errors; a
// canonical-policy no-op (Queued==0 and no Errors)
// counts as Skipped instead of vanishing, so "processed but nothing to do" is
// visible in the result.
func (s *Service) processWanted(ctx context.Context, artist *domain.TrackedArtist, album domain.TrackedAlbum, idx downloadIndex, result *SearchResult) {
	defer s.markAlbumSearched(ctx, album.ID)
	key := downloadKey(artist.Name, album.Title)
	if id := idx.rearm[key]; id != "" {
		if err := s.activeFinder.Retry(ctx, id); err != nil {
			s.log.Warn("re-arm exhausted album failed", "download_id", id, "album", album.Title, "error", err, "component", "tracking")
			result.Errors++
			return
		}
		// The record is in flight again, so downloading is the correct status;
		// leaving it wanted would re-select it on the next run.
		if err := s.store.MarkAlbumStatus(ctx, album.ID, domain.AlbumStatusDownloading); err != nil {
			s.log.Warn("mark album downloading after re-arm failed", "album_id", album.ID, "error", err, "component", "tracking")
		}
		result.Queued++
		return
	}
	if idx.active[key] {
		result.Skipped++
		return
	}
	res, qerr := s.queueWantedAlbum(ctx, artist, album)
	if qerr != nil {
		s.log.Warn("queue album failed", "artist", artist.Name, "album", album.Title, "error", qerr, "component", "tracking")
		result.Errors++
		return
	}
	result.Queued += res.Queued
	result.Errors += len(res.Errors)
	if res.Queued == 0 && len(res.Errors) == 0 {
		result.Skipped++
		return
	}
	if res.Queued > 0 {
		if err := s.store.MarkAlbumStatus(ctx, album.ID, domain.AlbumStatusDownloading); err != nil {
			s.log.Warn("mark album downloading failed", "album_id", album.ID, "error", err, "component", "tracking")
		}
	}
}

// markAlbumSearched stamps an album as processed, logging a store failure
// without failing the run: the batch rotation is best-effort and must never
// turn a successful queue into an error.
func (s *Service) markAlbumSearched(ctx context.Context, albumID int64) {
	if err := s.store.MarkAlbumSearched(ctx, albumID); err != nil {
		s.log.Warn("mark album searched failed", "album_id", albumID, "error", err, "component", "tracking")
	}
}

// queueWantedAlbum resolves the album's track list best-effort and delegates
// the album-first / per-track policy to internal/download. A track-resolution
// error or empty list must not block the album-first attempt, so it is logged
// and an empty slice is forwarded.
func (s *Service) queueWantedAlbum(ctx context.Context, artist *domain.TrackedArtist, album domain.TrackedAlbum) (download.AlbumQueueResult, error) {
	tracks, err := s.albumTracks(ctx, album)
	if err != nil {
		s.log.Warn("album track lookup failed, attempting album-first without tracks", "artist", artist.Name, "album", album.Title, "error", err, "component", "tracking")
		tracks = nil
	}
	return s.queuer.QueueAlbumWithFallback(ctx, artist.Name, album.Title, trackQueues(tracks), s.cfg().DownloadClient, s.cfg().AlbumSources)
}

// trackQueues converts resolved discovery tracks into the canonical queue shape
// consumed by the per-track fallback in internal/download.
func trackQueues(tracks []discovery.TrackInfo) []download.TrackQueue {
	out := make([]download.TrackQueue, 0, len(tracks))
	for _, tr := range tracks {
		out = append(out, download.TrackQueue{
			Artist:      tr.ArtistName,
			Album:       tr.AlbumTitle,
			Title:       tr.Title,
			TrackNumber: tr.TrackNumber,
			DiscNumber:  tr.DiscNumber,
			ISRC:        tr.ISRC,
		})
	}
	return out
}

// albumTracks resolves an album's track list from the discovery provider that
// produced the tracked album, using the stored provider pair. Returns nil,nil
// when the provider is unknown, unconfigured, or cooling down.
func (s *Service) albumTracks(ctx context.Context, album domain.TrackedAlbum) ([]discovery.TrackInfo, error) {
	if s.discoveryReg == nil || album.ProviderName == "" || album.ProviderAlbumID == "" {
		return nil, nil
	}
	provider := s.discoveryReg.Get(album.ProviderName)
	if provider == nil || s.coolingDown(album.ProviderName) {
		return nil, nil
	}
	tracks, err := provider.GetAlbumTracks(ctx, album.ProviderAlbumID)
	if err != nil {
		s.noteRateLimit(album.ProviderName, err)
		return nil, fmt.Errorf("get album tracks %s/%s: %w", album.ProviderName, album.ProviderAlbumID, err)
	}
	return tracks, nil
}

// downloadIndex classifies existing download records into "already in the
// pipeline" (skip) and "exhausted, past cooldown" (re-arm), keyed by
// artist|title. Album-level records (QueueAlbum) carry the album title, so they
// collapse under downloadKey(artist, album). Per-track fallback records
// (QueuePending) carry the track title instead, so they do NOT match that key:
// the album-level check only catches album-level records. No literal duplicates
// accumulate — a queued album is marked downloading after the first pass, and
// QueuePending itself dedups identical per-track records, so the residual cost
// is bounded re-search churn, not duplication.
type downloadIndex struct {
	active map[string]bool
	rearm  map[string]string
}

// downloadIndex builds the dedup/re-arm index from the download records. It
// degrades to an empty index when ActiveDownloadFinder is nil or its List
// fails, so SearchMissing still works (relying on QueueAlbum/QueuePending's
// built-in dedup).
func (s *Service) downloadIndex(ctx context.Context) downloadIndex {
	idx := downloadIndex{active: map[string]bool{}, rearm: map[string]string{}}
	if s.activeFinder == nil {
		return idx
	}
	records, err := s.activeFinder.List(ctx)
	if err != nil {
		s.log.Warn("search missing: list downloads failed, proceeding without dedup", "error", err, "component", "tracking")
		return idx
	}
	now := time.Now().UTC()
	for _, d := range records {
		if d.Artist == "" || d.Title == "" {
			continue
		}
		key := downloadKey(d.Artist, d.Title)
		exhausted := (d.State == download.StateFailed || d.State == download.StateFailedPending) && d.RetryCount >= download.MaxRetries
		if exhausted && now.Sub(d.UpdatedAt) >= requeueCooldown {
			idx.rearm[key] = d.ID
			continue
		}
		idx.active[key] = true
	}
	return idx
}

// downloadKey is the artist|title dedup key, case- and space-normalized so a
// minor provider/library casing difference does not create a duplicate.
func downloadKey(artist, title string) string {
	return strings.ToLower(strings.TrimSpace(artist)) + "|" + strings.ToLower(strings.TrimSpace(title))
}

// validMonitorMode reports whether mode is one of the three supported modes.
func validMonitorMode(mode domain.MonitorMode) bool {
	switch mode {
	case domain.MonitorModeAll, domain.MonitorModeFuture, domain.MonitorModeNone:
		return true
	default:
		return false
	}
}
