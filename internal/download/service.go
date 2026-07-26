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
)

// WorkerPool dispatches download tasks to workers for execution.
// Workers are responsible for driving the state machine from queued
// through downloading, importPending, importing, and imported (or failed).
type WorkerPool interface {
	// Submit enqueues a download record for processing by an available worker.
	Submit(ctx context.Context, record *domain.DownloadRecord) error

	// Cancel stops an in-progress download by cancelling its context.
	Cancel(downloadID string)

	// Shutdown gracefully stops all workers and waits for them to exit.
	Shutdown()
}

// DownloadMeta carries track metadata supplied at queue time.
type DownloadMeta struct {
	Artist      string
	Album       string
	Title       string
	TrackNumber int
	DiscNumber  int
	Year        int
	TrackID     string
	ISRC        string
	CoverURL    string
	PlaylistID  string
	Bitrate     int    // kbps
	Format      string // "flac", "mp3", etc.
}

// DownloadService orchestrates the download lifecycle: queueing, status
// tracking, cancellation, retry, and worker pool dispatch.
type DownloadService struct {
	log        *slog.Logger
	store      DownloadStore
	bus        events.IEventAggregator
	workerPool WorkerPool
	registry   *Registry // needed for pending source resolution
	mu         sync.Mutex
}

// NewDownloadService creates a DownloadService backed by the given store,
// event bus, and worker pool. The pool is required — pass nil only in tests
// where worker dispatch is not needed.
func NewDownloadService(store DownloadStore, bus events.IEventAggregator, logger *slog.Logger, pool WorkerPool) *DownloadService {
	if logger == nil {
		logger = slog.Default()
	}
	return &DownloadService{
		log:        logger,
		store:      store,
		bus:        bus,
		workerPool: pool,
	}
}

// SetWorkerPool replaces the worker pool at runtime. Prefer passing the pool
// via NewDownloadService — this method exists for tests and runtime overrides.
func (s *DownloadService) SetWorkerPool(pool WorkerPool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workerPool = pool
}

// SetRegistry sets the plugin registry for pending source resolution.
// Set before calling RecoverOrphans for full startup recovery.
func (s *DownloadService) SetRegistry(registry *Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registry = registry
}

