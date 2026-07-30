package download

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/quality"
)

// retryOriginalSnap captures original source fields before they are cleared
// for the UI reset. Used to restore the original source on search failure.
type retryOriginalSnap struct {
	sourcename string
	filename   string
	size       int64
	bitrate    int
	format     string
	username   string
}

// Service provides a thin API for download queueing, status tracking,
// cancellation, and manual retry. The MonitoringService scans the DB and
// drives the download state machine automatically.
type Service struct {
	log                 *slog.Logger
	store               Store
	bus                 events.IEventAggregator
	registry            *Registry // needed for retry source resolution
	downloadClients     *DownloadClientRegistry
	qualityProfileStore quality.ProfileStore
	mu                  sync.Mutex
}

// NewService creates a Service backed by the given store,
// event bus, and logger.
func NewService(store Store, bus events.IEventAggregator, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		log:   logger,
		store: store,
		bus:   bus,
	}
}

// SetRegistry sets the plugin registry for retry source resolution.
func (s *Service) SetRegistry(registry *Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registry = registry
}

// SetDownloadClientRegistry sets the download client registry for album
// download cancellation.
func (s *Service) SetDownloadClientRegistry(reg *DownloadClientRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloadClients = reg
}

// SetQualityProfileStore sets the quality profile store for retry search resolution.
func (s *Service) SetQualityProfileStore(store quality.ProfileStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qualityProfileStore = store
}

// Queue creates a new download record in "queued" state, persists it via the
// store, and fires a TopicDownloadQueued event. The MonitoringService picks
// up queued records from the DB and drives the download lifecycle.
// Skips if an active download already exists for the same artist+title.
// Returns the generated download ID.
func (s *Service) Queue(ctx context.Context, sourceName, username, filename string, fileSize int64, meta Meta) (string, error) {
	// Serialize dedup check + insert to prevent TOCTOU race.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Dedup: skip if an active download already exists for the same artist+title.
	if meta.Artist != "" && meta.Title != "" {
		if existing, err := s.store.FindActiveByTitle(ctx, meta.Artist, meta.Title); err != nil {
			s.log.Warn("dedup check failed, proceeding", "artist", meta.Artist, "title", meta.Title, "error", err, "component", "download")
		} else if existing != nil {
			return existing.ID, nil
		}
	}

	id := fmt.Sprintf("%s-%d-%04x", sourceName, time.Now().UnixNano(), rand.Intn(0xffff))

	displayName := filename
	if meta.Artist != "" && meta.Title != "" {
		displayName = meta.Artist + " - " + meta.Title
	} else if meta.Title != "" {
		displayName = meta.Title
	}

	record := &Record{
		ID:          id,
		SourceName:  sourceName,
		Username:    username,
		Filename:    filename,
		DisplayName: displayName,
		State:       StateQueued,
		Size:        fileSize,
		TrackID:     meta.TrackID,
		ISRC:        meta.ISRC,
		CoverURL:    meta.CoverURL,
		PlaylistID:  meta.PlaylistID,
		Artist:      meta.Artist,
		Album:       meta.Album,
		Title:       meta.Title,
		TrackNumber: meta.TrackNumber,
		DiscNumber:  meta.DiscNumber,
		Year:        meta.Year,
		Bitrate:     meta.Bitrate,
		Format:      meta.Format,
	}

	if err := s.store.Insert(ctx, record); err != nil {
		return "", fmt.Errorf("queue download: %w", err)
	}

	s.bus.Publish(ctx, events.TopicDownloadQueued, record)

	return id, nil
}

// QueuePending inserts a download record with metadata only (no resolved
// source), fires the download:queued event, and does NOT dispatch. The record
// is resolved later (search + update) by the caller or MonitoringService.
//
// This enables batch-queuing all items first (visible in UI), then resolving
// them in the background.
func (s *Service) QueuePending(ctx context.Context, meta Meta) (string, error) {
	// Serialize dedup check + insert to prevent TOCTOU race.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Dedup: skip if an active download already exists for the same artist+title.
	if meta.Artist != "" && meta.Title != "" {
		if existing, err := s.store.FindActiveByTitle(ctx, meta.Artist, meta.Title); err != nil {
			s.log.Warn("dedup check failed, proceeding", "artist", meta.Artist, "title", meta.Title, "error", err, "component", "download")
		} else if existing != nil {
			return existing.ID, nil
		}
	}

	id := fmt.Sprintf("pending-%d-%04x", time.Now().UnixNano(), rand.Intn(0xffff))

	displayName := meta.Artist + " - " + meta.Title

	record := &Record{
		ID:          id,
		SourceName:  PendingSource,
		DisplayName: displayName,
		State:       StateQueued,
		TrackID:     meta.TrackID,
		ISRC:        meta.ISRC,
		CoverURL:    meta.CoverURL,
		PlaylistID:  meta.PlaylistID,
		Artist:      meta.Artist,
		Album:       meta.Album,
		Title:       meta.Title,
		TrackNumber: meta.TrackNumber,
		DiscNumber:  meta.DiscNumber,
		Year:        meta.Year,
		Bitrate:     meta.Bitrate,
		Format:      meta.Format,
	}

	if err := s.store.Insert(ctx, record); err != nil {
		return "", fmt.Errorf("queue pending: %w", err)
	}

	s.bus.Publish(ctx, events.TopicDownloadQueued, record)
	return id, nil
}

