package download

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ramonskie/groovearr/internal/events"
)

// mockDownloadStore implements Store for testing.
type mockDownloadStore struct {
	records map[string]*Record
}

func newMockDownloadStore() *mockDownloadStore {
	return &mockDownloadStore{records: make(map[string]*Record)}
}

func (m *mockDownloadStore) Insert(ctx context.Context, r *Record) error {
	cp := *r
	m.records[r.ID] = &cp
	return nil
}

func (m *mockDownloadStore) Update(ctx context.Context, r *Record) error {
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

func (m *mockDownloadStore) UpdateProgress(ctx context.Context, id string, state State, progress float64, size, transferred, speed int64, filePath, coverURL string) error {
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

func (m *mockDownloadStore) TransitionState(ctx context.Context, id string, oldState, newState State) (bool, error) {
	r, ok := m.records[id]
	if !ok || r.State != oldState {
		return false, nil
	}
	r.State = newState
	return true, nil
}

func (m *mockDownloadStore) Get(ctx context.Context, id string) (*Record, error) {
	r, ok := m.records[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (m *mockDownloadStore) List(ctx context.Context) ([]Record, error) {
	var out []Record
	for _, r := range m.records {
		out = append(out, *r)
	}
	return out, nil
}

func (m *mockDownloadStore) ListByState(ctx context.Context, state State) ([]Record, error) {
	var out []Record
	for _, r := range m.records {
		if r.State == state {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockDownloadStore) ListActive(ctx context.Context) ([]Record, error) {
	var out []Record
	for _, r := range m.records {
		if !r.State.Terminal() {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockDownloadStore) ListByPlaylist(ctx context.Context, playlistID string) ([]Record, error) {
	var out []Record
	for _, r := range m.records {
		if r.PlaylistID == playlistID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockDownloadStore) RecordEvent(ctx context.Context, e *Event) error {
	return nil
}

func (m *mockDownloadStore) GetEvents(ctx context.Context, downloadID string) ([]Event, error) {
	return nil, nil
}

func (m *mockDownloadStore) DeleteTerminal(ctx context.Context) error {
	return nil
}

func (m *mockDownloadStore) Close() error { return nil }

func (m *mockDownloadStore) FindActiveByTitle(ctx context.Context, artist, title string) (*Record, error) {
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

func (h *successHandler) Handle(ctx context.Context, record *Record) error {
	return nil
}

// failHandler always fails.
type failHandler struct{ msg string }

func (h *failHandler) Handle(ctx context.Context, record *Record) error {
	return errors.New(h.msg)
}

func TestCompletedDownloadService_SuccessChain(t *testing.T) {
	store := newMockDownloadStore()
	bus := newTrackingBus()

	record := &Record{
		ID:    "test-dl-1",
		State: StateImportPending,
	}
	store.Insert(context.Background(), record)

	h1 := &successHandler{name: "h1"}
	h2 := &successHandler{name: "h2"}

	var svc CompletedDownloadService
	svc.store = store
	svc.bus = bus
	svc.log = testLogger()
	svc.handlers = []ImportHandler{h1, h2}
	svc.onDownloadCompleted(context.Background(), &Record{ID: "test-dl-1"})

	// Verify state transition.
	got, _ := store.Get(context.Background(), "test-dl-1")
	if got.State != StateImported {
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

	record := &Record{
		ID:    "test-dl-2",
		State: StateImportPending,
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
	svc.onDownloadCompleted(context.Background(), &Record{ID: "test-dl-2"})

	got, _ := store.Get(context.Background(), "test-dl-2")
	if got.State != StateFailed {
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

	record := &Record{
		ID:    "test-dl-3",
		State: StateQueued,
	}
	store.Insert(context.Background(), record)

	var svc CompletedDownloadService
	svc.store = store
	svc.bus = bus
	svc.log = testLogger()
	svc.handlers = []ImportHandler{&successHandler{name: "h1"}}
	svc.onDownloadCompleted(context.Background(), &Record{ID: "test-dl-3"})

	// State should remain unchanged.
	got, _ := store.Get(context.Background(), "test-dl-3")
	if got.State != StateQueued {
		t.Errorf("expected queued, got %s", got.State)
	}
}
