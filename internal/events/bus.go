// Package events provides an event bus for decoupled, asynchronous communication
// between download pipeline components.
package events

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
)

// Topic constants define the event channels available in the download pipeline.
const (
	TopicDownloadQueued       = "download:queued"
	TopicDownloadStateChanged = "download:stateChanged"
	TopicDownloadProgress     = "download:progress"
	TopicDownloadCompleted    = "download:completed"
	TopicDownloadFailed       = "download:failed"
	TopicImportStarted        = "import:started"
	TopicImportCompleted      = "import:completed"
	TopicImportFailed         = "import:failed"
)

// EventHandler is a callback that receives events for a subscribed topic.
type EventHandler func(ctx context.Context, event any)

// IEventAggregator defines the contract for a publish-subscribe event bus.
// Implementations must be safe for concurrent use.
type IEventAggregator interface {
	// Subscribe registers handler to receive events published on topic.
	Subscribe(topic string, handler EventHandler)

	// Unsubscribe removes a previously registered handler from topic.
	// It is a no-op if the handler was not subscribed to the topic.
	Unsubscribe(topic string, handler EventHandler)

	// Publish delivers event to every subscriber of topic asynchronously.
	// Each handler runs in its own goroutine. Panics inside handlers are
	// recovered and logged — they never propagate to the caller.
	Publish(ctx context.Context, topic string, event any)
}

// InMemoryEventBus is a concurrent-safe, in-memory implementation of
// IEventAggregator. Handlers are stored per-topic and invoked in separate
// goroutines.
type InMemoryEventBus struct {
	mu       sync.RWMutex
	handlers map[string][]EventHandler
	logger   *slog.Logger
}

// NewInMemoryEventBus creates a ready-to-use InMemoryEventBus.
func NewInMemoryEventBus(logger *slog.Logger) *InMemoryEventBus {
	if logger == nil {
		logger = slog.Default()
	}
	return &InMemoryEventBus{
		handlers: make(map[string][]EventHandler),
		logger:   logger,
	}
}

// Subscribe registers handler for topic.
func (b *InMemoryEventBus) Subscribe(topic string, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], handler)
}

// Unsubscribe removes handler from topic. Does nothing if handler is not found.
func (b *InMemoryEventBus) Unsubscribe(topic string, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()

	handlers := b.handlers[topic]
	for i, h := range handlers {
		if functionsEqual(h, handler) {
			b.handlers[topic] = append(handlers[:i], handlers[i+1:]...)
			return
		}
	}
}

// Publish delivers event to all subscribers of topic. Each handler runs in its
// own goroutine so a slow handler never blocks other subscribers. Panics inside
// handlers are recovered and logged.
func (b *InMemoryEventBus) Publish(ctx context.Context, topic string, event any) {
	b.mu.RLock()
	handlers := make([]EventHandler, len(b.handlers[topic]))
	copy(handlers, b.handlers[topic])
	b.mu.RUnlock()

	for _, handler := range handlers {
		h := handler // capture for closure
		go func() {
			defer func() {
				if r := recover(); r != nil {
					b.logger.Error("panic in handler", "topic", topic, "error", r, "component", "events")
				}
			}()
			h(ctx, event)
		}()
	}
}

// functionsEqual reports whether two EventHandler values refer to the same
// underlying function by comparing their code pointers via reflection.
func functionsEqual(a, b EventHandler) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}