// QueueAlbum creates a single download record for a full album release.
// Unlike Queue (per-track), this produces ONE record that imports N tracks
// from a downloaded folder. Used by album-capable sources (prowlarr/torrent).
func (s *Service) QueueAlbum(ctx context.Context, release domain.AlbumRelease, tracks []domain.ExpectedTrack, downloadClient string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Dedup: skip if an active album download already exists for the same artist+album.
	if release.Artist != "" && release.Album != "" {
		if existing, err := s.store.FindActiveByTitle(ctx, release.Artist, release.Album); err != nil {
			s.log.Warn("dedup check failed for album, proceeding",
				"artist", release.Artist, "album", release.Album, "error", err, "component", "download")
		} else if existing != nil {
			return existing.ID, nil
		}
	}

	id := fmt.Sprintf("%s-%d-%04x", release.SourceName, time.Now().UnixNano(), rand.Intn(0xffff))

	displayName := release.Artist + " - " + release.Album
	albumType := release.AlbumType
	if albumType == "" {
		albumType = "Album"
	}

	record := &Record{
		ID:              id,
		SourceName:      release.SourceName,
		State:           StateQueued,
		DisplayName:     displayName,
		Title:           release.Album, // used by FindActiveByTitle dedup for albums
		AlbumType:       albumType,
		AlbumTracks:     tracks,
		DownloadClient:  downloadClient,
		Artist:          release.Artist,
		Album:           release.Album,
		Year:            release.Year,
		MagnetURI:       release.MagnetURI,
		Size:            release.Size,
		CoverURL:        release.CoverURL,
		Filename:        release.MagnetURI, // for backward compat with monitor dispatch
	}

	if err := s.store.Insert(ctx, record); err != nil {
		return "", fmt.Errorf("queue album: %w", err)
	}

	s.bus.Publish(ctx, events.TopicDownloadQueued, record)
	return id, nil
}

// UpdateDownload persists changes to an existing download record and fires
// a state-changed event.
func (s *Service) UpdateDownload(ctx context.Context, record *Record) error {
	if err := s.store.Update(ctx, record); err != nil {
		return fmt.Errorf("update download: %w", err)
	}
	s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)
	return nil
}

// GetStatus returns the current state of a download by ID.
func (s *Service) GetStatus(ctx context.Context, id string) (*Record, error) {
	return s.store.Get(ctx, id)
}

// List returns all download records ordered by creation time.
func (s *Service) List(ctx context.Context) ([]Record, error) {
	return s.store.List(ctx)
}

// ListByState returns downloads filtered by a single state.
func (s *Service) ListByState(ctx context.Context, state State) ([]Record, error) {
	return s.store.ListByState(ctx, state)
}

// ListActive returns all non-terminal downloads.
func (s *Service) ListActive(ctx context.Context) ([]Record, error) {
	return s.store.ListActive(ctx)
}

// Cancel transitions a download to the "ignored" state and directly cancels
// the provider-level download for both album and track downloads.
func (s *Service) Cancel(ctx context.Context, id string) error {
	s.log.Info("cancelling download", "download_id", id, "component", "download")

	s.mu.Lock()
	record, err := s.store.Get(ctx, id)
	if err != nil {
		s.mu.Unlock()
		s.log.Error("cancel: get failed", "download_id", id, "error", err, "component", "download")
		return fmt.Errorf("cancel: %w", err)
	}
	if record == nil {
		s.mu.Unlock()
		s.log.Error("cancel: not found", "download_id", id, "component", "download")
		return fmt.Errorf("cancel: download %q not found", id)
	}

	if record.State.Terminal() {
		s.mu.Unlock()
		return nil // already terminal — idempotent
	}

	// Update state BEFORE cancelling the provider. This prevents the monitor's
	// checkCancellations from issuing a duplicate cancel on the next tick.
	record.State = StateIgnored
	if err := s.store.Update(ctx, record); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("cancel: %w", err)
	}
	s.mu.Unlock()

	// Cancel at the provider level. No lock held — external I/O.
	s.cancelDownloadClient(ctx, record)
	s.cancelTrackProvider(ctx, record)

	s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)
	return nil
}

