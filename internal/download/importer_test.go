package download

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

// mockDownloadStore implements DownloadStore for testing.
type mockDownloadStore struct {
	records map[string]*domain.DownloadRecord
}

func newMockDownloadStore() *mockDownloadStore {
	return &mockDownloadStore{records: make(map[string]*domain.DownloadRecord)}
}

func (m *mockDownloadStore) Insert(ctx context.Context, r *domain.DownloadRecord) error {
	cp := *r
	m.records[r.ID] = &cp
	return nil
}

func (m *mockDownloadStore) Update(ctx context.Context, r *domain.DownloadRecord) error {
	existing, ok := m.records[r.ID]
	if !ok {
		return fmt.Errorf("not found")
	}
	if r.State != "" {
		existing.State = r.State
	}
	if r.FilePath != "" {
		existing.FilePath = r.FilePath
	}
	if r.Error != "" {
		existing.Error = r.Error
	}
	if r.Progress != 0 {
		existing.Progress = r.Progress
	}
	if r.Size != 0 {
		existing.Size = r.Size
	}
	existing.LibraryTrackID = r.LibraryTrackID
	return nil
}

func (m *mockDownloadStore) UpdateProgress(ctx context.Context, id string, state domain.DownloadState, progress float64, size, transferred, speed int64, filePath, coverURL string) error {
	existing, ok := m.records[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	existing.State = state
	existing.Progress = progress
	existing.Size = size
	existing.Transferred = transferred
	existing.Speed = speed
	if filePath != "" {
		existing.FilePath = filePath
	}
	if coverURL != "" {
		existing.CoverURL = coverURL
	}
	return nil
}

func (m *mockDownloadStore) TransitionState(ctx context.Context, id string, oldState, newState domain.DownloadState) (bool, error) {
	r, ok := m.records[id]
	if !ok || r.State != oldState {
		return false, nil
	}
	r.State = newState
	return true, nil
}

func (m *mockDownloadStore) Get(ctx context.Context, id string) (*domain.DownloadRecord, error) {
	r, ok := m.records[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (m *mockDownloadStore) List(ctx context.Context) ([]domain.DownloadRecord, error) {
	var out []domain.DownloadRecord
	for _, r := range m.records {
		out = append(out, *r)
	}
	return out, nil
}

func (m *mockDownloadStore) ListByState(ctx context.Context, state domain.DownloadState) ([]domain.DownloadRecord, error) {
	var out []domain.DownloadRecord
	for _, r := range m.records {
		if r.State == state {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockDownloadStore) ListActive(ctx context.Context) ([]domain.DownloadRecord, error) {
	var out []domain.DownloadRecord
	for _, r := range m.records {
		if !r.State.Terminal() {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockDownloadStore) ListByPlaylist(ctx context.Context, playlistID string) ([]domain.DownloadRecord, error) {
	var out []domain.DownloadRecord
	for _, r := range m.records {
		if r.PlaylistID == playlistID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockDownloadStore) RecordEvent(ctx context.Context, e *domain.DownloadEvent) error {
	return nil
}

func (m *mockDownloadStore) GetEvents(ctx context.Context, downloadID string) ([]domain.DownloadEvent, error) {
	return nil, nil
}

func (m *mockDownloadStore) DeleteTerminal(ctx context.Context) error {
	return nil
}

func (m *mockDownloadStore) Close() error { return nil }

func (m *mockDownloadStore) FindActiveByTitle(ctx context.Context, artist, title string) (*domain.DownloadRecord, error) {
	for _, r := range m.records {
		if r.Artist == artist && r.Title == title && !r.State.Terminal() {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

// trackingBus records published events for assertion in tests.
type trackingBus struct {
	events []trackedEvent
}

type trackedEvent struct {
	topic string
	event any
}

func newTrackingBus() *trackingBus {
	return &trackingBus{}
}

func (b *trackingBus) Subscribe(topic string, handler events.EventHandler) {}

func (b *trackingBus) Unsubscribe(topic string, handler events.EventHandler) {}

func (b *trackingBus) Publish(ctx context.Context, topic string, event any) {
	b.events = append(b.events, trackedEvent{topic: topic, event: event})
}

func (b *trackingBus) lastEvent() *trackedEvent {
	if len(b.events) == 0 {
		return nil
	}
	return &b.events[len(b.events)-1]
}

// successHandler always succeeds.
type successHandler struct{ name string }

func (h *successHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	return nil
}

// failHandler always fails.
type failHandler struct{ msg string }

func (h *failHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	return errors.New(h.msg)
}

func TestCompletedDownloadService_SuccessChain(t *testing.T) {
	store := newMockDownloadStore()
	bus := newTrackingBus()

	record := &domain.DownloadRecord{
		ID:    "test-dl-1",
		State: domain.DownloadImportPending,
	}
	store.Insert(context.Background(), record)

	h1 := &successHandler{name: "h1"}
	h2 := &successHandler{name: "h2"}

	var svc CompletedDownloadService
	svc.store = store
	svc.bus = bus
	svc.log = testLogger()
	svc.handlers = []ImportHandler{h1, h2}
	svc.onDownloadCompleted(context.Background(), &domain.DownloadRecord{ID: "test-dl-1"})

	// Verify state transition.
	got, _ := store.Get(context.Background(), "test-dl-1")
	if got.State != domain.DownloadImported {
		t.Errorf("expected imported, got %s", got.State)
	}

	// Verify import completed event was published.
	last := bus.lastEvent()
	if last == nil || last.topic != events.TopicImportCompleted {
		t.Errorf("expected TopicImportCompleted, got %v", last)
	}
}

func TestCompletedDownloadService_FailureStopsChain(t *testing.T) {
	store := newMockDownloadStore()
	bus := newTrackingBus()

	record := &domain.DownloadRecord{
		ID:    "test-dl-2",
		State: domain.DownloadImportPending,
	}
	store.Insert(context.Background(), record)

	h1 := &successHandler{name: "h1"}
	h2 := &failHandler{msg: "handler 2 failed"}
	h3 := &successHandler{name: "h3"} // should never run

	var svc CompletedDownloadService
	svc.store = store
	svc.bus = bus
	svc.log = testLogger()
	svc.handlers = []ImportHandler{h1, h2, h3}
	svc.onDownloadCompleted(context.Background(), &domain.DownloadRecord{ID: "test-dl-2"})

	got, _ := store.Get(context.Background(), "test-dl-2")
	if got.State != domain.DownloadFailed {
		t.Errorf("expected failed, got %s", got.State)
	}
	if got.Error == "" {
		t.Error("expected error message on record")
	}

	last := bus.lastEvent()
	if last == nil || last.topic != events.TopicImportFailed {
		t.Errorf("expected TopicImportFailed, got %v", last)
	}
}

func TestCompletedDownloadService_SkipsNonImportPending(t *testing.T) {
	store := newMockDownloadStore()
	bus := newTrackingBus()

	record := &domain.DownloadRecord{
		ID:    "test-dl-3",
		State: domain.DownloadQueued,
	}
	store.Insert(context.Background(), record)

	var svc CompletedDownloadService
	svc.store = store
	svc.bus = bus
	svc.log = testLogger()
	svc.handlers = []ImportHandler{&successHandler{name: "h1"}}
	svc.onDownloadCompleted(context.Background(), &domain.DownloadRecord{ID: "test-dl-3"})

	// State should remain unchanged.
	got, _ := store.Get(context.Background(), "test-dl-3")
	if got.State != domain.DownloadQueued {
		t.Errorf("expected queued, got %s", got.State)
	}
}
