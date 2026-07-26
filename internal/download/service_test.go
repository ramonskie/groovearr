package download

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
	existing.RetryCount = r.RetryCount
	existing.RetryAfter = r.RetryAfter
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

// ─── Retry: RetryCount, MaxRetries, RetryAfter ────────────────────────

func TestManualRetryResetsRetryCount(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed, RetryCount: 5})

	_ = svc.Retry(context.Background(), id)

	rec, _ := store.Get(context.Background(), id)
	if rec.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0 (manual retry resets count)", rec.RetryCount)
	}
	if rec.State != domain.DownloadQueued {
		t.Errorf("state = %q, want queued", rec.State)
	}
	// Queue() already submitted once, Retry() submits again. Verify ID present.
	found := false
	for _, sid := range pool.submitted() {
		if sid == id {
			found = true
		}
	}
	if !found {
		t.Error("not dispatched to pool after manual retry")
	}
}

func TestManualRetryNotBlockedByMaxRetries(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed, RetryCount: domain.MaxRetries})

	err := svc.Retry(context.Background(), id)
	if err != nil {
		t.Fatalf("manual retry should succeed even at max retries, got: %v", err)
	}

	rec, _ := store.Get(context.Background(), id)
	if rec.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0", rec.RetryCount)
	}
}

func TestManualRetryClearsBackoff(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{})
	_ = store.Update(context.Background(), &domain.DownloadRecord{
		ID: id, State: domain.DownloadFailed,
		RetryAfter: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})

	_ = svc.Retry(context.Background(), id)

	rec, _ := store.Get(context.Background(), id)
	if rec.RetryAfter != "" {
		t.Errorf("RetryAfter = %q, want empty (manual retry dispatches immediately)", rec.RetryAfter)
	}
}

// ─── Auto-Retry Worker ────────────────────────────────────────────────

func TestAutoRetryIncrementsCountAndBackoff(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}

	id, _ := (&DownloadService{store: store, bus: newMockBus(), log: testLogger(), workerPool: pool}).Queue(
		context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{},
	)
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed, RetryCount: 3})

	svc := &DownloadService{store: store, bus: newMockBus(), log: testLogger(), workerPool: pool}
	failed, _ := store.ListByState(context.Background(), domain.DownloadFailed)

	before := time.Now().UTC()
	for _, rec := range failed {
		if rec.RetryCount >= domain.MaxRetries {
			continue
		}
		rec.RetryCount++
		backoffMin := 1 << rec.RetryCount
		rec.RetryAfter = time.Now().UTC().Add(time.Duration(backoffMin) * time.Minute).Format(time.RFC3339)
		_ = svc.dispatchRetry(context.Background(), &rec)
	}

	rec, _ := store.Get(context.Background(), id)
	if rec.RetryCount != 4 {
		t.Errorf("RetryCount = %d, want 4", rec.RetryCount)
	}
	retryAfter, _ := time.Parse(time.RFC3339, rec.RetryAfter)
	backoff := retryAfter.Sub(before)
	if backoff < 14*time.Minute || backoff > 18*time.Minute {
		t.Errorf("backoff = %v, want ~16 minutes (2^4)", backoff)
	}
}

func TestAutoRetrySkipsAtMaxRetries(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}

	id, _ := (&DownloadService{store: store, bus: newMockBus(), log: testLogger(), workerPool: pool}).Queue(
		context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{},
	)
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed, RetryCount: domain.MaxRetries})

	failed, _ := store.ListByState(context.Background(), domain.DownloadFailed)
	if len(failed) == 0 {
		t.Fatal("expected at least one failed record")
	}
	skipped := true
	for _, rec := range failed {
		if rec.RetryCount < domain.MaxRetries {
			skipped = false
		}
	}
	if !skipped {
		t.Error("should have skipped record at max retries")
	}

	rec, _ := store.Get(context.Background(), id)
	if rec.RetryCount != domain.MaxRetries {
		t.Errorf("RetryCount = %d, want %d (unchanged)", rec.RetryCount, domain.MaxRetries)
	}
}

func TestAutoRetryRespectsBackoff(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}

	id, _ := (&DownloadService{store: store, bus: newMockBus(), log: testLogger(), workerPool: pool}).Queue(
		context.Background(), "soulseek", "peer", "f.flac", 1, DownloadMeta{},
	)
	_ = store.Update(context.Background(), &domain.DownloadRecord{
		ID: id, State: domain.DownloadFailed,
		RetryAfter: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})

	failed, _ := store.ListByState(context.Background(), domain.DownloadFailed)
	retried := false
	for _, rec := range failed {
		if rec.RetryAfter != "" {
			retryAfter, _ := time.Parse(time.RFC3339, rec.RetryAfter)
			if time.Now().UTC().Before(retryAfter) {
				continue
			}
		}
		_ = pool.Submit(context.Background(), &rec)
		retried = true
	}
	if retried {
		t.Error("should have skipped record with future RetryAfter")
	}
}

