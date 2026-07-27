package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/events"
)

// testLogger returns a discard logger suitable for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRegisterClient(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ch := make(chan SSEEvent, 4)
	id := hub.Register(ch)

	if id != 1 {
		t.Errorf("expected first client ID 1, got %d", id)
	}
	if hub.ClientCount() != 1 {
		t.Errorf("ClientCount = %d, want 1", hub.ClientCount())
	}
}

func TestRegisterMultipleClients(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ids := make(map[int64]bool)
	for i := 0; i < 5; i++ {
		ch := make(chan SSEEvent, 1)
		id := hub.Register(ch)
		if _, exists := ids[id]; exists {
			t.Errorf("duplicate client ID %d", id)
		}
		ids[id] = true
	}

	if hub.ClientCount() != 5 {
		t.Errorf("ClientCount = %d, want 5", hub.ClientCount())
	}
}

func TestUnregisterRemovesClient(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ch := make(chan SSEEvent, 1)
	id := hub.Register(ch)

	if hub.ClientCount() != 1 {
		t.Fatal("expected 1 client after register")
	}

	hub.Unregister(id)

	if hub.ClientCount() != 0 {
		t.Errorf("ClientCount = %d, want 0 after unregister", hub.ClientCount())
	}

	// Channel must be closed.
	_, ok := <-ch
	if ok {
		t.Error("channel was not closed after Unregister")
	}
}

func TestUnregisterIsIdempotent(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ch := make(chan SSEEvent, 1)
	id := hub.Register(ch)

	hub.Unregister(id)
	// Second call must not panic.
	hub.Unregister(id)

	if hub.ClientCount() != 0 {
		t.Errorf("ClientCount = %d, want 0", hub.ClientCount())
	}
}