// Queue creates a new download record in "queued" state, persists it via the
// store, fires a TopicDownloadQueued event, and dispatches to the worker pool.
// Skips if an active download already exists for the same artist+title.
// Returns the generated download ID.
func (s *DownloadService) Queue(ctx context.Context, sourceName, username, filename string, fileSize int64, meta DownloadMeta) (string, error) {
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

	record := &domain.DownloadRecord{
		ID:          id,
		SourceName:  sourceName,
		Username:    username,
		Filename:    filename,
		DisplayName: displayName,
		State:       domain.DownloadQueued,
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

	pool := s.workerPool
	if pool != nil {
		if err := pool.Submit(ctx, record); err != nil {
			return id, fmt.Errorf("worker dispatch: %w", err)
		}
	}

	return id, nil
}

// QueuePending inserts a download record with metadata only (no resolved
// source), fires the download:queued event, and does NOT dispatch to the
// worker pool. The record is resolved later (search + update + dispatch) by
// the caller — see playlist.resolvePendingDownloads.
//
// This enables batch-queuing all items first (visible in UI), then resolving
// them in the background.
func (s *DownloadService) QueuePending(ctx context.Context, meta DownloadMeta) (string, error) {
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

	record := &domain.DownloadRecord{
		ID:          id,
		SourceName:  domain.PendingSourceName,
		DisplayName: displayName,
		State:       domain.DownloadQueued,
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

// UpdateDownload persists changes to an existing download record and fires
// a state-changed event.
func (s *DownloadService) UpdateDownload(ctx context.Context, record *domain.DownloadRecord) error {
	if err := s.store.Update(ctx, record); err != nil {
		return fmt.Errorf("update download: %w", err)
	}
	s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)
	return nil
}

// Dispatch submits a resolved download record to the worker pool.
func (s *DownloadService) Dispatch(ctx context.Context, record *domain.DownloadRecord) error {
	s.mu.Lock()
	pool := s.workerPool
	s.mu.Unlock()
	if pool == nil {
		return fmt.Errorf("dispatch: worker pool not set")
	}
	return pool.Submit(ctx, record)
}

// RecoverOrphans recovers download records left in non-terminal states after a
// previous run's shutdown (container restart, crash, etc.).
//
// Recovery strategy per state:
//   - queued:        re-submit to worker pool (existing behavior)
//   - downloading:   transition to failed (worker goroutine died, retry picks up)
//   - importPending: re-publish download:completed event (file already on disk,
//     CompletedDownloadService picks up the import chain)
//   - importing:     transition to failed (import chain interrupted mid-flight,
//     safer to re-download than guess partial completion)
func (s *DownloadService) RecoverOrphans(ctx context.Context) {
	s.mu.Lock()
	pool := s.workerPool
	s.mu.Unlock()

	active, err := s.store.ListActive(ctx)
	if err != nil {
		s.log.Error("recover orphans: list active failed", "error", err, "component", "download")
		return
	}
	if len(active) == 0 {
		return
	}

	var queued []domain.DownloadRecord
	var downloading, importPending, importing int

	for _, rec := range active {
		switch rec.State {
		case domain.DownloadQueued:
			// Separated into resolved vs pending later (lines 312–321).
			queued = append(queued, rec)

		case domain.DownloadDownloading:
			if ok, err := s.store.TransitionState(ctx, rec.ID, domain.DownloadDownloading, domain.DownloadFailed); err != nil {
				s.log.Error("recover orphans: downloading->failed transition failed", "download_id", rec.ID, "error", err, "component", "download")
				continue
			} else if !ok {
				s.log.Warn("recover orphans: downloading state changed, skipping", "download_id", rec.ID, "component", "download")
				continue
			}
			rec.State = domain.DownloadFailed
			rec.Error = "download interrupted by server restart"
			if err := s.store.Update(ctx, &rec); err != nil {
				s.log.Warn("recover orphans: update error field failed", "download_id", rec.ID, "error", err, "component", "download")
			}
			s.bus.Publish(ctx, events.TopicDownloadFailed, &rec)
			downloading++

		case domain.DownloadImportPending:
			// File is already on disk from the previous run — just re-trigger
			// the import chain. No CAS check needed here: the record stays in
			// importPending state, and CompletedDownloadService re-reads it
			// from the store and checks state before importing anyway.
			s.log.Info("recover orphans: re-triggering import", "download_id", rec.ID, "component", "download")
			s.bus.Publish(ctx, events.TopicDownloadCompleted, &rec)
			importPending++

		case domain.DownloadFailedPending:
			// Handled by the playlist service's retry worker. Log at debug
			// level to avoid noise — these are expected on every restart.
			s.log.Debug("recover orphans: skipping failedPending, handled by playlist retry worker", "download_id", rec.ID, "component", "download")

		case domain.DownloadImporting:
			if ok, err := s.store.TransitionState(ctx, rec.ID, domain.DownloadImporting, domain.DownloadFailed); err != nil {
				s.log.Error("recover orphans: importing->failed transition failed", "download_id", rec.ID, "error", err, "component", "download")
				continue
			} else if !ok {
				s.log.Warn("recover orphans: importing state changed, skipping", "download_id", rec.ID, "component", "download")
				continue
			}
			rec.State = domain.DownloadFailed
			rec.Error = "import interrupted by server restart"
			if err := s.store.Update(ctx, &rec); err != nil {
				s.log.Warn("recover orphans: update error field failed", "download_id", rec.ID, "error", err, "component", "download")
			}
			s.bus.Publish(ctx, events.TopicDownloadFailed, &rec)
			importing++
		}
	}

	// Separate resolved (ready for pool) from pending (need source resolution).
	var resolved []domain.DownloadRecord
	var pendingCount int
	for _, rec := range queued {
		if rec.IsPendingSource() {
			pendingCount++
		} else {
			resolved = append(resolved, rec)
		}
	}

	// Re-submit resolved records to the worker pool.
	if len(resolved) > 0 && pool != nil {
		s.log.Info("recover orphans: re-submitting resolved", "count", len(resolved), "component", "download")
		recovered, _ := s.submitQueued(ctx, pool, resolved, false)
		s.log.Info("recover orphans: resolved done", "recovered", recovered, "total", len(resolved), "component", "download")
	}

	// Resolve pending source records (search + dispatch).
	if pendingCount > 0 {
		s.ResolvePendingSources(ctx)
	}

	if downloading+importPending+importing > 0 {
		s.log.Info("recover orphans: state recovery complete",
			"downloading", downloading,
			"importPending", importPending,
			"importing", importing,
			"pendingQueued", pendingCount,
			"component", "download",
		)
	}
}

// ResolvePendingSources resolves all queued downloads that are in pending
// source state (source_name="pending", no filename). For each record it:
//  1. Searches for a matching download via the orchestrator
//  2. Updates the record with resolved source/filename
//  3. Dispatches to the worker pool
//
// On resolution failure, the record transitions to failedPending for the
// playlist retry worker to handle with backoff.
func (s *DownloadService) ResolvePendingSources(ctx context.Context) {
	s.mu.Lock()
	pool := s.workerPool
	registry := s.registry
	s.mu.Unlock()

	if registry == nil {
		s.log.Warn("resolve pending: no registry, skipping resolution", "component", "download")
		return
	}

	queued, err := s.store.ListByState(ctx, domain.DownloadQueued)
	if err != nil {
		s.log.Error("resolve pending: list queued failed", "error", err, "component", "download")
		return
	}

	var pending []domain.DownloadRecord
	for _, rec := range queued {
		if rec.IsPendingSource() {
			pending = append(pending, rec)
		}
	}
	if len(pending) == 0 {
		return
	}

	s.log.Info("resolve pending: resolving sources", "count", len(pending), "component", "download")
	orch := NewOrchestrator(registry, s.log)
	resolved := 0

	for _, rec := range pending {
		if rec.State.Terminal() {
			continue // state changed while we were scanning
		}
		if rec.Artist == "" || rec.Title == "" {
			s.log.Warn("resolve pending: missing artist/title", "download_id", rec.ID, "component", "download")
			continue
		}

		best, err := orch.FindBestMatch(ctx, rec.Title, rec.Artist, rec.Album, 0, "", nil)
		if err != nil {
			s.log.Warn("resolve pending: search failed", "download_id", rec.ID, "artist", rec.Artist, "title", rec.Title, "error", err, "component", "download")
			rec.State = domain.DownloadFailedPending
			rec.Error = err.Error()
			rec.RetryCount = 1
			rec.RetryAfter = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
			_ = s.store.Update(ctx, &rec)
			continue
		}

		// Populate resolved fields.
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
		rec.DisplayName = rec.Artist + " - " + rec.Title

		if err := s.store.Update(ctx, &rec); err != nil {
			s.log.Error("resolve pending: update failed", "download_id", rec.ID, "error", err, "component", "download")
			continue
		}

		// Dispatch to worker pool.
		if pool != nil {
			if err := pool.Submit(ctx, &rec); err != nil {
				s.log.Error("resolve pending: dispatch failed", "download_id", rec.ID, "error", err, "component", "download")
				continue
			}
		}
		resolved++
	}

	if resolved > 0 {
		s.log.Info("resolve pending: done", "resolved", resolved, "component", "download")
	}
}

const dispatchInterval = 60 * time.Second

// StartDispatcher periodically rescans for queued downloads and submits them
// to the worker pool. This handles records that were inserted into the DB but
// whose initial pool.Submit failed (e.g., pool at capacity during batch imports).
// Runs until ctx is cancelled. Does not block the caller.
func (s *DownloadService) StartDispatcher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(dispatchInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.dispatchQueued(ctx)
			}
		}
	}()
}

