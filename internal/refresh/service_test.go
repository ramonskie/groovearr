package refresh

import (
	"context"
	"errors"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// fixedNow is the deterministic clock every test drives the service with.
var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// ─── Fakes ───────────────────────────────────────────────────────────

// fakeStore is an in-memory catalog.Store covering the refresh-engine surface.
// It is mutex-guarded because the start/stop loop test drives it from a
// background goroutine while the test observes it.
type fakeStore struct {
	mu       sync.Mutex
	queue    map[int64]*catalog.RefreshItem
	nextID   int64
	meta     map[int64]catalog.AlbumMetadata
	tracks   map[int64][]catalog.AlbumTrack
	retries  []retryCall
	finishes []finishCall
}

type retryCall struct {
	id       int64
	attempts int
	next     time.Time
	lastErr  string
}

type finishCall struct {
	id     int64
	status catalog.RefreshStatus
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		queue:  map[int64]*catalog.RefreshItem{},
		nextID: 1,
		meta:   map[int64]catalog.AlbumMetadata{},
		tracks: map[int64][]catalog.AlbumTrack{},
	}
}

func (s *fakeStore) enqueue(item catalog.RefreshItem) int64 {
	if item.ID == 0 {
		item.ID = s.nextID
		s.nextID++
	}
	cp := item
	s.queue[item.ID] = &cp
	return item.ID
}

func (s *fakeStore) EnqueueRefresh(_ context.Context, item catalog.RefreshItem) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueue(item), nil
}

func (s *fakeStore) ClaimDueRefresh(_ context.Context, now time.Time, limit int) ([]catalog.RefreshItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []catalog.RefreshItem
	for _, it := range s.queue {
		if !it.DueAt.After(now) {
			due = append(due, *it)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].Priority != due[j].Priority {
			return due[i].Priority > due[j].Priority
		}
		return due[i].DueAt.Before(due[j].DueAt)
	})
	if len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

func (s *fakeStore) FinishRefresh(_ context.Context, id int64, status catalog.RefreshStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishes = append(s.finishes, finishCall{id: id, status: status})
	delete(s.queue, id)
	return nil
}

func (s *fakeStore) RetryRefresh(_ context.Context, id int64, attempts int, next time.Time, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries = append(s.retries, retryCall{id: id, attempts: attempts, next: next, lastErr: lastErr})
	if it, ok := s.queue[id]; ok {
		it.Attempts = attempts
		it.DueAt = next
		it.LastError = lastErr
	}
	return nil
}

func (s *fakeStore) DeleteRefresh(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queue, id)
	return nil
}

func (s *fakeStore) BumpRefreshPriority(_ context.Context, _ catalog.EntityType, _ int64, _ int) error {
	return nil
}

func (s *fakeStore) CountQueuedRefresh(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue), nil
}

func (s *fakeStore) UpsertAlbumMetadata(_ context.Context, m catalog.AlbumMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = fixedNow
	}
	s.meta[m.AlbumID] = m
	return nil
}

func (s *fakeStore) GetAlbumMetadata(_ context.Context, albumID int64) (*catalog.AlbumMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meta[albumID]
	if !ok {
		return nil, nil
	}
	cp := m
	return &cp, nil
}

func (s *fakeStore) ListAlbumsDueForRefresh(context.Context, time.Time, int) ([]int64, error) {
	return nil, nil
}

func (s *fakeStore) TouchAlbumAccessed(context.Context, int64, time.Time) error { return nil }

func (s *fakeStore) ReplaceAlbumTracks(_ context.Context, albumID int64, tracks []catalog.AlbumTrack) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]catalog.AlbumTrack, len(tracks))
	copy(cp, tracks)
	s.tracks[albumID] = cp
	return nil
}

func (s *fakeStore) GetAlbumTracks(_ context.Context, albumID int64) ([]catalog.AlbumTrack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tracks[albumID], nil
}

func (s *fakeStore) DeleteAlbumTracks(_ context.Context, albumID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tracks, albumID)
	return nil
}

func (s *fakeStore) CountAlbumTracks(_ context.Context, albumID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tracks[albumID]), nil
}

