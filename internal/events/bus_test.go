package events

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPublishToMultipleSubscribers(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	var (
		mu    sync.Mutex
		got   []string
		wg    sync.WaitGroup
		count int32
	)

	makeHandler := func(name string) EventHandler {
		return func(ctx context.Context, event any) {
			mu.Lock()
			got = append(got, name)
			mu.Unlock()
			atomic.AddInt32(&count, 1)
		}
	}

	h1 := makeHandler("h1")
	h2 := makeHandler("h2")
	h3 := makeHandler("h3")

	bus.Subscribe("test", h1)
	bus.Subscribe("test", h2)
	bus.Subscribe("test", h3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		bus.Publish(context.Background(), "test", "hello")
	}()

	// Wait for async handlers to complete.
	assertEventually(t, func() bool { return atomic.LoadInt32(&count) == 3 }, 500*time.Millisecond)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Errorf("expected 3 handler calls, got %d", len(got))
	}
}

func TestUnsubscribeRemovesHandler(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	var count int32

	handler := func(ctx context.Context, event any) {
		atomic.AddInt32(&count, 1)
	}

	bus.Subscribe("test", handler)
	bus.Publish(context.Background(), "test", "event")
	assertEventually(t, func() bool { return atomic.LoadInt32(&count) == 1 }, 200*time.Millisecond)
	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("expected 1 call after subscribe, got %d", count)
	}

	bus.Unsubscribe("test", handler)
	atomic.StoreInt32(&count, 0)
	bus.Publish(context.Background(), "test", "event2")

	// Give enough time for any async deliveries.
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&count) != 0 {
		t.Errorf("expected 0 calls after unsubscribe, got %d", count)
	}
}

func TestUnsubscribeNoOpForMissingHandler(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())

	h1 := func(ctx context.Context, event any) {}
	h2 := func(ctx context.Context, event any) {}

	bus.Subscribe("test", h1)
	bus.Unsubscribe("test", h2) // different handler; should be no-op

	if len(bus.handlers["test"]) != 1 {
		t.Fatalf("expected 1 handler after no-op unsubscribe, got %d", len(bus.handlers["test"]))
	}
}

func TestPublishEmptyTopic(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	// Should not panic or block.
	bus.Publish(context.Background(), "nonexistent", "event")
}

func TestHandlerPanicIsolation(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	var count int32

	panicking := func(ctx context.Context, event any) {
		panic("intentional panic for test")
	}

	normal := func(ctx context.Context, event any) {
		atomic.AddInt32(&count, 1)
	}

	bus.Subscribe("test", panicking)
	bus.Subscribe("test", normal)
	bus.Publish(context.Background(), "test", "event")

	assertEventually(t, func() bool { return atomic.LoadInt32(&count) == 1 }, 200*time.Millisecond)
	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("normal handler should have been called once despite panicking handler, got %d", count)
	}
}

func TestUnsubscribeDuringPublish(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	var (
		h1Called int32
		h2Called int32
		wg       sync.WaitGroup
	)

	// h1 unsubscribes h2 when it fires.
	var h2copy EventHandler
	h2 := func(ctx context.Context, event any) {
		atomic.AddInt32(&h2Called, 1)
	}
	h2copy = h2

	h1 := func(ctx context.Context, event any) {
		atomic.AddInt32(&h1Called, 1)
		bus.Unsubscribe("test", h2copy)
	}

	bus.Subscribe("test", h1)
	bus.Subscribe("test", h2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		bus.Publish(context.Background(), "test", "first")
	}()

	assertEventually(t, func() bool { return atomic.LoadInt32(&h1Called) >= 1 }, 200*time.Millisecond)
	wg.Wait()

	// Both h1 and h2 MAY have been called (Publish already grabbed the snapshot).
	// After this, unsubscribe ensures h2 won't be called on next Publish.
	calledBefore := atomic.LoadInt32(&h2Called)

	bus.Publish(context.Background(), "test", "second")
	time.Sleep(100 * time.Millisecond)
	calledAfter := atomic.LoadInt32(&h2Called)

	if calledAfter > calledBefore {
		t.Errorf("h2 should not have been called after unsubscribe, before=%d after=%d", calledBefore, calledAfter)
	}
}

func TestConcurrentSubscribePublish(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	var count int32

	handler := func(ctx context.Context, event any) {
		atomic.AddInt32(&count, 1)
	}

	var wg sync.WaitGroup

	// Concurrent subscribes.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Subscribe("concurrent", handler)
		}()
	}
	wg.Wait()

	// Concurrent publishes.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Publish(context.Background(), "concurrent", "event")
		}()
	}
	wg.Wait()

	// Wait for async handlers to flush.
	assertEventually(t, func() bool { return atomic.LoadInt32(&count) > 0 }, 500*time.Millisecond)
}

func TestConcurrentSubscribeUnsubscribePublish(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	var running int32
	atomic.StoreInt32(&running, 1)

	// Handler that runs while we're manipulating subscriptions.
	slowHandler := func(ctx context.Context, event any) {
		time.Sleep(10 * time.Millisecond)
	}

	bus.Subscribe("mixed", slowHandler)

	var wg sync.WaitGroup

	// Publish in background.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&running) == 1 {
			bus.Publish(context.Background(), "mixed", "event")
		}
	}()

	// Subscribe/unsubscribe rapidly while publish is running.
	for i := 0; i < 50; i++ {
		tmp := func(ctx context.Context, event any) {}
		bus.Subscribe("mixed", tmp)
		bus.Unsubscribe("mixed", tmp)
	}

	atomic.StoreInt32(&running, 0)
	wg.Wait()
}

func TestNewInMemoryEventBus(t *testing.T) {
	bus := NewInMemoryEventBus(testLogger())
	if bus == nil {
		t.Fatal("NewInMemoryEventBus returned nil")
	}
	if bus.handlers == nil {
		t.Fatal("handlers map should be initialized")
	}
}

func TestTopicConstants(t *testing.T) {
	// Verify topic constants are non-empty strings.
	topics := []string{
		TopicDownloadStateChanged,
		TopicDownloadProgress,
		TopicDownloadCompleted,
		TopicDownloadFailed,
		TopicImportStarted,
		TopicImportCompleted,
	}
	for i, topic := range topics {
		if topic == "" {
			t.Errorf("topic constant at index %d is empty", i)
		}
	}
}

func TestIEventAggregatorSatisfied(t *testing.T) {
	// Compile-time check: InMemoryEventBus implements IEventAggregator.
	var _ IEventAggregator = (*InMemoryEventBus)(nil)
}

// assertEventually polls cond every 10ms until it returns true or the timeout
// expires. Fails the test if the condition is never met.
func assertEventually(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("condition not met within %v", timeout)
		case <-ticker.C:
			if cond() {
				return
			}
		}
	}
}