func (s *DownloadService) dispatchQueued(ctx context.Context) {
	s.mu.Lock()
	pool := s.workerPool
	s.mu.Unlock()
	if pool == nil {
		return
	}

	queued, err := s.store.ListByState(ctx, domain.DownloadQueued)
	if err != nil {
		s.log.Warn("dispatch scan: list failed", "error", err, "component", "download")
		return
	}

	dispatched, _ := s.submitQueued(ctx, pool, queued, true) // true = stop on full
	if dispatched > 0 {
		s.log.Debug("dispatch scan: submitted", "count", dispatched, "component", "download")
	}
}

// submitQueued iterates over queued records and submits them to the pool.
// When stopOnFull is true, it breaks on the first submission error (pool at
// capacity). When false, it logs each error and continues.
// Returns (dispatched, full) — full is true when the loop stopped because
// the pool was full.
func (s *DownloadService) submitQueued(ctx context.Context, pool WorkerPool, queued []domain.DownloadRecord, stopOnFull bool) (int, bool) {
	dispatched := 0
	for _, r := range queued {
		rec := r // copy for pointer safety
		if rec.IsPendingSource() {
			continue
		}
		// Respect retry backoff — don't submit before RetryAfter.
		if rec.RetryAfter != "" {
			if t, err := time.Parse(time.RFC3339, rec.RetryAfter); err == nil && time.Now().UTC().Before(t) {
				continue
			}
		}
		if err := pool.Submit(ctx, &rec); err != nil {
			if stopOnFull {
				return dispatched, true
			}
			s.log.Error("submit queued: failed", "download_id", rec.ID, "error", err, "component", "download")
			continue
		}
		dispatched++
	}
	return dispatched, false
}

// GetStatus returns the current state of a download by ID.
func (s *DownloadService) GetStatus(ctx context.Context, id string) (*domain.DownloadRecord, error) {
	return s.store.Get(ctx, id)
}

// List returns all download records ordered by creation time.
func (s *DownloadService) List(ctx context.Context) ([]domain.DownloadRecord, error) {
	return s.store.List(ctx)
}