// fakeProvider is a configurable discovery.Provider that records call counts.
type fakeProvider struct {
	name          string
	searchResults []discovery.AlbumResult
	tracks        []discovery.TrackInfo
	searchErr     error
	tracksErr     error
	searchCalls   int
	tracksCalls   int
}

func (p *fakeProvider) Name() string                          { return p.name }
func (p *fakeProvider) DisplayName() string                   { return p.name }
func (p *fakeProvider) IsConfigured() bool                    { return true }
func (p *fakeProvider) CheckConnection(context.Context) error { return nil }
func (p *fakeProvider) Connected() bool                       { return true }
func (p *fakeProvider) CapabilityStatus() map[string]string   { return nil }
func (p *fakeProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	return nil, nil
}
func (p *fakeProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}

func (p *fakeProvider) SearchAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	p.searchCalls++
	if p.searchErr != nil {
		return nil, p.searchErr
	}
	return p.searchResults, nil
}

func (p *fakeProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	p.tracksCalls++
	if p.tracksErr != nil {
		return nil, p.tracksErr
	}
	return p.tracks, nil
}

// fakeLibrary is a minimal Library backed by maps.
type fakeLibrary struct {
	albums  map[int64]*domain.Album
	artists map[int64]*domain.Artist
}

func (l *fakeLibrary) GetAlbum(_ context.Context, id int64) (*domain.Album, error) {
	return l.albums[id], nil
}

func (l *fakeLibrary) GetArtist(_ context.Context, id int64) (*domain.Artist, error) {
	return l.artists[id], nil
}

// fakeCooldown records marks and answers CoolingDown from a set.
type fakeCooldown struct {
	cooling map[string]bool
	marks   []string
}

func newFakeCooldown() *fakeCooldown { return &fakeCooldown{cooling: map[string]bool{}} }

func (c *fakeCooldown) CoolingDown(name string) bool { return c.cooling[name] }

func (c *fakeCooldown) MarkAfter(name string, _ time.Duration) { c.marks = append(c.marks, name) }

// ─── Helpers ─────────────────────────────────────────────────────────

// newTestService wires a service driven by the fixed clock with no cooldown
// unless the caller replaces it. Use `svc.cooldown = cd` for cooldown tests.
func newTestService(store Store, lib Library, opts Options, providers ...discovery.Provider) *Service {
	svc := New(store, lib, func() []discovery.Provider { return providers }, nil, nil, opts)
	svc.now = func() time.Time { return fixedNow }
	return svc
}

func dueAlbumItem(entityID int64) catalog.RefreshItem {
	return catalog.RefreshItem{
		ID:         1,
		EntityType: catalog.EntityAlbum,
		EntityID:   entityID,
		Reason:     catalog.ReasonEvent,
		Priority:   catalog.PriorityDefault,
		DueAt:      fixedNow.Add(-time.Minute),
	}
}

// ─── Tests ───────────────────────────────────────────────────────────

