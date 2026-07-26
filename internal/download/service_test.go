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
	r2 := *r
	m.records[r.ID] = &r2
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
	if r.SourceName != "" {
		existing.SourceName = r.SourceName
	}
	if r.Filename != "" {
		existing.Filename = r.Filename
	}
	if r.Bitrate != 0 {
		existing.Bitrate = r.Bitrate
	}
	if r.Format != "" {
		existing.Format = r.Format
	}
	if r.Username != "" {
		existing.Username = r.Username
	}
	if r.Artist != "" {
		existing.Artist = r.Artist
	}
	if r.Title != "" {
		existing.Title = r.Title
	}
	if r.Album != "" {
		existing.Album = r.Album
	}
	if r.DisplayName != "" {
		existing.DisplayName = r.DisplayName
	}
	return nil
}

func (m *mockStore) UpdateProgress(ctx context.Context, id string, state domain.DownloadState, progress float64, size, transferred, speed int64, filePath, coverURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	if !ok {
		return fmt.Errorf("id %q not found", id)
	}
	rec.State = state
	rec.Progress = progress
	rec.Size = size
	rec.Transferred = transferred
	rec.Speed = speed
	if filePath != "" {
		rec.FilePath = filePath
	}
	if coverURL != "" {
		rec.CoverURL = coverURL
	}
	return nil
}

func (m *mockStore) TransitionState(ctx context.Context, id string, oldState, newState domain.DownloadState) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	if !ok {
		return false, nil
	}
	if rec.State != oldState {
		return false, nil
	}
	rec.State = newState
	return true, nil
}

func (m *mockStore) Get(ctx context.Context, id string) (*domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	if !ok {
		return nil, nil
	}
	r2 := *rec
	return &r2, nil
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
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DownloadRecord
	for _, r := range m.records {
		if r.PlaylistID == playlistID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockStore) FindActiveByTitle(ctx context.Context, artist, title string) (*domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records {
		if r.Artist == artist && r.Title == title && !r.State.Terminal() {
			r2 := *r
			return &r2, nil
		}
	}
	return nil, nil
}

func (m *mockStore) RecordEvent(ctx context.Context, event *domain.DownloadEvent) error { return nil }
func (m *mockStore) GetEvents(ctx context.Context, downloadID string) ([]domain.DownloadEvent, error) {
	return nil, nil
}
func (m *mockStore) DeleteTerminal(ctx context.Context) error { return nil }
func (m *mockStore) Close() error                            { return nil }

// ─── Mock event bus ───────────────────────────────────────────────────

type mockBusEvent struct {
	Topic string
	Event any
}

type mockBus struct {
	mu       sync.Mutex
	events   []mockBusEvent
	pubPanic bool
}

func newMockBus() *mockBus { return &mockBus{} }

func (b *mockBus) Subscribe(topic string, handler events.EventHandler) {}

func (b *mockBus) Unsubscribe(topic string, handler events.EventHandler) {}

func (b *mockBus) Publish(ctx context.Context, topic string, event any) {
	if b.pubPanic {
		panic("mock bus panic")
	}
	b.mu.Lock()
	b.events = append(b.events, mockBusEvent{Topic: topic, Event: event})
	b.mu.Unlock()
}

func (b *mockBus) published() []mockBusEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]mockBusEvent, len(b.events))
	copy(out, b.events)
	return out
}

// Compile-time check.
var _ events.IEventAggregator = (*mockBus)(nil)

// ─── Service tests ────────────────────────────────────────────────────

func TestNewDownloadService(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger())
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

// ─── Queue tests ──────────────────────────────────────────────────────

func TestQueueCreatesRecord(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	id, err := svc.Queue(context.Background(), "soulseek", "peer", "song.flac", 12345678, DownloadMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	record, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("record not found")
	}
	if record.SourceName != "soulseek" {
		t.Errorf("source = %q, want %q", record.SourceName, "soulseek")
	}
	if record.Filename != "song.flac" {
		t.Errorf("filename = %q", record.Filename)
	}
	if record.State != domain.DownloadQueued {
		t.Errorf("state = %q, want %q", record.State, domain.DownloadQueued)
	}
	if record.Size != 12345678 {
		t.Errorf("size = %d", record.Size)
	}
}