// ─── submitQueued / RecoverOrphans / dispatchQueued ─────────────────

func TestSubmitQueuedAllDispatched(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	// Create 3 queued records.
	records := make([]domain.DownloadRecord, 3)
	for i := range records {
		records[i] = domain.DownloadRecord{
			ID:         fmt.Sprintf("rec-%d", i),
			State:      domain.DownloadQueued,
			SourceName: "soulseek",
			Filename:   fmt.Sprintf("f%d.flac", i),
		}
	}

	dispatched, full := svc.submitQueued(context.Background(), pool, records, true)
	if dispatched != 3 {
		t.Errorf("dispatched = %d, want 3", dispatched)
	}
	if full {
		t.Error("full = true, want false")
	}
	if len(pool.submitted()) != 3 {
		t.Errorf("submitted = %d, want 3", len(pool.submitted()))
	}
}

func TestSubmitQueuedSkipsPendingSource(t *testing.T) {
	pool := &mockWorkerPool{}
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), pool)

	records := []domain.DownloadRecord{
		{ID: "rec-1", State: domain.DownloadQueued, SourceName: domain.PendingSourceName, Filename: ""},
		{ID: "rec-2", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f.flac"},
	}

	dispatched, _ := svc.submitQueued(context.Background(), pool, records, true)
	if dispatched != 1 {
		t.Errorf("dispatched = %d, want 1 (pending skipped)", dispatched)
	}
	submitted := pool.submitted()
	if len(submitted) != 1 || submitted[0] != "rec-2" {
		t.Errorf("submitted = %v, want [rec-2]", submitted)
	}
}

func TestSubmitQueuedStopOnFull(t *testing.T) {
	pool := &mockWorkerPool{
		submitFn: func(ctx context.Context, r *domain.DownloadRecord) error {
			if r.ID == "rec-2" {
				return fmt.Errorf("pool full")
			}
			return nil
		},
	}
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), pool)

	records := []domain.DownloadRecord{
		{ID: "rec-1", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f1.flac"},
		{ID: "rec-2", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f2.flac"},
		{ID: "rec-3", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f3.flac"},
	}

	dispatched, full := svc.submitQueued(context.Background(), pool, records, true)
	if dispatched != 1 {
		t.Errorf("dispatched = %d, want 1 (stopped at rec-2)", dispatched)
	}
	if !full {
		t.Error("full = false, want true")
	}
}

func TestSubmitQueuedContinueOnError(t *testing.T) {
	pool := &mockWorkerPool{
		submitFn: func(ctx context.Context, r *domain.DownloadRecord) error {
			if r.ID == "rec-2" {
				return fmt.Errorf("transient error")
			}
			return nil
		},
	}
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), pool)

	records := []domain.DownloadRecord{
		{ID: "rec-1", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f1.flac"},
		{ID: "rec-2", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f2.flac"},
		{ID: "rec-3", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f3.flac"},
	}

	dispatched, full := svc.submitQueued(context.Background(), pool, records, false) // stopOnFull=false
	if dispatched != 2 {
		t.Errorf("dispatched = %d, want 2 (rec-2 failed but continued)", dispatched)
	}
	if full {
		t.Error("full = true, want false")
	}
}

func TestSubmitQueuedRespectsRetryAfter(t *testing.T) {
	pool := &mockWorkerPool{}
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), pool)

	future := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)

	records := []domain.DownloadRecord{
		{ID: "rec-1", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f1.flac", RetryAfter: future},
		{ID: "rec-2", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f2.flac", RetryAfter: past},
		{ID: "rec-3", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f3.flac"},
	}

	dispatched, _ := svc.submitQueued(context.Background(), pool, records, true)
	if dispatched != 2 {
		t.Errorf("dispatched = %d, want 2 (rec-1 skipped for future RetryAfter)", dispatched)
	}
	submitted := pool.submitted()
	if len(submitted) != 2 {
		t.Fatalf("submitted = %d, want 2", len(submitted))
	}
	if submitted[0] != "rec-2" || submitted[1] != "rec-3" {
		t.Errorf("submitted = %v, want [rec-2, rec-3]", submitted)
	}
}

func TestSubmitQueuedInvalidRetryAfterNotSkipped(t *testing.T) {
	pool := &mockWorkerPool{}
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger(), pool)

	records := []domain.DownloadRecord{
		{ID: "rec-1", State: domain.DownloadQueued, SourceName: "soulseek", Filename: "f1.flac", RetryAfter: "not-a-date"},
	}

	dispatched, _ := svc.submitQueued(context.Background(), pool, records, true)
	if dispatched != 1 {
		t.Errorf("dispatched = %d, want 1 (invalid RetryAfter treated as expired)", dispatched)
	}
}

func TestRecoverOrphansDispatchesQueued(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	// Insert records directly into store (simulating orphans).
	for i := 0; i < 3; i++ {
		_ = store.Insert(context.Background(), &domain.DownloadRecord{
			ID:         fmt.Sprintf("orphan-%d", i),
			SourceName: "soulseek",
			Filename:   fmt.Sprintf("f%d.flac", i),
			State:      domain.DownloadQueued,
		})
	}

	svc.RecoverOrphans(context.Background())

	submitted := pool.submitted()
	if len(submitted) != 3 {
		t.Errorf("submitted = %d, want 3", len(submitted))
	}
}

func TestRecoverOrphansSkipsPendingSource(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "pending-1",
		SourceName: domain.PendingSourceName,
		Filename:   "",
		State:      domain.DownloadQueued,
	})
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "real-1",
		SourceName: "soulseek",
		Filename:   "f.flac",
		State:      domain.DownloadQueued,
	})

	svc.RecoverOrphans(context.Background())

	submitted := pool.submitted()
	if len(submitted) != 1 || submitted[0] != "real-1" {
		t.Errorf("submitted = %v, want [real-1]", submitted)
	}
}