func TestService_ProcessBatch_MaterializesAlbumOnDrain(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &fakeProvider{
		name:          "alpha",
		searchResults: []discovery.AlbumResult{{ProviderID: "pa1", ProviderName: "alpha", ArtistName: "Artist One", Title: "Album One"}},
		tracks: []discovery.TrackInfo{
			{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"},
			{ProviderID: "t2", DiscNumber: 1, TrackNumber: 2, Title: "Song 2"},
		},
	}
	svc := newTestService(store, lib, Options{}, p)

	// Act
	handled, err := svc.processBatch(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if handled != 1 {
		t.Fatalf("handled = %d, want 1", handled)
	}
	if len(store.queue) != 0 {
		t.Fatalf("queue not drained: %d rows remain", len(store.queue))
	}
	tracks, _ := store.GetAlbumTracks(context.Background(), 42)
	if len(tracks) != 2 {
		t.Fatalf("materialized tracks = %d, want 2", len(tracks))
	}
	if tracks[0].Origin != catalog.OriginProvider {
		t.Fatalf("origin = %q, want provider", tracks[0].Origin)
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), 42)
	if meta == nil || meta.RefreshStatus != catalog.StatusFresh {
		t.Fatalf("meta = %+v, want fresh", meta)
	}
	if meta.ProviderAlbumID != "pa1" || meta.ProviderName != "alpha" {
		t.Fatalf("provider link = %s/%s, want alpha/pa1", meta.ProviderName, meta.ProviderAlbumID)
	}
	if p.searchCalls != 1 || p.tracksCalls != 1 {
		t.Fatalf("provider calls search=%d tracks=%d, want 1/1", p.searchCalls, p.tracksCalls)
	}
}

func TestService_ProcessBatch_ReusesProviderLinkWithoutSearch(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	store.meta[42] = catalog.AlbumMetadata{
		AlbumID:         42,
		ProviderName:    "alpha",
		ProviderAlbumID: "pa1",
		RefreshStatus:   catalog.StatusStale,
		LastAccessedAt:  fixedNow.Add(-24 * time.Hour),
	}
	p := &fakeProvider{
		name:   "alpha",
		tracks: []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
	}
	svc := newTestService(store, &fakeLibrary{}, Options{}, p)

	// Act
	handled, err := svc.processBatch(context.Background())

	// Assert
	if err != nil || handled != 1 {
		t.Fatalf("processBatch handled=%d err=%v, want 1/nil", handled, err)
	}
	if p.searchCalls != 0 {
		t.Fatalf("SearchAlbums called %d times, want 0 when link is stored", p.searchCalls)
	}
	if p.tracksCalls != 1 {
		t.Fatalf("GetAlbumTracks called %d times, want 1", p.tracksCalls)
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), 42)
	if meta.ProviderAlbumID != "pa1" || meta.RefreshStatus != catalog.StatusFresh {
		t.Fatalf("meta = %+v, want fresh link pa1", meta)
	}
	if !meta.LastAccessedAt.Equal(fixedNow.Add(-24 * time.Hour)) {
		t.Fatalf("last accessed = %v, want preserved", meta.LastAccessedAt)
	}
}

func TestService_ProcessBatch_SkipsCoolingProviderAndReschedules(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	store.meta[42] = catalog.AlbumMetadata{
		AlbumID: 42, ProviderName: "alpha", ProviderAlbumID: "pa1", RefreshStatus: catalog.StatusStale,
	}
	p := &fakeProvider{name: "alpha", tracks: []discovery.TrackInfo{{ProviderID: "t1"}}}
	cd := newFakeCooldown()
	cd.cooling["alpha"] = true
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	svc := newTestService(store, lib, Options{}, p)
	svc.cooldown = cd

	// Act
	handled, err := svc.processBatch(context.Background())

	// Assert
	if err != nil || handled != 1 {
		t.Fatalf("handled=%d err=%v, want 1/nil", handled, err)
	}
	if p.searchCalls != 0 || p.tracksCalls != 0 {
		t.Fatalf("provider called while cooling: search=%d tracks=%d", p.searchCalls, p.tracksCalls)
	}
	if len(store.queue) != 1 {
		t.Fatalf("queue = %d rows, want item rescheduled", len(store.queue))
	}
	if len(store.retries) != 1 || store.retries[0].attempts != 0 {
		t.Fatalf("retries = %+v, want one with attempts unchanged (0)", store.retries)
	}
	if !store.retries[0].next.After(fixedNow) {
		t.Fatalf("retry next = %v, want a future time", store.retries[0].next)
	}
	if len(store.finishes) != 0 {
		t.Fatalf("finishes = %+v, want none", store.finishes)
	}
}

