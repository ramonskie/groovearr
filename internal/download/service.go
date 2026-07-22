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
}

// DownloadService orchestrates the download lifecycle: queueing, status
// tracking, cancellation, retry, and worker pool dispatch.
type DownloadService struct {
	log        *slog.Logger
	store      DownloadStore
	bus        events.IEventAggregator
	workerPool WorkerPool
	mu         sync.Mutex
}

// NewDownloadService creates a DownloadService backed by the given store
// and event bus. Call SetWorkerPool before queuing downloads.
func NewDownloadService(store DownloadStore, bus events.IEventAggregator, logger *slog.Logger) *DownloadService {
	if logger == nil {
		logger = slog.Default()
	}
	return &DownloadService{
		log:   logger,
		store: store,
		bus:   bus,
	}
}

// SetWorkerPool wires the worker pool used to dispatch downloads.
func (s *DownloadService) SetWorkerPool(pool WorkerPool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workerPool = pool
}

// Queue creates a new download record in "queued" state, persists it via the
// store, fires a TopicDownloadQueued event, and dispatches to the worker pool.
// Returns the generated download ID.
func (s *DownloadService) Queue(ctx context.Context, sourceName, username, filename string, fileSize int64, meta DownloadMeta) (string, error) {
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
	}

	if err := s.store.Insert(ctx, record); err != nil {
		return "", fmt.Errorf("queue download: %w", err)
	}

	s.bus.Publish(ctx, events.TopicDownloadQueued, record)

	s.mu.Lock()
	pool := s.workerPool
	s.mu.Unlock()
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

// RecoverOrphans re-submits all download records stuck in "queued" state
// to the worker pool. Called at startup to recover items that were inserted
// into the DB but failed pool submission (e.g., pool was at capacity).
func (s *DownloadService) RecoverOrphans(ctx context.Context) {
	s.mu.Lock()
	pool := s.workerPool
	s.mu.Unlock()
	if pool == nil {
		return
	}

	queued, err := s.store.ListByState(ctx, domain.DownloadQueued)
	if err != nil {
		s.log.Error("recover orphans: list failed", "error", err, "component", "download")
		return
	}
	if len(queued) == 0 {
		return
	}

	s.log.Info("recover orphans: re-submitting", "count", len(queued), "component", "download")
	recovered := 0
	for _, r := range queued {
		// Skip records queued via QueuePending — they have no source info
		// and need resolution, not worker dispatch.
		if r.IsPendingSource() {
			continue
		}
		if err := pool.Submit(ctx, &r); err != nil {
			s.log.Error("recover orphans: re-submit failed", "download_id", r.ID, "error", err, "component", "download")
			continue
		}
		recovered++
	}
	s.log.Info("recover orphans: done", "recovered", recovered, "total", len(queued), "component", "download")
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
// state (only "failed" is retryable).
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