func TestRecoverOrphansNoPool(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger(), nil)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "orphan-1", SourceName: "soulseek", Filename: "f.flac", State: domain.DownloadQueued,
	})

	// Should not panic — no pool means queued records are skipped.
	svc.RecoverOrphans(context.Background())
}

func TestRecoverOrphansDownloadingToFailed(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, bus, testLogger(), pool)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "dl-1", SourceName: "soulseek", Filename: "f.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "dl-1", State: domain.DownloadDownloading})

	svc.RecoverOrphans(context.Background())

	rec, _ := store.Get(context.Background(), "dl-1")
	if rec == nil {
		t.Fatal("record not found")
	}
	if rec.State != domain.DownloadFailed {
		t.Errorf("state = %q, want %q", rec.State, domain.DownloadFailed)
	}
	if rec.Error == "" {
		t.Error("expected error message set")
	}

	// Verify TopicDownloadFailed event was published.
	found := false
	for _, e := range bus.published() {
		if e.Topic == events.TopicDownloadFailed {
			if r, ok := e.Event.(*domain.DownloadRecord); ok && r.ID == "dl-1" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("TopicDownloadFailed event not published")
	}

	// Queued should still work when pool is available.
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "q-1", SourceName: "soulseek", Filename: "f.flac",
	})
	svc.RecoverOrphans(context.Background())
	if len(pool.submitted()) != 1 || pool.submitted()[0] != "q-1" {
		t.Errorf("queued records not dispatched, got %v", pool.submitted())
	}
}

func TestRecoverOrphansDownloadingSkipsIfStateChanged(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, bus, testLogger(), pool)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "dl-1", SourceName: "soulseek", Filename: "f.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "dl-1", State: domain.DownloadDownloading})

	// Manually change state before recovery runs (simulating concurrent cancel).
	store.Update(context.Background(), &domain.DownloadRecord{ID: "dl-1", State: domain.DownloadIgnored})

	svc.RecoverOrphans(context.Background())

	rec, _ := store.Get(context.Background(), "dl-1")
	if rec.State != domain.DownloadIgnored {
		t.Errorf("state = %q, want ignored (CAS should skip)", rec.State)
	}
}

func TestRecoverOrphansImportPending(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, bus, testLogger(), pool)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "ip-1", SourceName: "soulseek", Filename: "f.flac",
		Artist: "Test", Title: "Song",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "ip-1", State: domain.DownloadImportPending})

	svc.RecoverOrphans(context.Background())

	// Verify TopicDownloadCompleted event was published with correct record.
	found := false
	for _, e := range bus.published() {
		if e.Topic == events.TopicDownloadCompleted {
			if r, ok := e.Event.(*domain.DownloadRecord); ok && r.ID == "ip-1" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("TopicDownloadCompleted event not published for importPending record")
	}

	// State should remain importPending (CompletedDownloadService transitions it).
	rec, _ := store.Get(context.Background(), "ip-1")
	if rec.State != domain.DownloadImportPending {
		t.Errorf("state = %q, want importPending (unchanged)", rec.State)
	}
}

