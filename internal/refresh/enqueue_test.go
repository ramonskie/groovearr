package refresh

import (
	"context"
	"testing"

	"github.com/ramonskie/groovearr/internal/catalog"
)

func TestEnqueueAlbum(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store, &fakeLibrary{}, Options{})

	if err := svc.EnqueueAlbum(context.Background(), 42, catalog.ReasonEvent, catalog.PriorityHigh); err != nil {
		t.Fatalf("EnqueueAlbum: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.queue) != 1 {
		t.Fatalf("queue size = %d, want 1", len(store.queue))
	}
	for _, it := range store.queue {
		if it.EntityType != catalog.EntityAlbum || it.EntityID != 42 {
			t.Fatalf("row entity = %s/%d, want album/42", it.EntityType, it.EntityID)
		}
		if it.Priority != catalog.PriorityHigh || it.Reason != catalog.ReasonEvent {
			t.Fatalf("row meta = priority %d reason %q, want %d/%q",
				it.Priority, it.Reason, catalog.PriorityHigh, catalog.ReasonEvent)
		}
		if !it.DueAt.Equal(fixedNow) {
			t.Fatalf("DueAt = %v, want %v", it.DueAt, fixedNow)
		}
	}
}

func TestEnqueueArtist(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store, &fakeLibrary{}, Options{})

	if err := svc.EnqueueArtist(context.Background(), 7, catalog.ReasonManual, catalog.PriorityDefault); err != nil {
		t.Fatalf("EnqueueArtist: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.queue) != 1 {
		t.Fatalf("queue size = %d, want 1", len(store.queue))
	}
	for _, it := range store.queue {
		if it.EntityType != catalog.EntityArtist || it.EntityID != 7 {
			t.Fatalf("row entity = %s/%d, want artist/7", it.EntityType, it.EntityID)
		}
	}
}

func TestEnqueueRejectsInvalidID(t *testing.T) {
	svc := newTestService(newFakeStore(), &fakeLibrary{}, Options{})

	if err := svc.EnqueueAlbum(context.Background(), 0, catalog.ReasonEvent, catalog.PriorityLow); err == nil {
		t.Fatal("expected error for album id 0")
	}
	if err := svc.EnqueueArtist(context.Background(), -1, catalog.ReasonEvent, catalog.PriorityLow); err == nil {
		t.Fatal("expected error for artist id -1")
	}
}

func TestEnqueueNilServiceIsNoop(t *testing.T) {
	var svc *Service
	if err := svc.EnqueueAlbum(context.Background(), 1, catalog.ReasonEvent, catalog.PriorityLow); err != nil {
		t.Fatalf("nil service should be a no-op, got %v", err)
	}
	if err := svc.EnqueueArtist(context.Background(), 1, catalog.ReasonEvent, catalog.PriorityLow); err != nil {
		t.Fatalf("nil service should be a no-op, got %v", err)
	}
}

// TestService_ReArmTerminalAlbums covers the engine wrapper: it forwards the
// caller's limit and its injected clock to the store, and a nil service is a
// clear error rather than a panic.
func TestService_ReArmTerminalAlbums(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store, &fakeLibrary{}, Options{})

	n, err := svc.ReArmTerminalAlbums(context.Background(), 7)
	if err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	if n != 0 {
		t.Errorf("re-armed = %d, want 0 (no terminal albums seeded)", n)
	}
	if len(store.rearms) != 1 {
		t.Fatalf("store rearm calls = %d, want 1", len(store.rearms))
	}
	if got := store.rearms[0]; got.limit != 7 || !got.now.Equal(fixedNow) {
		t.Errorf("re-arm call = %+v, want limit 7 and now %v", got, fixedNow)
	}

	var nilSvc *Service
	if _, err := nilSvc.ReArmTerminalAlbums(context.Background(), 0); err == nil {
		t.Error("nil service must return an error, not panic")
	}
}