func TestQueueSetsDisplayName(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	id, _ := svc.Queue(context.Background(), "soulseek", "peer", "song.flac", 1, DownloadMeta{
		Artist: "Artist", Title: "Title",
	})
	record, _ := store.Get(context.Background(), id)
	if record.DisplayName != "Artist - Title" {
		t.Errorf("display_name = %q, want %q", record.DisplayName, "Artist - Title")
	}
}

func TestQueueFiresQueuedEvent(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger())

	id, err := svc.Queue(context.Background(), "soulseek", "peer", "song.flac", 1, DownloadMeta{})
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
	if record.ID != id {
		t.Errorf("event record id = %q, want %q", record.ID, id)
	}
	if record.State != domain.DownloadQueued {
		t.Errorf("event record state = %q", record.State)
	}
}

func TestQueuePersistsMetaFields(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	id, _ := svc.Queue(context.Background(), "deezer", "dluser", "42.mp3", 999, DownloadMeta{
		Artist: "TestArtist", Album: "TestAlbum", Title: "TestTitle",
		TrackNumber: 3, DiscNumber: 1, Year: 2024,
		TrackID: "trk-42", ISRC: "US-ABC-24-00001", CoverURL: "http://cover.jpg",
		PlaylistID: "pl-99", Bitrate: 320, Format: "mp3",
	})
	record, _ := store.Get(context.Background(), id)
	if record.Artist != "TestArtist" {
		t.Errorf("artist = %q", record.Artist)
	}
	if record.TrackNumber != 3 {
		t.Errorf("track_number = %d", record.TrackNumber)
	}
	if record.ISRC != "US-ABC-24-00001" {
		t.Errorf("isrc = %q", record.ISRC)
	}
	if record.CoverURL != "http://cover.jpg" {
		t.Errorf("cover_url not set")
	}
	if record.Bitrate != 320 {
		t.Errorf("bitrate = %d", record.Bitrate)
	}
	if record.Format != "mp3" {
		t.Errorf("format = %q", record.Format)
	}
}

func TestQueueDedupSkipsActive(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	meta := DownloadMeta{Artist: "DupeArtist", Title: "DupeTitle"}
	id1, err := svc.Queue(context.Background(), "soulseek", "peer", "f1.flac", 1, meta)
	if err != nil {
		t.Fatal(err)
	}

	// Second queue with same artist+title should return existing ID.
	id2, err := svc.Queue(context.Background(), "soulseek", "peer", "f2.flac", 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("dedup should return same id, got %q and %q", id1, id2)
	}
}

func TestQueueDedupErrorLogsWarning(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	meta := DownloadMeta{Artist: "ErrArtist", Title: "ErrTitle"}
	id, err := svc.Queue(context.Background(), "soulseek", "peer", "f1.flac", 1, meta)
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt store — the dedup check should warn and proceed.
	store.mu.Lock()
	delete(store.records, id) // remove the record to force a nil result with no error
	store.mu.Unlock()

	// Insert nil record will trigger an error on FindActiveByTitle in real store;
	// mock always returns nil,nil so this tests the happy dedup miss path.
	id2, err := svc.Queue(context.Background(), "soulseek", "peer", "f2.flac", 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	if id == id2 {
		t.Error("expected new id after removing original record")
	}
}

func TestQueueDedupPreservesState(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	meta := DownloadMeta{Artist: "PreserveArt", Title: "PreserveTitle"}
	id1, _ := svc.Queue(context.Background(), "soulseek", "peer", "f1.flac", 1, meta)

	// Manually change state — dedup should still return id1 since it's active.
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id1, State: domain.DownloadDownloading})

	id2, err := svc.Queue(context.Background(), "soulseek", "peer", "f2.flac", 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("dedup should return same id for downloading state, got %q and %q", id1, id2)
	}
}

