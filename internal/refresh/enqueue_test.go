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
