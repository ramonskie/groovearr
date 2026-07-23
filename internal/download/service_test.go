package download

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

// ─── Mock store ───────────────────────────────────────────────────────

type mockStore struct {
	mu      sync.Mutex
	records map[string]*domain.DownloadRecord
}

func newMockStore() *mockStore {
	return &mockStore{records: make(map[string]*domain.DownloadRecord)}
}

func (m *mockStore) Insert(ctx context.Context, r *domain.DownloadRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.records[r.ID]; exists {
		return fmt.Errorf("duplicate id %q", r.ID)
	}
	cp := *r
	cp.State = domain.DownloadQueued
	m.records[r.ID] = &cp
	return nil
}

func (m *mockStore) Update(ctx context.Context, r *domain.DownloadRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.records[r.ID]
	if !ok {
		return fmt.Errorf("download %q not found", r.ID)
	}
	existing.State = r.State
	existing.Progress = r.Progress
	existing.Size = r.Size
	existing.Transferred = r.Transferred
	existing.Speed = r.Speed
	existing.FilePath = r.FilePath
	existing.Error = r.Error
	return nil
}

func (m *mockStore) UpdateProgress(ctx context.Context, id string, state domain.DownloadState, progress float64, size, transferred, speed int64, filePath, coverURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.records[id]
	if !ok {
		return fmt.Errorf("download %q not found", id)
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

func (m *mockStore) TransitionState(ctx context.Context, id string, oldState, newState domain.DownloadState) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok || r.State != oldState {
		return false, nil
	}
	r.State = newState
	return true, nil
}

func (m *mockStore) Get(ctx context.Context, id string) (*domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (m *mockStore) List(ctx context.Context) ([]domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.DownloadRecord, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, *r)
	}
	return out, nil
}

func (m *mockStore) ListByState(ctx context.Context, state domain.DownloadState) ([]domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DownloadRecord
	for _, r := range m.records {
		if r.State == state {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockStore) ListActive(ctx context.Context) ([]domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DownloadRecord
	for _, r := range m.records {
		if !r.State.Terminal() {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockStore) ListByPlaylist(ctx context.Context, playlistID string) ([]domain.DownloadRecord, error) {
	return nil, nil
}

func (m *mockStore) RecordEvent(ctx context.Context, e *domain.DownloadEvent) error {
	return nil
}

func (m *mockStore) GetEvents(ctx context.Context, downloadID string) ([]domain.DownloadEvent, error) {
	return nil, nil
}

func (m *mockStore) DeleteTerminal(ctx context.Context) error {
	return nil
}

func (m *mockStore) Close() error { return nil }

func (m *mockStore) FindActiveByTitle(ctx context.Context, artist, title string) (*domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records {
		if r.Artist == artist && r.Title == title && !r.State.Terminal() {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *mockStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) { return nil, nil }

// Compile-time check.
var _ DownloadStore = (*mockStore)(nil)

// ─── Mock event bus ───────────────────────────────────────────────────

type mockBus struct {
	mu     sync.Mutex
	events []mockEvent
}

type mockEvent struct {
	Topic string
	Event any
}

func newMockBus() *mockBus {
	return &mockBus{}
}

func (b *mockBus) Subscribe(topic string, handler events.EventHandler) {}

func (b *mockBus) Unsubscribe(topic string, handler events.EventHandler) {}

func (b *mockBus) Publish(ctx context.Context, topic string, event any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, mockEvent{Topic: topic, Event: event})
}

func (b *mockBus) published() []mockEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]mockEvent, len(b.events))
	copy(out, b.events)
	return out
}

// Compile-time check.
var _ events.IEventAggregator = (*mockBus)(nil)

// ─── Mock worker pool ─────────────────────────────────────────────────

type mockWorkerPool struct {
	mu       sync.Mutex
	submits  []string // download IDs
	submitFn func(ctx context.Context, record *domain.DownloadRecord) error
}

func (p *mockWorkerPool) Submit(ctx context.Context, record *domain.DownloadRecord) error {
	p.mu.Lock()
	p.submits = append(p.submits, record.ID)
	p.mu.Unlock()
	if p.submitFn != nil {
		return p.submitFn(ctx, record)
	}
	return nil
}

func (p *mockWorkerPool) Cancel(downloadID string) {}

func (p *mockWorkerPool) Shutdown() {}

func (p *mockWorkerPool) submitted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.submits))
	copy(out, p.submits)
	return out
}

var _ WorkerPool = (*mockWorkerPool)(nil)

// ─── Service tests ────────────────────────────────────────────────────

func TestNewDownloadService(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)
	if svc == nil {
		t.Fatal("NewDownloadService returned nil")
	}
	if svc.store != store {
		t.Error("store not set")
	}
	if svc.bus != bus {
		t.Error("bus not set")
	}
}

func TestSetWorkerPool(t *testing.T) {
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), nil)
	pool := &mockWorkerPool{}
	svc.SetWorkerPool(pool)

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.workerPool != pool {
		t.Error("worker pool not set")
	}
}

