package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/sse"
	"github.com/ramonskie/groovearr/internal/user"
)

// syncRecorder is a concurrency-safe http.ResponseWriter + http.Flusher so a
// test can read what an SSE handler has streamed while the handler is still
// running. httptest.ResponseRecorder is not safe to read from the test
// goroutine while the handler goroutine writes to it.
type syncRecorder struct {
	mu   sync.Mutex
	code int
	hdr  http.Header
	buf  bytes.Buffer
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{code: http.StatusOK, hdr: make(http.Header)}
}

func (r *syncRecorder) Header() http.Header { return r.hdr }

func (r *syncRecorder) WriteHeader(code int) {
	r.mu.Lock()
	r.code = code
	r.mu.Unlock()
}

func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(b)
}

// Flush satisfies http.Flusher; the SSE hub rejects a writer without it with a
// 500, so the test doubles must implement it.
func (r *syncRecorder) Flush() {}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// TestHandleEventsRoleWiring is the R3-M1 regression: handleEvents must resolve
// the caller's role from the withAuth Identity and register the SSE subscriber
// with exactly that admin flag. A regular user MUST NOT register an admin
// subscriber (which would leak log_line/job_* events).
//
// The hub's subscriber channel is FIFO, so the test broadcasts a shared event,
// then an admin-only event, then a sentinel shared event. Reading the stream
// only after the sentinel arrives makes the "admin event absent" assertion
// deterministic: the admin event was already delivered-or-withheld before the
// sentinel was written.
func TestHandleEventsRoleWiring(t *testing.T) {
	tests := []struct {
		name      string
		role      user.Role
		userID    int64
		wantAdmin bool
	}{
		{name: "user identity registers non-admin subscriber", role: user.RoleUser, userID: 2, wantAdmin: false},
		{name: "admin identity registers admin subscriber", role: user.RoleAdmin, userID: 1, wantAdmin: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := sse.NewSSEHub(testAPILogger())
			t.Cleanup(hub.Shutdown)
			srv := &Server{log: testAPILogger(), sseHub: hub}

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			req := httptest.NewRequest(http.MethodGet, "/api/events", nil).
				WithContext(contextWithIdentity(ctx, Identity{
					UserID: tt.userID, Username: "caller", Role: tt.role,
				}))
			rec := newSyncRecorder()

			done := make(chan struct{})
			go func() {
				srv.handleEvents(rec, req)
				close(done)
			}()

			waitForClientCount(t, hub, 1)

			hub.Broadcast(sse.SSEEvent{ID: "shared", Type: "download_progress"})
			hub.Broadcast(sse.SSEEvent{ID: "admin-only", Type: "job_progress"})
			hub.Broadcast(sse.SSEEvent{ID: "sentinel", Type: "download_completed"})

			waitForBody(t, rec, "event: download_completed")

			body := rec.body()
			if !strings.Contains(body, "event: download_progress") {
				t.Errorf("shared event missing from stream: %q", body)
			}
			if got := strings.Contains(body, "event: job_progress"); got != tt.wantAdmin {
				t.Errorf("admin-only event delivered = %v, want %v (body=%q)", got, tt.wantAdmin, body)
			}

			cancel()
			waitForDone(t, done)
		})
	}
}

// TestHandleEventsRegistersUserID proves handleEvents forwards the caller's
// account id to the hub: UnregisterByUserID (the demotion/disable teardown
// path) must close this stream. If handleEvents registered userID 0, the stream
// would stay open and this test would time out.
func TestHandleEventsRegistersUserID(t *testing.T) {
	hub := sse.NewSSEHub(testAPILogger())
	t.Cleanup(hub.Shutdown)
	srv := &Server{log: testAPILogger(), sseHub: hub}

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).
		WithContext(contextWithIdentity(context.Background(), Identity{
			UserID: 42, Username: "carol", Role: user.RoleUser,
		}))
	rec := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		srv.handleEvents(rec, req)
		close(done)
	}()

	waitForClientCount(t, hub, 1)

	// Tear down by account id; the stream must close without cancelling the
	// request context.
	hub.UnregisterByUserID(42)
	waitForDone(t, done)
}

// waitForClientCount blocks until the hub has exactly want subscribers.
func waitForClientCount(t *testing.T, hub *sse.SSEHub, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ClientCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("hub client count = %d, want %d", hub.ClientCount(), want)
}

// waitForBody blocks until the recorder's streamed body contains substr.
func waitForBody(t *testing.T, rec *syncRecorder, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.body(), substr) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("streamed body never contained %q; body=%q", substr, rec.body())
}

// waitForDone blocks until the handler goroutine signals completion.
func waitForDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleEvents did not return")
	}
}