func TestService_ProcessBatch_RateLimitedMarksCooldownAndReschedules(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	store.meta[42] = catalog.AlbumMetadata{
		AlbumID: 42, ProviderName: "alpha", ProviderAlbumID: "pa1", RefreshStatus: catalog.StatusStale,
	}
	p := &fakeProvider{name: "alpha", tracksErr: metadata.NewRateLimitError("alpha", 90*time.Second, "429")}
	cd := newFakeCooldown()
	svc := newTestService(store, &fakeLibrary{}, Options{}, p)
	svc.cooldown = cd

	// Act
	handled, err := svc.processBatch(context.Background())

	// Assert
	if err != nil || handled != 1 {
		t.Fatalf("handled=%d err=%v, want 1/nil", handled, err)
	}
	if len(cd.marks) != 1 || cd.marks[0] != "alpha" {
		t.Fatalf("cooldown marks = %v, want [alpha]", cd.marks)
	}
	if len(store.retries) != 1 || store.retries[0].attempts != 0 {
		t.Fatalf("retries = %+v, want one rescheduled with no failure counted", store.retries)
	}
	if len(store.finishes) != 0 {
		t.Fatalf("finishes = %+v, want none (not failed)", store.finishes)
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), 42)
	if meta.RefreshStatus == catalog.StatusFailed {
		t.Fatalf("meta parked as failed; a rate limit must not be a hard failure")
	}
}

func TestService_ProcessItem_BackoffThenTerminalFailure(t *testing.T) {
	tests := []struct {
		name          string
		startAttempts int
		wantRetry     bool
		wantAttempts  int
		wantFailed    bool
	}{
		{name: "first failure backs off and retries", startAttempts: 0, wantRetry: true, wantAttempts: 1},
		{name: "second failure backs off and retries", startAttempts: 1, wantRetry: true, wantAttempts: 2},
		{name: "third failure is terminal", startAttempts: 2, wantRetry: false, wantFailed: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			store := newFakeStore()
			store.enqueue(catalog.RefreshItem{
				ID: 1, EntityType: catalog.EntityAlbum, EntityID: 42,
				Priority: catalog.PriorityDefault, DueAt: fixedNow, Attempts: tc.startAttempts,
			})
			store.meta[42] = catalog.AlbumMetadata{
				AlbumID: 42, ProviderName: "alpha", ProviderAlbumID: "pa1", RefreshStatus: catalog.StatusStale,
			}
			p := &fakeProvider{name: "alpha", tracksErr: errors.New("boom")}
			svc := newTestService(store, &fakeLibrary{}, Options{MaxAttempts: 3}, p)
			item := catalog.RefreshItem{ID: 1, EntityType: catalog.EntityAlbum, EntityID: 42, Attempts: tc.startAttempts}

			// Act
			if err := svc.processItem(context.Background(), item); err != nil {
				t.Fatalf("processItem: %v", err)
			}

			// Assert
			meta, _ := store.GetAlbumMetadata(context.Background(), 42)
			if tc.wantFailed {
				if len(store.retries) != 0 {
					t.Fatalf("retries = %+v, want none at max attempts", store.retries)
				}
				if len(store.finishes) != 1 || store.finishes[0].status != catalog.StatusFailed {
					t.Fatalf("finishes = %+v, want one StatusFailed", store.finishes)
				}
				if meta.RefreshStatus != catalog.StatusFailed {
					t.Fatalf("meta status = %q, want failed", meta.RefreshStatus)
				}
				if _, ok := store.queue[1]; ok {
					t.Fatalf("queue row still present; terminal failure must drain it")
				}
				return
			}
			if len(store.retries) != 1 {
				t.Fatalf("retries = %+v, want exactly one", store.retries)
			}
			if store.retries[0].attempts != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", store.retries[0].attempts, tc.wantAttempts)
			}
			wantNext := fixedNow.Add(catalog.BackoffFor(tc.wantAttempts))
			if !store.retries[0].next.Equal(wantNext) {
				t.Fatalf("next = %v, want %v", store.retries[0].next, wantNext)
			}
			if meta.RefreshStatus != catalog.StatusPending {
				t.Fatalf("meta status = %q, want pending", meta.RefreshStatus)
			}
		})
	}
}