func TestQueueCreatesRecord(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)

	meta := DownloadMeta{
		Artist:      "Test Artist",
		Album:       "Test Album",
		Title:       "Test Title",
		TrackNumber: 3,
		DiscNumber:  1,
		Year:        2024,
		TrackID:     "trk-123",
		CoverURL:    "https://example.com/cover.jpg",
		PlaylistID:  "pl-456",
	}

	id, err := svc.Queue(context.Background(), "soulseek", "peer1", "track.flac", 30_000_000, meta)
	if err != nil {
		t.Fatalf("Queue failed: %v", err)
	}
	if id == "" {
		t.Fatal("returned empty id")
	}

	record, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("record not found in store")
	}

	// Verify record fields.
	if record.State != domain.DownloadQueued {
		t.Errorf("state = %q, want %q", record.State, domain.DownloadQueued)
	}
	if record.SourceName != "soulseek" {
		t.Errorf("source = %q, want soulseek", record.SourceName)
	}
	if record.Filename != "track.flac" {
		t.Errorf("filename = %q", record.Filename)
	}
	if record.Size != 30_000_000 {
		t.Errorf("size = %d, want 30000000", record.Size)
	}
	if record.DisplayName != "Test Artist - Test Title" {
		t.Errorf("displayName = %q", record.DisplayName)
	}
	if record.Artist != "Test Artist" {
		t.Errorf("artist = %q", record.Artist)
	}
	if record.Album != "Test Album" {
		t.Errorf("album = %q", record.Album)
	}
	if record.Title != "Test Title" {
		t.Errorf("title = %q", record.Title)
	}
	if record.TrackNumber != 3 {
		t.Errorf("trackNumber = %d", record.TrackNumber)
	}
	if record.DiscNumber != 1 {
		t.Errorf("discNumber = %d", record.DiscNumber)
	}
	if record.Year != 2024 {
		t.Errorf("year = %d", record.Year)
	}
	if record.TrackID != "trk-123" {
		t.Errorf("trackID = %q", record.TrackID)
	}
	if record.CoverURL != "https://example.com/cover.jpg" {
		t.Errorf("coverURL = %q", record.CoverURL)
	}
}

func TestQueueDisplayNameTitleOnly(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)

	meta := DownloadMeta{Title: "Just Title"}
	id, err := svc.Queue(context.Background(), "deezer", "user", "file.mp3", 0, meta)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Get(context.Background(), id)
	if record.DisplayName != "Just Title" {
		t.Errorf("displayName = %q, want 'Just Title'", record.DisplayName)
	}
}

func TestQueueDisplayNameFallback(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)

	id, err := svc.Queue(context.Background(), "deezer", "user", "fallback.mp3", 0, DownloadMeta{})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Get(context.Background(), id)
	if record.DisplayName != "fallback.mp3" {
		t.Errorf("displayName = %q, want 'fallback.mp3'", record.DisplayName)
	}
}

func TestQueueFiresEvent(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)

	_, err := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	if err != nil {
		t.Fatal(err)
	}

	evts := bus.published()
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	if evts[0].Topic != events.TopicDownloadQueued {
		t.Errorf("topic = %q, want %q", evts[0].Topic, events.TopicDownloadQueued)
	}
	record, ok := evts[0].Event.(*domain.DownloadRecord)
	if !ok {
		t.Fatalf("event payload is not *domain.DownloadRecord")
	}
	if record.State != domain.DownloadQueued {
		t.Errorf("event record state = %q", record.State)
	}
}

func TestQueueDispatchesToWorkerPool(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	id, err := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	if err != nil {
		t.Fatal(err)
	}

	submitted := pool.submitted()
	if len(submitted) != 1 {
		t.Fatalf("expected 1 submit, got %d", len(submitted))
	}
	if submitted[0] != id {
		t.Errorf("submitted id = %q, want %q", submitted[0], id)
	}
}

func TestQueueWithoutWorkerPoolOK(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)
	// No SetWorkerPool called — should not panic or error.

	id, err := svc.Queue(context.Background(), "deezer", "user", "f.mp3", 1, DownloadMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}
}

func TestQueueWorkerPoolError(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{
		submitFn: func(ctx context.Context, r *domain.DownloadRecord) error {
			return fmt.Errorf("pool full")
		},
	}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	id, err := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	if err == nil {
		t.Fatal("expected error from worker pool")
	}
	if !strings.Contains(err.Error(), "worker dispatch") {
		t.Errorf("error = %q, want 'worker dispatch'", err)
	}
	if id == "" {
		t.Error("id should still be returned even on dispatch error")
	}
	// Record should still be persisted.
	record, _ := store.Get(context.Background(), id)
	if record == nil {
		t.Fatal("record should be persisted even on dispatch error")
	}
}

func TestGetStatus(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)

	id, err := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 42, DownloadMeta{})
	if err != nil {
		t.Fatal(err)
	}

	record, err := svc.GetStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("expected record")
	}
	if record.ID != id {
		t.Errorf("id mismatch: %q != %q", record.ID, id)
	}
	if record.State != domain.DownloadQueued {
		t.Errorf("state = %q", record.State)
	}
}

