package sse

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

// SSENotifier subscribes to the event bus and translates download pipeline
// events into SSE events that are broadcast via the SSEHub.
//
// It also implements download.ImportHandler so it can be placed last in the
// import handler chain as a synchronous fallback for import-completed
// notifications.
type SSENotifier struct {
	hub *SSEHub
}

// NewSSENotifier creates an SSENotifier, subscribes to the relevant event bus
// topics, and returns the notifier. The notifier must be kept alive for the
// lifetime of the event bus subscriptions.
func NewSSENotifier(hub *SSEHub, bus events.IEventAggregator) *SSENotifier {
	n := &SSENotifier{hub: hub}

	bus.Subscribe(events.TopicDownloadProgress, n.onProgress)
	bus.Subscribe(events.TopicDownloadCompleted, n.onDownloadCompleted)
	bus.Subscribe(events.TopicDownloadFailed, n.onDownloadFailed)
	bus.Subscribe(events.TopicImportCompleted, n.onImportCompleted)

	return n
}

// broadcastRecord marshals a download record as JSON and broadcasts it as an
// SSE event of the given type.
func (n *SSENotifier) broadcastRecord(record *domain.DownloadRecord, eventType string) {
	data, err := json.Marshal(record)
	if err != nil {
		log.Printf("sse: marshal download %s: %v", record.ID, err)
		return
	}

	n.hub.Broadcast(SSEEvent{
		ID:        record.ID,
		Type:      eventType,
		Data:      data,
		Timestamp: time.Now(),
	})
}

func (n *SSENotifier) onProgress(ctx context.Context, event any) {
	record, ok := event.(*domain.DownloadRecord)
	if !ok {
		return
	}
	n.broadcastRecord(record, "download_progress")
}

func (n *SSENotifier) onDownloadCompleted(ctx context.Context, event any) {
	record, ok := event.(*domain.DownloadRecord)
	if !ok {
		return
	}
	n.broadcastRecord(record, "download_completed")
}

func (n *SSENotifier) onDownloadFailed(ctx context.Context, event any) {
	record, ok := event.(*domain.DownloadRecord)
	if !ok {
		return
	}
	n.broadcastRecord(record, "download_failed")
}

func (n *SSENotifier) onImportCompleted(ctx context.Context, event any) {
	record, ok := event.(*domain.DownloadRecord)
	if !ok {
		return
	}
	n.broadcastRecord(record, "import_completed")
}

// Handle implements download.ImportHandler. When the notifier is placed in the
// import handler chain, it broadcasts the record as a final SSE notification.
func (n *SSENotifier) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	n.broadcastRecord(record, "import_completed")
	return nil
}