func TestService_ProcessBatch_UnmatchedSchedulesLongRetry(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &fakeProvider{name: "alpha", searchResults: nil} // no match
	svc := newTestService(store, lib, Options{}, p)

	// Act
	handled, err := svc.processBatch(context.Background())

	// Assert
	if err != nil || handled != 1 {
		t.Fatalf("handled=%d err=%v, want 1/nil", handled, err)
	}
	if p.tracksCalls != 0 {
		t.Fatalf("GetAlbumTracks called %d times, want 0 on no match", p.tracksCalls)
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), 42)
	if meta == nil || meta.RefreshStatus != catalog.StatusUnmatched {
		t.Fatalf("meta = %+v, want unmatched", meta)
	}
	if len(store.retries) != 1 {
		t.Fatalf("retries = %+v, want one long retry", store.retries)
	}
	wantNext := fixedNow.Add(catalog.UnmatchedRetry)
	if !store.retries[0].next.Equal(wantNext) {
		t.Fatalf("unmatched retry next = %v, want %v", store.retries[0].next, wantNext)
	}
	if store.retries[0].attempts != 0 {
		t.Fatalf("unmatched attempts = %d, want unchanged (0)", store.retries[0].attempts)
	}
	if len(store.finishes) != 0 {
		t.Fatalf("finishes = %+v, want none (queue drains via long retry)", store.finishes)
	}
}

func TestService_ProcessBatch_NoProvidersReschedulesWithoutFailure(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	svc := newTestService(store, &fakeLibrary{}, Options{})

	// Act
	handled, err := svc.processBatch(context.Background())

	// Assert
	if err != nil || handled != 1 {
		t.Fatalf("handled=%d err=%v, want 1/nil", handled, err)
	}
	if len(store.queue) != 1 {
		t.Fatalf("queue = %d rows, want item rescheduled", len(store.queue))
	}
	if len(store.retries) != 1 || store.retries[0].attempts != 0 {
		t.Fatalf("retries = %+v, want one with no failure counted", store.retries)
	}
	if len(store.finishes) != 0 {
		t.Fatalf("finishes = %+v, want none", store.finishes)
	}
}

func TestService_StartDrainsThenStopsCleanly(t *testing.T) {
	// Arrange
	baseline := runtime.NumGoroutine()
	store := newFakeStore()
	store.queue[1] = &catalog.RefreshItem{
		ID: 1, EntityType: catalog.EntityAlbum, EntityID: 42,
		Priority: catalog.PriorityDefault, DueAt: time.Now().Add(-time.Minute),
	}
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &fakeProvider{
		name:          "alpha",
		searchResults: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:        []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
	}
	svc := New(store, lib, func() []discovery.Provider { return []discovery.Provider{p} }, nil, nil,
		Options{Interval: 5 * time.Millisecond})

	// Act
	svc.Start(context.Background())
	svc.Start(context.Background()) // second Start must be a no-op, not a leak

	waitUntil(t, 2*time.Second, func() bool {
		n, _ := store.CountQueuedRefresh(context.Background())
		return n == 0
	})

	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()

	// Assert
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return")
	}
	waitUntil(t, time.Second, func() bool { return runtime.NumGoroutine() <= baseline+1 })
	if n := runtime.NumGoroutine(); n > baseline+1 {
		t.Fatalf("goroutine leak: baseline=%d now=%d", baseline, n)
	}
}

func TestBestMatch(t *testing.T) {
	results := []discovery.AlbumResult{
		{ProviderID: "wrong", ArtistName: "Other Artist", Title: "Other Album"},
		{ProviderID: "title-only", ArtistName: "Someone Else", Title: "Album One"},
		{ProviderID: "exact", ArtistName: "Artist One", Title: "album one"},
	}

	tests := []struct {
		name   string
		artist string
		title  string
		wantID string
		wantOK bool
	}{
		{name: "exact artist and title wins over title-only", artist: "Artist One", title: "Album One", wantID: "exact", wantOK: true},
		{name: "title-only falls back when artist differs", artist: "Nobody", title: "Album One", wantID: "title-only", wantOK: true},
		{name: "case and whitespace insensitive", artist: " artist one ", title: " ALBUM ONE ", wantID: "exact", wantOK: true},
		{name: "no match", artist: "Artist One", title: "Different", wantOK: false},
		{name: "empty title never matches", artist: "Artist One", title: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got, ok := bestMatch(results, tc.artist, tc.title)

			// Assert
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got.ProviderID != tc.wantID {
				t.Fatalf("provider = %q, want %q", got.ProviderID, tc.wantID)
			}
		})
	}
}