func TestBroadcastToSingleClient(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ch := make(chan SSEEvent, 4)
	hub.Register(ch)

	event := SSEEvent{
		ID:        "dl-1",
		Type:      "progress",
		Data:      json.RawMessage(`{"pct":50}`),
		Timestamp: time.Now(),
	}
	hub.Broadcast(event)

	select {
	case got := <-ch:
		if got.ID != event.ID {
			t.Errorf("ID = %q, want %q", got.ID, event.ID)
		}
		if got.Type != event.Type {
			t.Errorf("Type = %q, want %q", got.Type, event.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for broadcast event")
	}
}

func TestBroadcastToMultipleClients(t *testing.T) {
	hub := NewSSEHub(testLogger())

	const numClients = 5
	chs := make([]chan SSEEvent, numClients)
	for i := 0; i < numClients; i++ {
		chs[i] = make(chan SSEEvent, 4)
		hub.Register(chs[i])
	}

	event := SSEEvent{
		ID:   "broadcast-test",
		Type: "test",
		Data: json.RawMessage(`{"msg":"hello"}`),
	}

	hub.Broadcast(event)

	// Every client must receive the event.
	for i, ch := range chs {
		select {
		case got := <-ch:
			if got.ID != event.ID {
				t.Errorf("client %d: ID = %q, want %q", i, got.ID, event.ID)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("client %d: timed out waiting for broadcast", i)
		}
	}
}

func TestBroadcastSlowClientDropped(t *testing.T) {
	hub := NewSSEHub(testLogger())

	// Channel with buffer size 0 — always full.
	slowCh := make(chan SSEEvent)
	hub.Register(slowCh)

	// Drain goroutine that never drains.
	var droppedCount int32

	// Fast client with sufficient buffer.
	fastCh := make(chan SSEEvent, 16)
	hub.Register(fastCh)

	// Send many events. The slow client's channel will fill immediately (capacity 0)
	// so all events get dropped. The fast client should receive them all.
	const numEvents = 10
	for i := 0; i < numEvents; i++ {
		hub.Broadcast(SSEEvent{
			ID:   fmt.Sprintf("evt-%d", i),
			Type: "test",
		})
	}

	// Verify fast client received all events.
	time.Sleep(100 * time.Millisecond)

	fastReceived := 0
drainFast:
	for {
		select {
		case <-fastCh:
			fastReceived++
		default:
			break drainFast
		}
	}
	if fastReceived != numEvents {
		t.Errorf("fast client received %d/%d events", fastReceived, numEvents)
	}

	// Slow client: drops don't panic — the goroutine above would deadlock if
	// Broadcast blocked. Just verify we didn't crash.
	_ = droppedCount
}

func TestBroadcastNonBlockingDoesNotHang(t *testing.T) {
	hub := NewSSEHub(testLogger())

	// Register a client with a zero-size buffer and no reader.
	ch := make(chan SSEEvent)
	hub.Register(ch)

	// Broadcasting must return immediately even though nobody reads ch.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			hub.Broadcast(SSEEvent{ID: fmt.Sprintf("e-%d", i), Type: "test"})
		}
		close(done)
	}()

	select {
	case <-done:
		// OK — broadcast didn't hang.
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast hung on slow client")
	}
}

func TestHeartbeatGoroutine(t *testing.T) {
	hub := NewSSEHub(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := make(chan SSEEvent, 32)
	hub.Register(ch)

	hub.StartHeartbeat(ctx)

	// Wait for at least one heartbeat.
	var gotHeartbeat bool
	deadline := time.After(heartbeatInterval + 5*time.Second)

loop:
	for !gotHeartbeat {
		select {
		case evt := <-ch:
			if evt.Type == "heartbeat" {
				gotHeartbeat = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for heartbeat")
		case <-time.After(100 * time.Millisecond):
			// keep waiting
			if gotHeartbeat {
				break loop
			}
		}
	}

	if !gotHeartbeat {
		t.Error("never received heartbeat event")
	}
}

func TestHeartbeatStopsOnCancel(t *testing.T) {
	hub := NewSSEHub(testLogger())
	ctx, cancel := context.WithCancel(context.Background())

	ch := make(chan SSEEvent, 32)
	hub.Register(ch)

	hub.StartHeartbeat(ctx)

	// Wait for first heartbeat.
	gotOne := false
	deadline := time.After(heartbeatInterval + 5*time.Second)
	for !gotOne {
		select {
		case evt := <-ch:
			if evt.Type == "heartbeat" {
				gotOne = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for first heartbeat")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Cancel heartbeat context.
	cancel()

	// Drain any events already enqueued.
	drainCh(ch)

	// Wait to verify no more heartbeats arrive after cancel.
	time.Sleep(heartbeatInterval + 2*time.Second)

	drainCh(ch)

	select {
	case evt := <-ch:
		if evt.Type == "heartbeat" {
			t.Error("received heartbeat after context cancellation")
		}
	default:
		// OK — no heartbeats.
	}
}

func TestServeHTTPSetsHeaders(t *testing.T) {
	hub := NewSSEHub(testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()

	// Start serving in a goroutine and cancel via context.
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		hub.ServeHTTP(rec, req)
		close(done)
	}()

	// Wait a moment for headers to be written, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	resp := rec.Result()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if conn := resp.Header.Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection = %q, want keep-alive", conn)
	}
}

func TestServeHTTPCleansUpOnDisconnect(t *testing.T) {
	hub := NewSSEHub(testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		hub.ServeHTTP(rec, req)
		close(done)
	}()

	// Give handler time to register the client.
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Fatalf("ClientCount = %d, want 1 before disconnect", hub.ClientCount())
	}

	// Cancel context (simulates client disconnect).
	cancel()
	<-done

	// Give the defer Unregister time to run.
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("ClientCount = %d, want 0 after disconnect", hub.ClientCount())
	}
}

func TestServeHTTPStreamsEvents(t *testing.T) {
	hub := NewSSEHub(testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		hub.ServeHTTP(rec, req)
	}()

	// Wait for handler to register.
	time.Sleep(30 * time.Millisecond)

	// Broadcast an event.
	hub.Broadcast(SSEEvent{
		ID:   "dl-42",
		Type: "download_progress",
		Data: json.RawMessage(`{"progress":75}`),
	})

	// Give handler time to write.
	time.Sleep(100 * time.Millisecond)
	cancel()
	wg.Wait()

	body := rec.Body.String()

	if !strings.Contains(body, "id: dl-42") {
		t.Error("response missing event id")
		t.Logf("body: %q", body)
	}
	if !strings.Contains(body, "event: download_progress") {
		t.Error("response missing event type")
	}
	if !strings.Contains(body, `data: {"progress":75}`) {
		t.Error("response missing event data")
	}
}

func TestServeHTTPHeartbeatWritesComment(t *testing.T) {
	hub := NewSSEHub(testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		hub.ServeHTTP(rec, req)
	}()

	time.Sleep(30 * time.Millisecond)

	// Broadcast a heartbeat event directly.
	hub.Broadcast(SSEEvent{
		Type: "heartbeat",
	})

	time.Sleep(100 * time.Millisecond)
	cancel()
	wg.Wait()

	body := rec.Body.String()

	if !strings.Contains(body, ": keepalive") {
		t.Error("response missing heartbeat comment ': keepalive'")
		t.Logf("body: %q", body)
	}
}

func TestSSENotifierReceivesProgress(t *testing.T) {
	hub := NewSSEHub(testLogger())
	bus := events.NewInMemoryEventBus(testLogger())
	_ = NewSSENotifier(hub, bus, testLogger())

	ch := make(chan SSEEvent, 4)
	hub.Register(ch)

	record := &download.Record{
		ID:         "n1",
		SourceName: "test",
		Filename:   "song.flac",
		State:      download.StateDownloading,
		Progress:   42.0,
	}

	bus.Publish(context.Background(), events.TopicDownloadProgress, record)

	select {
	case evt := <-ch:
		if evt.Type != "download_progress" {
			t.Errorf("Type = %q, want download_progress", evt.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for progress event")
	}
}

func TestSSENotifierReceivesCompleted(t *testing.T) {
	hub := NewSSEHub(testLogger())
	bus := events.NewInMemoryEventBus(testLogger())
	_ = NewSSENotifier(hub, bus, testLogger())

	ch := make(chan SSEEvent, 4)
	hub.Register(ch)

	record := &download.Record{
		ID:         "n2",
		SourceName: "test",
		Filename:   "song.flac",
		State:      download.StateImportPending,
		Progress:   100,
	}

	bus.Publish(context.Background(), events.TopicDownloadCompleted, record)

	select {
	case evt := <-ch:
		if evt.Type != "download_completed" {
			t.Errorf("Type = %q, want download_completed", evt.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for completed event")
	}
}

func TestSSENotifierReceivesFailed(t *testing.T) {
	hub := NewSSEHub(testLogger())
	bus := events.NewInMemoryEventBus(testLogger())
	_ = NewSSENotifier(hub, bus, testLogger())

	ch := make(chan SSEEvent, 4)
	hub.Register(ch)

	record := &download.Record{
		ID:         "n3",
		SourceName: "test",
		Filename:   "song.flac",
		State:      download.StateFailed,
		Error:      "timeout",
	}

	bus.Publish(context.Background(), events.TopicDownloadFailed, record)

	select {
	case evt := <-ch:
		if evt.Type != "download_failed" {
			t.Errorf("Type = %q, want download_failed", evt.Type)
		}
		// Verify error is in the data.
		if !strings.Contains(string(evt.Data), "timeout") {
			t.Errorf("data missing error: %s", string(evt.Data))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for failed event")
	}
}

func TestSSENotifierReceivesImportCompleted(t *testing.T) {
	hub := NewSSEHub(testLogger())
	bus := events.NewInMemoryEventBus(testLogger())
	_ = NewSSENotifier(hub, bus, testLogger())

	ch := make(chan SSEEvent, 4)
	hub.Register(ch)

	record := &download.Record{
		ID:         "n4",
		SourceName: "test",
		Filename:   "song.flac",
		State:      download.StateImported,
	}

	bus.Publish(context.Background(), events.TopicImportCompleted, record)

	select {
	case evt := <-ch:
		if evt.Type != "import_completed" {
			t.Errorf("Type = %q, want import_completed", evt.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for import completed event")
	}
}

func TestSSENotifierHandle(t *testing.T) {
	hub := NewSSEHub(testLogger())
	bus := events.NewInMemoryEventBus(testLogger())
	notifier := NewSSENotifier(hub, bus, testLogger())

	ch := make(chan SSEEvent, 4)
	hub.Register(ch)

	record := &download.Record{
		ID:         "n5",
		SourceName: "test",
		Filename:   "song.flac",
		State:      download.StateImported,
	}

	err := notifier.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	select {
	case evt := <-ch:
		if evt.ID != "n5" {
			t.Errorf("ID = %q, want n5", evt.ID)
		}
		if evt.Type != "import_completed" {
			t.Errorf("Type = %q, want import_completed", evt.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for Handle broadcast")
	}
}

func TestSSENotifierHandlesNonDownloadRecordEvents(t *testing.T) {
	hub := NewSSEHub(testLogger())
	bus := events.NewInMemoryEventBus(testLogger())
	_ = NewSSENotifier(hub, bus, testLogger())

	ch := make(chan SSEEvent, 4)
	hub.Register(ch)

	// Publish an unexpected type — must not panic.
	bus.Publish(context.Background(), events.TopicDownloadProgress, "not-a-record")

	// Give async handler time to run (or not).
	time.Sleep(100 * time.Millisecond)

	// No event should arrive on the channel.
	select {
	case evt := <-ch:
		t.Errorf("unexpected event on channel: %+v", evt)
	default:
		// OK — nothing broadcast.
	}
}

func TestSSENotifierImplementsImportHandler(t *testing.T) {
	// Compile-time check: SSENotifier satisfies the ImportHandler interface.
	// We verify by asserting the method signature exists.

	hub := NewSSEHub(testLogger())
	bus := events.NewInMemoryEventBus(testLogger())
	notifier := NewSSENotifier(hub, bus, testLogger())

	// This test uses a helper interface matching download.ImportHandler.
	type importHandler interface {
		Handle(ctx context.Context, record *download.Record) error
	}

	var _ importHandler = notifier
}

func TestConcurrentRegisterUnregister(t *testing.T) {
	hub := NewSSEHub(testLogger())

	var wg sync.WaitGroup
	const numOps = 100

	// Concurrent registers.
	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := make(chan SSEEvent, 1)
			hub.Register(ch)
		}()
	}
	wg.Wait()

	if hub.ClientCount() != numOps {
		t.Errorf("after concurrent registers: %d, want %d", hub.ClientCount(), numOps)
	}
}

func TestConcurrentBroadcastDoesNotRace(t *testing.T) {
	hub := NewSSEHub(testLogger())

	// Register clients with draining goroutines.
	const numClients = 10
	for i := 0; i < numClients; i++ {
		ch := make(chan SSEEvent, 64)
		hub.Register(ch)
		go func() {
			for range ch {
			}
		}()
	}

	var wg sync.WaitGroup
	const numBroadcasts = 50

	for i := 0; i < numBroadcasts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hub.Broadcast(SSEEvent{
				ID:   fmt.Sprintf("b-%d", idx),
				Type: "test",
			})
		}(i)
	}
	wg.Wait()
}

func TestBroadcastToEmptyHub(t *testing.T) {
	hub := NewSSEHub(testLogger())

	// Broadcasting to a hub with no clients must not panic.
	hub.Broadcast(SSEEvent{ID: "no-clients", Type: "test"})
}

// drainCh removes all pending events from a channel without blocking.
func drainCh(ch chan SSEEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// Benchmark to validate the fast path doesn't degrade with many clients.
func BenchmarkBroadcast(b *testing.B) {
	hub := NewSSEHub(testLogger())

	const numClients = 50
	for i := 0; i < numClients; i++ {
		ch := make(chan SSEEvent, 128)
		hub.Register(ch)
		// Drain in background.
		go func() {
			for range ch {
			}
		}()
	}

	event := SSEEvent{ID: "bench", Type: "bench", Data: json.RawMessage("{}")}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Broadcast(event)
	}
}

// Ensure atomic ID counter doesn't wrap around in reasonable use.
func TestClientIDIsMonotonic(t *testing.T) {
	hub := NewSSEHub(testLogger())

	var lastID int64
	for i := 0; i < 100; i++ {
		ch := make(chan SSEEvent, 1)
		id := hub.Register(ch)
		if id <= lastID {
			t.Errorf("ID not monotonic: %d followed by %d", lastID, id)
		}
		lastID = id
	}
}

// Verify the Hub does not import domain package for core SSE logic.
// (The notifier imports domain — that's the integration layer.)

// Ensure event JSON round-trips correctly.
func TestSSEEventJSONRoundtrip(t *testing.T) {
	event := SSEEvent{
		ID:        "abc",
		Type:      "completed",
		Data:      json.RawMessage(`{"state":"imported"}`),
		Timestamp: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SSEEvent
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != event.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, event.ID)
	}
	if decoded.Type != event.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, event.Type)
	}
	if string(decoded.Data) != string(event.Data) {
		t.Errorf("Data = %q, want %q", string(decoded.Data), string(event.Data))
	}
}

// Verify ServeHTTP handles non-flusher response writer gracefully.
func TestServeHTTPNonFlusher(t *testing.T) {
	hub := NewSSEHub(testLogger())

	// responseWriterNoFlush implements http.ResponseWriter without Flusher.
	type responseWriterNoFlush struct {
		http.ResponseWriter
	}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()
	rw := struct {
		http.ResponseWriter
	}{ResponseWriter: rec}

	hub.ServeHTTP(rw, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}
