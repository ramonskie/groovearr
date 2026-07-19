// Package download provides the import handler chain for completed downloads.
package download

import (
	"context"
	"log"

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
	handlers ...ImportHandler,
) *CompletedDownloadService {
	s := &CompletedDownloadService{
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
		log.Printf("importer: failed to fetch download %s: %v", evt.ID, err)
		return
	}

	if record.State != domain.DownloadImportPending {
		return
	}

	// Transition to importing atomically. If the state changed (e.g., cancelled
	// concurrently), abort immediately.
	transited, err := s.store.TransitionState(ctx, record.ID, domain.DownloadImportPending, domain.DownloadImporting)
	if err != nil {
		log.Printf("importer: state update for %s: %v", record.ID, err)
		return
	}
	if !transited {
		log.Printf("importer: download %s state changed during import, aborting", record.ID)
		return
	}
	record.State = domain.DownloadImporting
	s.bus.Publish(ctx, events.TopicImportStarted, record)

	// Run handler chain sequentially. First failure stops the chain.
	// Re-check state between handlers in case a concurrent cancel took effect.
	for _, h := range s.handlers {
		current, checkErr := s.store.Get(ctx, record.ID)
		if checkErr == nil && current != nil && current.State.Terminal() {
			log.Printf("importer: download %s reached terminal state (%s), aborting chain", record.ID, current.State)
			return
		}
		if err := h.Handle(ctx, record); err != nil {
			log.Printf("importer: handler %T failed for %s: %v", h, record.ID, err)
			record.State = domain.DownloadFailed
			record.Error = err.Error()
			if updateErr := s.store.Update(ctx, record); updateErr != nil {
				log.Printf("importer: failed to persist error for %s: %v", record.ID, updateErr)
			}
			s.bus.Publish(ctx, events.TopicImportFailed, record)
			return
		}
	}

	// All handlers succeeded.
	record.State = domain.DownloadImported
	if err := s.store.Update(ctx, record); err != nil {
		log.Printf("importer: final state update for %s: %v", record.ID, err)
		return
	}
	s.bus.Publish(ctx, events.TopicImportCompleted, record)
}