// TestService_SetOnMaterializedFiresAfterMaterialize verifies the completion
// hook fires once with the album id after a successful materialization — the
// signal the API wires to an SSE broadcast so the UI can invalidate.
func TestService_SetOnMaterializedFiresAfterMaterialize(t *testing.T) {
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &fakeProvider{
		name:          "alpha",
		searchResults: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:        []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
	}
	svc := newTestService(store, lib, Options{}, p)
	var got []int64
	svc.SetOnMaterialized(func(albumID int64) { got = append(got, albumID) })

	if _, err := svc.processBatch(context.Background()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("onMaterialized calls = %v, want exactly [42]", got)
	}
}

// TestService_SetOnMaterializedNotFiredOnUnmatched verifies the hook is not
// fired when no provider matched (nothing was materialized).
func TestService_SetOnMaterializedNotFiredOnUnmatched(t *testing.T) {
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &fakeProvider{name: "alpha"} // no search results → unmatched
	svc := newTestService(store, lib, Options{}, p)
	fired := false
	svc.SetOnMaterialized(func(int64) { fired = true })

	if _, err := svc.processBatch(context.Background()); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if fired {
		t.Fatal("onMaterialized fired without a materialize; it must fire only after success")
	}
}

// waitUntil polls cond until it is true or the timeout elapses, failing the
// test on timeout.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

// ─── Per-album in-flight coalescing (M3) ─────────────────────────────

// waitForAlbumInFlight polls until the service has registered an in-flight
// refresh for albumID, guarded by the service's own mutex.
func waitForAlbumInFlight(t *testing.T, svc *Service, albumID int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.inflightMu.Lock()
		_, ok := svc.inflight[albumID]
		svc.inflightMu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no in-flight refresh registered for album %d within timeout", albumID)
}

// TestService_RefreshAlbumConcurrentSameAlbumCoalescesToOneFetch proves the
// keyed in-flight guard: N concurrent refreshes of the SAME album share exactly
// one provider fetch, so the cold fetcher, the engine loop, and the warm job
// cannot duplicate provider work or clobber each other's metadata.
func TestService_RefreshAlbumConcurrentSameAlbumCoalescesToOneFetch(t *testing.T) {
	// Arrange
	store := newFakeStore()
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:  []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := newTestService(store, newColdAlbumLibrary(), Options{}, p)

	const n = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.RefreshAlbum(context.Background(), 42)
		}(i)
	}

	// Act
	close(start)
	waitForAlbumInFlight(t, svc, 42)
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider was never invoked")
	}
	// Let every racing caller join the registered call before releasing.
	time.Sleep(50 * time.Millisecond)
	close(p.release)
	wg.Wait()

	// Assert
	if got := atomic.LoadInt32(&p.searchN); got != 1 {
		t.Fatalf("SearchAlbums calls = %d, want exactly 1 for %d concurrent refreshes", got, n)
	}
	if got := atomic.LoadInt32(&p.trackN); got != 1 {
		t.Fatalf("GetAlbumTracks calls = %d, want exactly 1 for %d concurrent refreshes", got, n)
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("caller %d err = %v, want nil", i, errs[i])
		}
	}
	if got := albumInFlightCount(svc); got != 0 {
		t.Fatalf("in-flight refreshes = %d after completion, want 0", got)
	}
}

// albumInFlightCount reports how many album refreshes the service is coalescing.
func albumInFlightCount(svc *Service) int {
	svc.inflightMu.Lock()
	defer svc.inflightMu.Unlock()
	return len(svc.inflight)
}

// ─── Dead provider link (M4) ─────────────────────────────────────────

