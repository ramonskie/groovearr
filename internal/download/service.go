package download

import (
	"context"
	"fmt"
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
	CoverURL    string
	PlaylistID  string
}

// DownloadService orchestrates the download lifecycle: queueing, status
// tracking, cancellation, retry, and worker pool dispatch.
type DownloadService struct {
	store      DownloadStore
	bus        events.IEventAggregator
	workerPool WorkerPool
	mu         sync.Mutex
}

// NewDownloadService creates a DownloadService backed by the given store
// and event bus. Call SetWorkerPool before queuing downloads.
func NewDownloadService(store DownloadStore, bus events.IEventAggregator) *DownloadService {
	return &DownloadService{
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

// GetStatus returns the current state of a download by ID.
func (s *DownloadService) GetStatus(ctx context.Context, id string) (*domain.DownloadRecord, error) {
	return s.store.Get(ctx, id)
}

// List returns all download records ordered by creation time.
func (s *DownloadService) List(ctx context.Context) ([]domain.DownloadRecord, error) {
	return s.store.List(ctx)
}

// Cancel transitions a download to the "ignored" state, cancels the
// in-progress worker goroutine, and fires a state-changed event.
func (s *DownloadService) Cancel(ctx context.Context, id string) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	if record == nil {
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
		return fmt.Errorf("retry: %w", err)
	}
	if record == nil {
		return fmt.Errorf("retry: download %q not found", id)
	}

	if !record.State.IsRetryable() {
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