// cancelDownloadClient cancels an album download via its DownloadClient.
// If the ProviderID hasn't been persisted yet (race with startAlbumDownload),
// the monitor's checkCancellations will use its in-memory providerID instead.
func (s *Service) cancelDownloadClient(ctx context.Context, record *Record) {
	if !record.IsAlbum() || record.DownloadClient == "" {
		return
	}
	if record.ProviderID == "" {
		s.log.Warn("cancel: ProviderID not persisted yet, monitor will handle",
			"download_id", record.ID, "component", "download")
		return
	}
	if s.downloadClients == nil {
		s.log.Warn("cancel: download client registry not available",
			"client", record.DownloadClient, "component", "download")
		return
	}
	dc := s.downloadClients.Get(record.DownloadClient)
	if dc == nil {
		s.log.Warn("cancel: download client not found",
			"client", record.DownloadClient, "component", "download")
		return
	}
	if err := dc.Cancel(ctx, record.ProviderID, false); err != nil {
		s.log.Warn("cancel: provider cancel failed",
			"download_id", record.ID, "error", err, "component", "download")
	}
}

// cancelTrackProvider cancels a track download via its MonitoredProvider.
func (s *Service) cancelTrackProvider(ctx context.Context, record *Record) {
	if record.IsAlbum() {
		return
	}
	if s.registry == nil {
		return
	}
	plugin := s.registry.Get(record.SourceName)
	if plugin == nil {
		return
	}
	mp, ok := plugin.(MonitoredProvider)
	if !ok {
		return
	}
	if err := mp.Cancel(ctx, record.ProviderID, false); err != nil {
		s.log.Warn("cancel: track provider cancel failed",
			"download_id", record.ID, "source", record.SourceName,
			"error", err, "component", "download")
	}
}

// Retry resets a failed download back to "queued", fires a state-changed
// event, and optionally launches a background goroutine to search for
// alternative download sources. The MonitoringService picks up the queued
// record and drives the download.
//
// Returns an error if the download is not in a retryable state (only "failed"
// is retryable).
func (s *Service) Retry(ctx context.Context, id string) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		s.log.Error("retry: get failed", "download_id", id, "error", err, "component", "download")
		return fmt.Errorf("retry: %w", err)
	}
	if record == nil {
		s.log.Error("retry: not found", "download_id", id, "component", "download")
		return fmt.Errorf("retry: download %q not found", id)
	}

	if !record.State.IsRetryable() {
		s.log.Error("retry: not retryable", "download_id", id, "state", record.State, "component", "download")
		return fmt.Errorf("retry: download %q in state %q is not retryable", id, record.State)
	}

	// Manual retry — reset count and backoff, transition to queued immediately.
	record.RetryCount = 0
	record.RetryAfter = ""
	record.State = StateQueued
	record.Error = ""
	record.Progress = 0

	s.mu.Lock()
	registry := s.registry
	s.mu.Unlock()

	if registry != nil && record.Artist != "" && record.Title != "" {
		// Registry available with metadata — search for alternative sources
		// in background. Snapshot original fields before clearing so they
		// can be restored if the search fails.
		orig := retryOriginalSnap{
			sourcename: record.SourceName,
			filename:   record.Filename,
			size:       record.Size,
			bitrate:    record.Bitrate,
			format:     record.Format,
			username:   record.Username,
		}

		// Clear source fields so the UI shows "resolving" until the search
		// completes and repopulates them.
		record.Filename = ""
		record.Size = 0
		record.Bitrate = 0
		record.Format = ""
		record.Username = ""

		if err := s.store.Update(ctx, record); err != nil {
			return fmt.Errorf("retry: %w", err)
		}
		s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)

		// Copy record for the goroutine so it doesn't race with event
		// subscribers that may hold the original pointer.
		recCopy := *record
		go s.resolveAndSubmit(&recCopy, orig)
		return nil
	}

	// No registry or no metadata — persist immediately.
	if err := s.store.Update(ctx, record); err != nil {
		return fmt.Errorf("retry: %w", err)
	}
	s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)

	return nil
}

