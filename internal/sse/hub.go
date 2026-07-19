// Package sse provides a Server-Sent Events hub for pushing real-time
// download pipeline progress to connected web clients.
package sse

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
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
	clients map[int64]chan SSEEvent
	nextID  atomic.Int64
}

// NewSSEHub creates a ready-to-use SSEHub.
func NewSSEHub() *SSEHub {
	return &SSEHub{
		clients: make(map[int64]chan SSEEvent),
	}
}

// Register adds a client channel and returns the assigned client ID.
// The caller is responsible for reading from the channel and calling
// Unregister when the client disconnects.
func (h *SSEHub) Register(client chan SSEEvent) int64 {
	id := h.nextID.Add(1)

	h.mu.Lock()
	h.clients[id] = client
	h.mu.Unlock()

	return id
}

// Unregister removes a client and closes its channel. It is safe to call
// multiple times for the same client; subsequent calls are no-ops.
func (h *SSEHub) Unregister(clientID int64) {
	h.mu.Lock()
	ch, ok := h.clients[clientID]
	if ok {
		delete(h.clients, clientID)
	}
	h.mu.Unlock()

	if ok {
		close(ch)
	}
}

// Broadcast sends an event to every registered client. Sends are non-blocking:
// if a client's buffer is full the event is silently dropped for that client.
func (h *SSEHub) Broadcast(event SSEEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, ch := range h.clients {
		select {
		case ch <- event:
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

// ServeHTTP implements http.Handler for SSE connections.
//
// It sets the required SSE response headers, registers the client's channel,
// and streams events until the request context is cancelled (client
// disconnects). Each SSEEvent is formatted according to the SSE protocol:
//
//	id: <id>
//	event: <type>
//	data: <data>
//
// Heartbeat events are written as SSE comments (": keepalive\n\n").
func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	clientID := h.Register(ch)
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
					log.Printf("sse: write heartbeat for client %d: %v", clientID, err)
					return
				}
				flusher.Flush()
				continue
			}

			if event.ID != "" {
				if _, err := w.Write([]byte("id: " + event.ID + "\n")); err != nil {
					log.Printf("sse: write id for client %d: %v", clientID, err)
					return
				}
			}
			if event.Type != "" {
				if _, err := w.Write([]byte("event: " + event.Type + "\n")); err != nil {
					log.Printf("sse: write event for client %d: %v", clientID, err)
					return
				}
			}
			if len(event.Data) > 0 {
				// Split multi-line data and prefix each line with "data: ".
				lines := splitLines(string(event.Data))
				for _, line := range lines {
					if _, err := w.Write([]byte("data: " + line + "\n")); err != nil {
						log.Printf("sse: write data for client %d: %v", clientID, err)
						return
					}
				}
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				log.Printf("sse: write terminator for client %d: %v", clientID, err)
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