// TestService_RefreshAlbum_ClearsDeadProviderLink proves a stored link whose
// tracklist call fails with a non-rate-limit error is cleared so the next
// attempt falls back to a search instead of being stuck on the dead link.
func TestService_RefreshAlbum_ClearsDeadProviderLink(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.meta[42] = catalog.AlbumMetadata{
		AlbumID: 42, ProviderName: "alpha", ProviderAlbumID: "dead",
		RefreshStatus: catalog.StatusStale,
	}
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &fakeProvider{
		name:          "alpha",
		tracksErr:     errors.New("404 not found"),
		searchResults: []discovery.AlbumResult{{ProviderID: "pa2", ArtistName: "Artist One", Title: "Album One"}},
		tracks:        []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
	}
	svc := newTestService(store, lib, Options{}, p)

	// Act 1: the stored link 404s.
	if _, err := svc.RefreshAlbum(context.Background(), 42); err == nil {
		t.Fatal("first RefreshAlbum err = nil, want the provider error")
	}

	// Assert 1: the link is cleared and no search happened.
	if p.searchCalls != 0 {
		t.Fatalf("SearchAlbums calls = %d, want 0 (link path must not search)", p.searchCalls)
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), 42)
	if meta == nil || meta.ProviderAlbumID != "" || meta.ProviderName != "" {
		t.Fatalf("meta = %+v, want a cleared provider link", meta)
	}

	// Act 2: with no link, the retry searches and materializes.
	p.tracksErr = nil
	outcome, err := svc.RefreshAlbum(context.Background(), 42)

	// Assert 2.
	if err != nil {
		t.Fatalf("second RefreshAlbum: %v", err)
	}
	if !outcome.Matched || outcome.ProviderAlbumID != "pa2" {
		t.Fatalf("outcome = %+v, want matched pa2", outcome)
	}
	if p.searchCalls != 1 {
		t.Fatalf("SearchAlbums calls = %d, want 1 on the retry", p.searchCalls)
	}
	meta, _ = store.GetAlbumMetadata(context.Background(), 42)
	if meta.ProviderAlbumID != "pa2" || meta.RefreshStatus != catalog.StatusFresh {
		t.Fatalf("meta = %+v, want fresh link pa2", meta)
	}
}

// TestService_RefreshAlbum_RateLimitKeepsProviderLink proves a rate-limited
// tracklist call does NOT clear the link (the link is not at fault).
func TestService_RefreshAlbum_RateLimitKeepsProviderLink(t *testing.T) {
	store := newFakeStore()
	store.meta[42] = catalog.AlbumMetadata{
		AlbumID: 42, ProviderName: "alpha", ProviderAlbumID: "pa1",
		RefreshStatus: catalog.StatusStale,
	}
	p := &fakeProvider{name: "alpha", tracksErr: metadata.NewRateLimitError("alpha", time.Minute, "429")}
	svc := newTestService(store, &fakeLibrary{}, Options{}, p)

	if _, err := svc.RefreshAlbum(context.Background(), 42); !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), 42)
	if meta.ProviderAlbumID != "pa1" {
		t.Fatalf("rate limit cleared the link: %+v", meta)
	}
}

// ─── Duplicate provider track keys (#2) ──────────────────────────────

// TestService_MaterializeDisambiguatesDuplicateTrackKeys proves a provider
// tracklist with repeated/zero (disc, track) numbers is normalized and
// materialized without dropping rows — the catalog's UNIQUE(album_id,
// disc_number, track_number) must never abort the whole album on provider data.
func TestService_MaterializeDisambiguatesDuplicateTrackKeys(t *testing.T) {
	// Arrange: two tracks sharing (1,0) and two sharing (0,0).
	store := newFakeStore()
	p := &fakeProvider{
		name:          "alpha",
		searchResults: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks: []discovery.TrackInfo{
			{ProviderID: "t1", DiscNumber: 1, TrackNumber: 0, Title: "A"},
			{ProviderID: "t2", DiscNumber: 1, TrackNumber: 0, Title: "B"},
			{ProviderID: "t3", DiscNumber: 0, TrackNumber: 0, Title: "C"},
			{ProviderID: "t4", DiscNumber: 0, TrackNumber: 0, Title: "D"},
		},
	}
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	svc := newTestService(store, lib, Options{}, p)

	// Act
	outcome, err := svc.RefreshAlbum(context.Background(), 42)

	// Assert
	if err != nil {
		t.Fatalf("RefreshAlbum: %v", err)
	}
	if !outcome.Matched {
		t.Fatal("outcome not matched")
	}
	tracks, _ := store.GetAlbumTracks(context.Background(), 42)
	if len(tracks) != 4 {
		t.Fatalf("materialized tracks = %d, want 4 (normalization must not drop rows)", len(tracks))
	}
	seen := make(map[[2]int]bool, len(tracks))
	for _, tr := range tracks {
		key := [2]int{tr.DiscNumber, tr.TrackNumber}
		if seen[key] {
			t.Fatalf("duplicate (disc, track) key materialized: %v", key)
		}
		seen[key] = true
	}
}