// resolveAndSubmit searches for an alternative download source and persists
// the resolved record. The MonitoringService picks up queued records from the
// DB. Intended for use in a background goroutine.
// orig holds the original source fields (cleared by Retry) so they can be
// restored if the search or persist fails.
func (s *Service) resolveAndSubmit(record *Record, orig retryOriginalSnap) {
	// Timeout only for the search — network calls can hang. DB operations
	// use a separate background context so they don't fail due to the
	// search consuming the timeout budget.
	searchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Recover from panics so a bug in the search/orchestrator doesn't leave
	// the record stuck in queued with empty source fields. Transition to
	// failed so the monitoring retry loop can re-attempt.
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("resolveAndSubmit: panic recovered",
				"download_id", record.ID, "panic", r, "component", "download")
			s.restoreOriginal(record, orig)
			s.failRetry(context.Background(), record,
				fmt.Sprintf("internal error during retry source resolution: %v", r))
		}
	}()

	s.resolveRetrySource(searchCtx, record)

	dbCtx := context.Background()

	if record.Filename == "" {
		// Search found nothing — restore original fields and transition back
		// to failed so the monitoring retry loop can re-attempt with backoff.
		s.restoreOriginal(record, orig)
		s.log.Warn("resolveAndSubmit: no source found, returning to failed",
			"download_id", record.ID, "component", "download")
		s.failRetry(dbCtx, record, "retry source resolution failed: no matching source found")
		return
	}

	// Persist the resolved source so the monitoring service sees it.
	if err := s.store.Update(dbCtx, record); err != nil {
		s.log.Error("resolveAndSubmit: persist failed",
			"download_id", record.ID, "error", err, "component", "download")
		s.restoreOriginal(record, orig)
		s.failRetry(dbCtx, record, "retry source resolution failed: "+err.Error())
		return
	}
	s.bus.Publish(dbCtx, events.TopicDownloadStateChanged, record)
}

// failRetry transitions a record to failed with the given error message,
// persists it, and publishes the state change event.
func (s *Service) failRetry(ctx context.Context, record *Record, errMsg string) {
	record.State = StateFailed
	record.Error = errMsg
	if err := s.store.Update(ctx, record); err != nil {
		s.log.Error("failRetry: update failed",
			"download_id", record.ID, "error", err, "component", "download")
	}
	s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)
}

// restoreOriginal restores the original source fields from a snapshot taken
// before Retry() cleared them for the UI reset.
func (s *Service) restoreOriginal(record *Record, orig retryOriginalSnap) {
	record.SourceName = orig.sourcename
	record.Filename = orig.filename
	record.Size = orig.size
	record.Bitrate = orig.bitrate
	record.Format = orig.format
	record.Username = orig.username
}

// resolveRetrySource searches for a download source for the record.
// Source fields (SourceName, Filename, Size, Bitrate, Format, Username)
// are always populated from the best match. Errors are logged but not
// returned — the caller should fall back to the existing source.
func (s *Service) resolveRetrySource(ctx context.Context, rec *Record) {
	if rec.Artist == "" || rec.Title == "" {
		return // can't search without artist+title
	}

	s.mu.Lock()
	registry := s.registry
	profileStore := s.qualityProfileStore
	s.mu.Unlock()

	if registry == nil {
		return // no plugins configured
	}

	orch := NewOrchestrator(registry, s.log)

	var profile *quality.QualityProfile
	if profileStore != nil {
		p, err := profileStore.LoadProfileByID(ctx, nil)
		if err != nil {
			s.log.Warn("retry: failed to load quality profile, using default",
				"download_id", rec.ID, "error", err, "component", "download")
		}
		profile = p
	}

	best, err := orch.FindBestMatch(ctx, rec.Title, rec.Artist, rec.Album, 0, "", profile)
	if err != nil {
		s.log.Warn("retry: search failed, retrying with original source",
			"download_id", rec.ID, "artist", rec.Artist, "title", rec.Title, "error", err, "component", "download")
		return
	}

	oldKey := rec.SourceName + "/" + rec.Filename
	newKey := best.SourceName + "/" + best.Track.Filename

	if newKey != oldKey {
		s.log.Info("retry: found alternative source",
			"download_id", rec.ID,
			"old_source", oldKey,
			"new_source", newKey,
			"component", "download",
		)
	}

	PopulateDownloadSource(rec, best.SourceName, best.Track)
}