// ListByState returns downloads filtered by a single state.
func (s *DownloadService) ListByState(ctx context.Context, state domain.DownloadState) ([]domain.DownloadRecord, error) {
	return s.store.ListByState(ctx, state)
}

// ListActive returns all non-terminal downloads.
func (s *DownloadService) ListActive(ctx context.Context) ([]domain.DownloadRecord, error) {
	return s.store.ListActive(ctx)
}

// Cancel transitions a download to the "ignored" state, cancels the
// in-progress worker goroutine, and fires a state-changed event.
func (s *DownloadService) Cancel(ctx context.Context, id string) error {
	s.log.Info("cancelling download", "download_id", id, "component", "download")

	record, err := s.store.Get(ctx, id)
	if err != nil {
		s.log.Error("cancel: get failed", "download_id", id, "error", err, "component", "download")
		return fmt.Errorf("cancel: %w", err)
	}
	if record == nil {
		s.log.Error("cancel: not found", "download_id", id, "component", "download")
		return fmt.Errorf("cancel: download %q not found", id)
	}

	if record.State.Terminal() {
		return nil // already terminal — idempotent
	}

	// Cancel the in-progress worker goroutine first so it doesn't
	// overwrite the ignored state when it completes/fails.
	s.mu.Lock()
	pool := s.workerPool
	s.mu.Unlock()
	if pool != nil {
		pool.Cancel(id)
	}

	record.State = domain.DownloadIgnored
	if err := s.store.Update(ctx, record); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}

	s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)
	return nil
}

// Retry resets a failed download back to "queued" and re-dispatches it to
// the worker pool. Returns an error if the download is not in a retryable
// state (only "failed" is retryable), or if max retries have been reached.
func (s *DownloadService) Retry(ctx context.Context, id string) error {
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

	// Manual retry — reset count and backoff, dispatch immediately.
	record.RetryCount = 0
	record.RetryAfter = ""
	return s.dispatchRetry(ctx, record)
}

// dispatchRetry transitions a record to queued, persists, and dispatches
// to the worker pool. Used by both manual Retry and auto-retry worker.
func (s *DownloadService) dispatchRetry(ctx context.Context, record *domain.DownloadRecord) error {
	record.State = domain.DownloadQueued
	record.Error = ""
	record.Progress = 0
	if err := s.store.Update(ctx, record); err != nil {
		return fmt.Errorf("retry: %w", err)
	}

	s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)

	s.mu.Lock()
	pool := s.workerPool
	s.mu.Unlock()
	if pool != nil {
		if err := pool.Submit(ctx, record); err != nil {
			return fmt.Errorf("retry dispatch: %w", err)
		}
	}

	return nil
}

// StartRetryWorker runs a periodic goroutine that scans for failed downloads
// and retries them (up to MaxRetries). Runs until ctx is cancelled.
func (s *DownloadService) StartRetryWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	s.log.Info("download retry worker started", "interval", interval, "max_retries", domain.MaxRetries, "component", "download")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("download retry worker panicked", "panic", r, "component", "download")
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.log.Info("download retry worker stopped", "component", "download")
				return
			case <-ticker.C:
				s.retryFailedDownloads(ctx)
			}
		}
	}()
}

// retryFailedDownloads lists failed downloads and retries those that haven't
// exceeded MaxRetries and whose RetryAfter time has passed.
func (s *DownloadService) retryFailedDownloads(ctx context.Context) {
	failed, err := s.store.ListByState(ctx, domain.DownloadFailed)
	if err != nil {
		s.log.Error("retry worker: list failed", "error", err, "component", "download")
		return
	}
	if len(failed) == 0 {
		return
	}

	retried := 0
	for _, rec := range failed {
		if rec.RetryCount >= domain.MaxRetries {
			continue
		}
		if rec.RetryAfter != "" {
			retryAfter, parseErr := time.Parse(time.RFC3339, rec.RetryAfter)
			if parseErr == nil && time.Now().UTC().Before(retryAfter) {
				continue
			}
		}

		// Increment retry count and set exponential backoff before dispatch.
		rec.RetryCount++
		backoffMin := 1 << rec.RetryCount // 2, 4, 8, 16, 32
		if backoffMin > 60 {
			backoffMin = 60
		}
		rec.RetryAfter = time.Now().UTC().Add(time.Duration(backoffMin) * time.Minute).Format(time.RFC3339)

		if err := s.dispatchRetry(ctx, &rec); err != nil {
			s.log.Warn("retry worker: retry failed", "download_id", rec.ID, "error", err, "component", "download")
			continue
		}
		retried++
	}
	if retried > 0 {
		s.log.Info("retry worker: retried", "count", retried, "component", "download")
	}
}
