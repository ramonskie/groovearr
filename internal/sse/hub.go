// Package sse provides a Server-Sent Events hub for pushing real-time
// download pipeline progress to connected web clients.
package sse

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// clientBufferSize is the capacity of each SSE client's event channel.
	// When the buffer is full, new events are silently dropped for that client.
	clientBufferSize = 64

	// heartbeatInterval is how often keepalive SSE comments are sent to each
	// connected client.
	heartbeatInterval = 15 * time.Second
)

// SSEEvent represents a single event that is broadcast to all connected SSE
// clients.
type SSEEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	Timestamp time.Time       `json:"timestamp"`
}

// SSEHub manages connected SSE clients. It is safe for concurrent use.
type SSEHub struct {
	mu      sync.RWMutex
	clients map[int64]*subscriber
	nextID  atomic.Int64
	log     *slog.Logger
}

// subscriber is a single connected SSE client together with the role that
// governs which event types it may receive and the account that owns the
// stream. Admin-only events (log lines and job snapshots) are withheld from
// non-admin subscribers; download/import events and heartbeats reach everyone.
// userID lets a role/credential change tear down that account's live streams
// immediately (0 when the identity is anonymous, e.g. auth.method "none").
type subscriber struct {
	ch     chan SSEEvent
	admin  bool
	userID int64
}

// NewSSEHub creates a ready-to-use SSEHub.
func NewSSEHub(logger *slog.Logger) *SSEHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &SSEHub{
		clients: make(map[int64]*subscriber),
		log:     logger,
	}
}

// Register adds a client channel and returns the assigned client ID. admin
// records whether the subscriber may receive admin-only event types
// (log_line, job_*). userID records which account owns the stream so it can be
// terminated on a role/credential change (0 when unknown). Every subscriber
// still receives download/import events and heartbeats. The caller is
// responsible for reading from the channel and calling Unregister when the
// client disconnects.
func (h *SSEHub) Register(client chan SSEEvent, admin bool, userID int64) int64 {
	id := h.nextID.Add(1)

	h.mu.Lock()
	h.clients[id] = &subscriber{ch: client, admin: admin, userID: userID}
	h.mu.Unlock()

	return id
}

// Unregister removes a client and closes its channel. It is safe to call
// multiple times for the same client; subsequent calls are no-ops.
func (h *SSEHub) Unregister(clientID int64) {
	h.mu.Lock()
	sub, ok := h.clients[clientID]
	if ok {
		delete(h.clients, clientID)
	}
	h.mu.Unlock()

	if ok {
		close(sub.ch)
	}
}

// UnregisterByUserID closes and removes every subscriber owned by userID. It
// is called when an account is demoted, disabled, or deleted so a stream that
// resolved its role at connect cannot keep receiving admin-only events.
//
// Channels are closed after the hub lock is released, mirroring Unregister and
// Shutdown, so no close can block a concurrent Register/Broadcast and there is
// no deadlock with Shutdown (both delete under the same lock before closing,
// so a channel is closed at most once).
func (h *SSEHub) UnregisterByUserID(userID int64) {
	h.mu.Lock()
	var closed []chan SSEEvent
	for id, sub := range h.clients {
		if sub.userID == userID {
			delete(h.clients, id)
			closed = append(closed, sub.ch)
		}
	}
	h.mu.Unlock()

	for _, ch := range closed {
		close(ch)
	}
}

// isAdminOnlyEvent reports whether an event type must only be delivered to
// admin subscribers. Log lines and job lifecycle events (job_started,
// job_progress, job_completed, job_failed, job_cancelled) are admin-only;
// download, import and heartbeat events are shared. Centralized here so the
// classification can be extended in one place.
func isAdminOnlyEvent(eventType string) bool {
	return eventType == "log_line" || strings.HasPrefix(eventType, "job_")
}

// Broadcast sends an event to every eligible registered client. Sends are
// non-blocking: if a client's buffer is full the event is silently dropped for
// that client. Admin-only events are skipped for non-admin subscribers.
func (h *SSEHub) Broadcast(event SSEEvent) {
	adminOnly := isAdminOnlyEvent(event.Type)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.clients {
		if adminOnly && !sub.admin {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			// Slow client — drop to avoid blocking the broadcaster.
		}
	}
}

// ClientCount returns the current number of connected clients.
func (h *SSEHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Shutdown closes every client channel, unblocking active SSE streams so the
// HTTP server can shut down cleanly (Go's http.Server.Shutdown waits for
// active connections, and a streaming SSE connection is active until its
// handler returns). Idempotent and safe with concurrent Broadcast: the hub
// lock serializes the map access, and ServeHTTP treats a closed channel as
// disconnect (same as Unregister).
func (h *SSEHub) Shutdown() {
	h.mu.Lock()
	for id, sub := range h.clients {
		delete(h.clients, id)
		close(sub.ch)
	}
	h.mu.Unlock()
}

// StartHeartbeat launches a background goroutine that sends keepalive events
// to all connected clients every heartbeatInterval. The goroutine exits when
// ctx is cancelled.
func (h *SSEHub) StartHeartbeat(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.Broadcast(SSEEvent{
					Type:      "heartbeat",
					Timestamp: time.Now(),
				})
			}
		}
	}()
}

// ServeHTTP streams events to one SSE client until the request context is
// cancelled (client disconnects). admin is the subscriber's role: admin-only
// event types (log_line, job_*) are withheld when it is false. userID records
// which account owns the stream so UnregisterByUserID can terminate it on a
// role/credential change (0 when the identity is anonymous).
//
// It sets the required SSE response headers, registers the client's channel,
// and streams events until the request context is cancelled. Each SSEEvent is
// formatted according to the SSE protocol:
//
//	id: <id>
//	event: <type>
//	data: <data>
//
// Heartbeat events are written as SSE comments (": keepalive\n\n").
//
// Note: this method takes explicit role/userID arguments, so it does not itself
// satisfy http.Handler; callers (see api.handleEvents) resolve them from the
// request Identity and pass them in.
func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request, admin bool, userID int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan SSEEvent, clientBufferSize)
	clientID := h.Register(ch, admin, userID)
	defer h.Unregister(clientID)

	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-ch:
			if !ok {
				return
			}

			if event.Type == "heartbeat" {
				// SSE comment — ignored by EventSource, resets proxy timeouts.
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
					h.log.Error("write heartbeat failed", "client_id", clientID, "error", err, "component", "sse")
					return
				}
				flusher.Flush()
				continue
			}

			if event.ID != "" {
				if _, err := w.Write([]byte("id: " + event.ID + "\n")); err != nil {
					h.log.Error("write id failed", "client_id", clientID, "error", err, "component", "sse")
					return
				}
			}
			if event.Type != "" {
				if _, err := w.Write([]byte("event: " + event.Type + "\n")); err != nil {
					h.log.Error("write event failed", "client_id", clientID, "error", err, "component", "sse")
					return
				}
			}
			if len(event.Data) > 0 {
				// Split multi-line data and prefix each line with "data: ".
				lines := splitLines(string(event.Data))
				for _, line := range lines {
					if _, err := w.Write([]byte("data: " + line + "\n")); err != nil {
						h.log.Error("write data failed", "client_id", clientID, "error", err, "component", "sse")
						return
					}
				}
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				h.log.Error("write terminator failed", "client_id", clientID, "error", err, "component", "sse")
				return
			}
			flusher.Flush()
		}
	}
}

// splitLines splits data on newlines so each line can be prefixed with
// "data: " per the SSE specification.
func splitLines(data string) []string {
	if data == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
