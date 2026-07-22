// Package download provides the import handler chain for completed downloads.
package download

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

// ImportHandler processes a download record as part of the post-download
// import pipeline. Implementations must be safe for concurrent use.
type ImportHandler interface {
	Handle(ctx context.Context, record *domain.DownloadRecord) error
}

// CompletedDownloadService subscribes to download completion events and runs
// a sequential chain of import handlers on each completed download.
type CompletedDownloadService struct {
	log      *slog.Logger
	store    DownloadStore
	bus      events.IEventAggregator
	handlers []ImportHandler
}

// NewCompletedDownloadService creates a service that subscribes to
// TopicDownloadCompleted on the given event bus and processes completed
// downloads through the provided handler chain.
func NewCompletedDownloadService(
	store DownloadStore,
	bus events.IEventAggregator,
	logger *slog.Logger,
	handlers ...ImportHandler,
) *CompletedDownloadService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &CompletedDownloadService{
		log:      logger,
		store:    store,
		bus:      bus,
		handlers: handlers,
	}
	bus.Subscribe(events.TopicDownloadCompleted, s.onDownloadCompleted)
	return s
}

// onDownloadCompleted is the event handler for TopicDownloadCompleted events.
// It fetches the full record from the store, transitions state to "importing",
// runs the handler chain sequentially, and publishes success/failure events.
func (s *CompletedDownloadService) onDownloadCompleted(ctx context.Context, event any) {
	evt, ok := event.(*domain.DownloadRecord)
	if !ok {
		return
	}

	record, err := s.store.Get(ctx, evt.ID)
	if err != nil || record == nil {
		s.log.Error("failed to fetch download", "download_id", evt.ID, "error", err, "component", "importer")
		return
	}

	if record.State != domain.DownloadImportPending {
		s.log.Warn("download state mismatch, skipping import chain", "download_id", record.ID, "state", string(record.State), "component", "importer")
		return
	}

	// Transition to importing atomically. If the state changed (e.g., cancelled
	// concurrently), abort immediately.
	transited, err := s.store.TransitionState(ctx, record.ID, domain.DownloadImportPending, domain.DownloadImporting)
	if err != nil {
		s.log.Error("state update failed", "download_id", record.ID, "error", err, "component", "importer")
		return
	}
	if !transited {
		s.log.Warn("state changed during import, aborting", "download_id", record.ID, "component", "importer")
		return
	}
	record.State = domain.DownloadImporting
	s.bus.Publish(ctx, events.TopicImportStarted, record)

	// Run handler chain sequentially. First failure stops the chain.
	// Re-check state between handlers in case a concurrent cancel took effect.
	for _, h := range s.handlers {
		current, checkErr := s.store.Get(ctx, record.ID)
		if checkErr == nil && current != nil && current.State.Terminal() {
			s.log.Warn("reached terminal state, aborting chain", "download_id", record.ID, "state", string(current.State), "component", "importer")
			return
		}
		if err := h.Handle(ctx, record); err != nil {
			s.log.Error("handler failed", "handler", fmt.Sprintf("%T", h), "download_id", record.ID, "error", err, "component", "importer")
			record.State = domain.DownloadFailed
			record.Error = err.Error()
			if updateErr := s.store.Update(ctx, record); updateErr != nil {
				s.log.Error("failed to persist error", "download_id", record.ID, "error", updateErr, "component", "importer")
			}
			s.bus.Publish(ctx, events.TopicImportFailed, record)
			return
		}
	}

	s.log.Info("import chain complete", "download_id", record.ID, "state", string(record.State), "component", "importer")

	// All handlers succeeded.
	record.State = domain.DownloadImported
	if err := s.store.Update(ctx, record); err != nil {
		s.log.Error("final state update failed", "download_id", record.ID, "error", err, "component", "importer")
		return
	}
	s.bus.Publish(ctx, events.TopicImportCompleted, record)
}