// TestProviderTrackKeys is table-driven over the pure key normalizer: stable,
// total (one key per input), and collision-free.
func TestProviderTrackKeys(t *testing.T) {
	tests := []struct {
		name   string
		tracks []discovery.TrackInfo
		want   [][2]int
	}{
		{
			name:   "explicit numbers preserved",
			tracks: []discovery.TrackInfo{{DiscNumber: 1, TrackNumber: 2}, {DiscNumber: 1, TrackNumber: 1}},
			want:   [][2]int{{1, 2}, {1, 1}},
		},
		{
			name:   "zero disc defaults to 1 and zero track uses position",
			tracks: []discovery.TrackInfo{{DiscNumber: 0, TrackNumber: 0}, {TrackNumber: 0}},
			want:   [][2]int{{1, 1}, {1, 2}},
		},
		{
			name:   "duplicate keys disambiguate forward",
			tracks: []discovery.TrackInfo{{DiscNumber: 1, TrackNumber: 0}, {DiscNumber: 1, TrackNumber: 0}},
			want:   [][2]int{{1, 1}, {1, 2}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := providerTrackKeys(tc.tracks)
			if len(got) != len(tc.want) {
				t.Fatalf("keys = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("keys = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// ─── Enabled gate (F2) ───────────────────────────────────────────────

// TestService_EnabledGateSkipsDrain proves a disabled catalog skips the whole
// drain: no queue claim, no provider call.
func TestService_EnabledGateSkipsDrain(t *testing.T) {
	store := newFakeStore()
	store.enqueue(dueAlbumItem(42))
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &fakeProvider{
		name:          "alpha",
		searchResults: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:        []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
	}
	svc := newTestService(store, lib, Options{}, p)
	svc.SetEnabledFunc(func() bool { return false })

	handled, err := svc.processBatch(context.Background())

	if err != nil || handled != 0 {
		t.Fatalf("handled=%d err=%v, want 0/nil when disabled", handled, err)
	}
	if len(store.queue) != 1 {
		t.Fatalf("queue = %d rows, want the item untouched", len(store.queue))
	}
	if p.searchCalls != 0 || p.tracksCalls != 0 {
		t.Fatalf("provider called while disabled: search=%d tracks=%d", p.searchCalls, p.tracksCalls)
	}
}

// ─── Soft TTL accessor (#8) ──────────────────────────────────────────

// TestService_AlbumStaleAfter covers the Tier-1 staleness accessor the api read
// path consumes: the live override, the default fallback, and nil-safety.
func TestService_AlbumStaleAfter(t *testing.T) {
	svc := newTestService(newFakeStore(), &fakeLibrary{}, Options{})
	if got := svc.AlbumStaleAfter(); got != DefaultAlbumStaleAfter {
		t.Fatalf("default AlbumStaleAfter = %v, want %v", got, DefaultAlbumStaleAfter)
	}

	svc.SetOptionsFunc(func() Options { return Options{StaleAfter: 7 * 24 * time.Hour} })
	if got := svc.AlbumStaleAfter(); got != 7*24*time.Hour {
		t.Fatalf("live AlbumStaleAfter = %v, want 7d", got)
	}

	svc.SetOptionsFunc(func() Options { return Options{} })
	if got := svc.AlbumStaleAfter(); got != DefaultAlbumStaleAfter {
		t.Fatalf("zero live StaleAfter = %v, want default", got)
	}

	var nilSvc *Service
	if got := nilSvc.AlbumStaleAfter(); got != DefaultAlbumStaleAfter {
		t.Fatalf("nil service AlbumStaleAfter = %v, want default", got)
	}
}