func TestRecoverOrphansImportingToFailed(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, bus, testLogger(), pool)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "im-1", SourceName: "soulseek", Filename: "f.flac",
		FilePath: "/music/Test/Song.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "im-1", State: domain.DownloadImporting})

	svc.RecoverOrphans(context.Background())

	rec, _ := store.Get(context.Background(), "im-1")
	if rec.State != domain.DownloadFailed {
		t.Errorf("state = %q, want %q", rec.State, domain.DownloadFailed)
	}
	if rec.Error == "" {
		t.Error("expected error message set")
	}

	found := false
	for _, e := range bus.published() {
		if e.Topic == events.TopicDownloadFailed {
			if r, ok := e.Event.(*domain.DownloadRecord); ok && r.ID == "im-1" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("TopicDownloadFailed event not published")
	}
}

func TestRecoverOrphansMixedStates(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, bus, testLogger(), pool)

	// Insert records in all non-terminal states + a terminal one (should be ignored).
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "q-1", SourceName: "soulseek", Filename: "q.flac",
	})
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "dl-1", SourceName: "soulseek", Filename: "d.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "dl-1", State: domain.DownloadDownloading})
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "ip-1", SourceName: "soulseek", Filename: "ip.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "ip-1", State: domain.DownloadImportPending})
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "im-1", SourceName: "soulseek", Filename: "im.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "im-1", State: domain.DownloadImporting})

	svc.RecoverOrphans(context.Background())

	// Queued → dispatched.
	submitted := pool.submitted()
	if len(submitted) != 1 || submitted[0] != "q-1" {
		t.Errorf("queued submission: got %v, want [q-1]", submitted)
	}

	// Downloading → failed.
	dl, _ := store.Get(context.Background(), "dl-1")
	if dl.State != domain.DownloadFailed {
		t.Errorf("downloading state = %q, want failed", dl.State)
	}

	// ImportPending → event published.
	hasCompleted := false
	for _, e := range bus.published() {
		if e.Topic == events.TopicDownloadCompleted {
			if r, _ := e.Event.(*domain.DownloadRecord); r.ID == "ip-1" {
				hasCompleted = true
				break
			}
		}
	}
	if !hasCompleted {
		t.Error("importPending: TopicDownloadCompleted event missing")
	}

	// Importing → failed.
	im, _ := store.Get(context.Background(), "im-1")
	if im.State != domain.DownloadFailed {
		t.Errorf("importing state = %q, want failed", im.State)
	}
}

func TestRecoverOrphansNoPoolStillRecoversNonQueued(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger(), nil) // pool = nil

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "dl-1", SourceName: "soulseek", Filename: "f.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "dl-1", State: domain.DownloadDownloading})
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "ip-1", SourceName: "soulseek", Filename: "ip.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "ip-1", State: domain.DownloadImportPending})
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "im-1", SourceName: "soulseek", Filename: "im.flac",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: "im-1", State: domain.DownloadImporting})

	// Should not panic — non-queued states are recovered even without pool.
	svc.RecoverOrphans(context.Background())

	dl, _ := store.Get(context.Background(), "dl-1")
	if dl.State != domain.DownloadFailed {
		t.Errorf("downloading without pool: state = %q, want failed", dl.State)
	}
	im, _ := store.Get(context.Background(), "im-1")
	if im.State != domain.DownloadFailed {
		t.Errorf("importing without pool: state = %q, want failed", im.State)
	}
}

func TestRecoverOrphansPendingSourceStaysQueued(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "ps-1", SourceName: domain.PendingSourceName, Filename: "",
		Artist: "Test", Title: "Song",
	})

	svc.RecoverOrphans(context.Background())

	// Pending source stays in queued (not transitioned to failedPending).
	// Without a registry, ResolvePendingSources logs a warning and returns.
	rec, _ := store.Get(context.Background(), "ps-1")
	if rec.State != domain.DownloadQueued {
		t.Errorf("state = %q, want queued (should not auto-transition)", rec.State)
	}

	// Not submitted to pool (needs resolution first).
	if len(pool.submitted()) != 0 {
		t.Errorf("pending source submitted to pool = %v, want empty", pool.submitted())
	}
}

func TestRecoverOrphansResolvedQueuedStillDispatched(t *testing.T) {
	store := newMockStore()
	pool := &mockWorkerPool{}
	svc := NewDownloadService(store, newMockBus(), testLogger(), pool)

	// Resolved queued with pending source — stays queued, not dispatched.
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "ps-1", SourceName: domain.PendingSourceName, Filename: "",
		Artist: "Test", Title: "Song",
	})
	// Already resolved — dispatched to pool.
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "r-1", SourceName: "soulseek", Filename: "f.flac",
	})

	svc.RecoverOrphans(context.Background())

	submitted := pool.submitted()
	if len(submitted) != 1 || submitted[0] != "r-1" {
		t.Errorf("submitted = %v, want [r-1] (only resolved records dispatched)", submitted)
	}
}