func TestGetStatusNonExistent(t *testing.T) {
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), nil)
	record, err := svc.GetStatus(context.Background(), "no-such-id")
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Error("expected nil for non-existent id")
	}
}

func TestList(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)

	_, _ = svc.Queue(context.Background(), "soulseek", "p1", "a.flac", 1, DownloadMeta{})
	_, _ = svc.Queue(context.Background(), "deezer", "u1", "b.mp3", 2, DownloadMeta{})

	records, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

func TestCancelSetsIgnored(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})

	err := svc.Cancel(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	record, _ := store.Get(context.Background(), id)
	if record.State != domain.DownloadIgnored {
		t.Errorf("state = %q, want %q", record.State, domain.DownloadIgnored)
	}
}

func TestCancelFiresStateChangedEvent(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	_ = svc.Cancel(context.Background(), id)

	evts := bus.published()
	// First event: queued, second: stateChanged.
	if len(evts) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(evts))
	}
	stateEvent := evts[1]
	if stateEvent.Topic != events.TopicDownloadStateChanged {
		t.Errorf("topic = %q, want %q", stateEvent.Topic, events.TopicDownloadStateChanged)
	}
	record := stateEvent.Event.(*domain.DownloadRecord)
	if record.State != domain.DownloadIgnored {
		t.Errorf("event record state = %q", record.State)
	}
}

func TestCancelNonExistent(t *testing.T) {
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), nil)
	err := svc.Cancel(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error for non-existent id")
	}
}

func TestCancelAlreadyTerminalIsNoOp(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	// Manually set to terminal in store.
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadImported})

	eventsBefore := len(bus.published())

	err := svc.Cancel(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	// Should not fire a new state-changed event.
	if len(bus.published()) != eventsBefore {
		t.Error("cancel on terminal state should not fire event")
	}

	record, _ := store.Get(context.Background(), id)
	if record.State != domain.DownloadImported {
		t.Errorf("state should not change on terminal record, got %q", record.State)
	}
}

func TestRetryResetsToQueued(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})

	// Manually set to failed in store.
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed, Error: "download error"})

	err := svc.Retry(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	record, _ := store.Get(context.Background(), id)
	if record.State != domain.DownloadQueued {
		t.Errorf("state = %q, want %q", record.State, domain.DownloadQueued)
	}
	if record.Error != "" {
		t.Errorf("error not cleared: %q", record.Error)
	}
	if record.Progress != 0 {
		t.Errorf("progress not reset: %f", record.Progress)
	}
}

func TestRetryFiresStateChangedEvent(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed})

	_ = svc.Retry(context.Background(), id)

	evts := bus.published()
	// First event: queued, second: stateChanged (retry).
	if len(evts) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(evts))
	}
	stateEvent := evts[1]
	if stateEvent.Topic != events.TopicDownloadStateChanged {
		t.Errorf("topic = %q, want %q", stateEvent.Topic, events.TopicDownloadStateChanged)
	}
	record := stateEvent.Event.(*domain.DownloadRecord)
	if record.State != domain.DownloadQueued {
		t.Errorf("event record state = %q", record.State)
	}
}

func TestRetryNonRetryableState(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})

	// Record is in "queued" state — not retryable.
	err := svc.Retry(context.Background(), id)
	if err == nil {
		t.Fatal("expected error when retrying non-failed record")
	}
	if !strings.Contains(err.Error(), "not retryable") {
		t.Errorf("error = %q, want 'not retryable'", err)
	}
}

func TestRetryNonExistent(t *testing.T) {
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), nil)
	err := svc.Retry(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error for non-existent id")
	}
}

func TestRetryDispatchesToWorkerPool(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	// Queue already submitted once.
	if len(pool.submitted()) != 1 {
		t.Fatalf("expected 1 initial submit, got %d", len(pool.submitted()))
	}

	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed})
	_ = svc.Retry(context.Background(), id)

	submits := pool.submitted()
	if len(submits) != 2 {
		t.Fatalf("expected 2 submits after retry, got %d", len(submits))
	}
	if submits[1] != id {
		t.Errorf("retry submitted wrong id: %q", submits[1])
	}
}

func TestConcurrentQueueAndCancel(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := svc.Queue(context.Background(), "soulseek", "peer", fmt.Sprintf("f%d.flac", n), int64(n), DownloadMeta{})
			if err != nil {
				t.Errorf("concurrent Queue failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	records, _ := store.List(context.Background())
	if len(records) != 20 {
		t.Errorf("expected 20 records, got %d", len(records))
	}
}

func TestQueueConcurrentWorkerPoolAccess(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = svc.Queue(context.Background(), "soulseek", "peer", fmt.Sprintf("f%d.flac", n), int64(n), DownloadMeta{})
		}(i)
	}
	wg.Wait()

	if len(pool.submitted()) != 10 {
		t.Errorf("expected 10 submits, got %d", len(pool.submitted()))
	}
}