func TestQueueNoDedupForTerminal(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	meta := DownloadMeta{Artist: "TermArt", Title: "TermTitle"}
	id1, _ := svc.Queue(context.Background(), "soulseek", "peer", "f1.flac", 1, meta)
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id1, State: domain.DownloadImported})

	id2, err := svc.Queue(context.Background(), "soulseek", "peer", "f2.flac", 1, meta)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Error("dedup should NOT match terminal record")
	}
}

// ─── QueuePending tests ────────────────────────────────────────────────

func TestQueuePendingCreatesRecord(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger())

	id, err := svc.QueuePending(context.Background(), DownloadMeta{
		Artist: "Artist", Album: "Album", Title: "Title",
		Bitrate: 320, Format: "flac",
	})
	if err != nil {
		t.Fatal(err)
	}

	record, _ := store.Get(context.Background(), id)
	if record == nil {
		t.Fatal("record not found")
	}
	if record.State != domain.DownloadQueued {
		t.Errorf("state = %q, want %q", record.State, domain.DownloadQueued)
	}
	if !record.IsPendingSource() {
		t.Error("record should be pending source")
	}

	evts := bus.published()
	if len(evts) != 1 || evts[0].Topic != events.TopicDownloadQueued {
		t.Errorf("expected 1 TopicDownloadQueued event, got %v", evts)
	}
}

func TestQueuePendingDedup(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	meta := DownloadMeta{Artist: "A", Title: "T"}
	id1, _ := svc.QueuePending(context.Background(), meta)
	id2, _ := svc.QueuePending(context.Background(), meta)
	if id1 != id2 {
		t.Errorf("expected dedup, got %q and %q", id1, id2)
	}
}

func TestQueuePendingNoDedupMissingArtistTitle(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	// No artist/title → dedup is skipped, two records are created.
	id1, _ := svc.QueuePending(context.Background(), DownloadMeta{})
	id2, _ := svc.QueuePending(context.Background(), DownloadMeta{})
	if id1 == id2 {
		t.Error("expected two distinct records when artist/title are empty")
	}
}

// ─── GetStatus / List tests ────────────────────────────────────────────

func TestGetStatus(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

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
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger())
	record, err := svc.GetStatus(context.Background(), "no-such-id")
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Error("expected nil for non-existent record")
	}
}

func TestList(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

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

// ─── Cancel tests ──────────────────────────────────────────────────────

func TestCancelSetsIgnored(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger())

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
	svc := NewDownloadService(store, bus, testLogger())

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
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger())
	err := svc.Cancel(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error for non-existent id")
	}
}

func TestCancelAlreadyTerminalIsNoOp(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger())

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

// ─── Retry tests ───────────────────────────────────────────────────────

func TestRetryResetsToQueued(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()
	svc := NewDownloadService(store, bus, testLogger())

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
	svc := NewDownloadService(store, bus, testLogger())

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
	svc := NewDownloadService(store, newMockBus(), testLogger())

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
	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger())
	err := svc.Retry(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error for non-existent id")
	}
}

func TestConcurrentQueueAndCancel(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

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

// ─── Retry: RetryCount, MaxRetries, RetryAfter ────────────────────────

func TestManualRetryResetsRetryCount(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

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
}

func TestManualRetryNotBlockedByMaxRetries(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

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
	svc := NewDownloadService(store, newMockBus(), testLogger())

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

// ─── Retry: async source resolution ──────────────────────────────────

func TestResolveRetrySourcePopulatesFields(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockPlugin{
		name: "soulseek", display: "Soulseek", configured: true, connected: true,
		searchResults: []domain.TrackResult{
			{SearchResult: domain.SearchResult{
				Filename: "found.flac", Size: 999, Bitrate: 320, Quality: "mp3", Username: "peer2",
			}, Title: "Title", Artist: "Artist"},
		},
	})

	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())
	svc.SetRegistry(reg)

	id, _ := svc.Queue(context.Background(), "soulseek", "peer1", "old.flac", 100, DownloadMeta{
		Artist: "Artist", Title: "Title",
	})
	_ = store.Update(context.Background(), &domain.DownloadRecord{ID: id, State: domain.DownloadFailed})

	rec, _ := store.Get(context.Background(), id)

	// The test record must be in failed state with metadata for the search.
	rec.State = domain.DownloadFailed
	rec.Artist = "Artist"
	rec.Title = "Title"

	svc.resolveRetrySource(context.Background(), rec)

	if rec.Filename != "found.flac" {
		t.Errorf("Filename = %q, want %q", rec.Filename, "found.flac")
	}
	if rec.Size != 999 {
		t.Errorf("Size = %d, want 999", rec.Size)
	}
	if rec.Bitrate != 320 {
		t.Errorf("Bitrate = %d, want 320", rec.Bitrate)
	}
	if rec.Format != "mp3" {
		t.Errorf("Format = %q, want %q", rec.Format, "mp3")
	}
	if rec.Username != "peer2" {
		t.Errorf("Username = %q, want %q", rec.Username, "peer2")
	}
}

func TestResolveRetrySourceNoResultsKeepsOriginalSource(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockPlugin{
		name: "soulseek", display: "Soulseek", configured: true, connected: true,
		searchResults: nil, // no results
	})

	svc := NewDownloadService(newMockStore(), newMockBus(), testLogger())
	svc.SetRegistry(reg)

	rec := &domain.DownloadRecord{
		ID: "test-1", SourceName: "soulseek", Filename: "old.flac",
		Artist: "Artist", Title: "Title", State: domain.DownloadFailed,
	}

	svc.resolveRetrySource(context.Background(), rec)

	if rec.Filename != "old.flac" {
		t.Errorf("Filename = %q, want %q (original preserved when search finds nothing)", rec.Filename, "old.flac")
	}
}

func TestResolveAndSubmitSuccess(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockPlugin{
		name: "soulseek", display: "Soulseek", configured: true, connected: true,
		searchResults: []domain.TrackResult{
			{SearchResult: domain.SearchResult{
				Filename: "found.flac", Size: 888, Bitrate: 320, Quality: "mp3", Username: "peer2",
			}, Title: "Title", Artist: "Artist"},
		},
	})

	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())
	svc.SetRegistry(reg)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "test-1", SourceName: "soulseek", Filename: "old.flac",
		Artist: "Artist", Title: "Title", State: domain.DownloadQueued,
	})

	rec, _ := store.Get(context.Background(), "test-1")

	svc.resolveAndSubmit(rec, retryOriginalSnap{})

	// Verify source fields were updated in the store.
	stored, _ := store.Get(context.Background(), "test-1")
	if stored.Filename != "found.flac" {
		t.Errorf("stored Filename = %q, want %q", stored.Filename, "found.flac")
	}
}

func TestResolveAndSubmitNoSourceTransitionsToFailed(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockPlugin{
		name: "soulseek", display: "Soulseek", configured: true, connected: true,
		searchResults: nil, // no results
	})

	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())
	svc.SetRegistry(reg)

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "test-2", SourceName: "soulseek", Filename: "",
		Artist: "Artist", Title: "Title", State: domain.DownloadQueued,
	})

	rec, _ := store.Get(context.Background(), "test-2")

	svc.resolveAndSubmit(rec, retryOriginalSnap{})

	stored, _ := store.Get(context.Background(), "test-2")
	if stored.State != domain.DownloadFailed {
		t.Errorf("state = %q, want %q", stored.State, domain.DownloadFailed)
	}
	if stored.Error == "" {
		t.Error("expected error message on resolution failure")
	}
}

func TestFailRetrySetsFailed(t *testing.T) {
	store := newMockStore()
	svc := NewDownloadService(store, newMockBus(), testLogger())

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID: "test-3", State: domain.DownloadQueued,
	})

	rec, _ := store.Get(context.Background(), "test-3")
	svc.failRetry(context.Background(), rec, "something went wrong")

	stored, _ := store.Get(context.Background(), "test-3")
	if stored.State != domain.DownloadFailed {
		t.Errorf("state = %q, want %q", stored.State, domain.DownloadFailed)
	}
	if stored.Error != "something went wrong" {
		t.Errorf("error = %q, want %q", stored.Error, "something went wrong")
	}
}
