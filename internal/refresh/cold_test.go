package refresh

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
)

// syncProvider is a discovery.Provider with atomic call counters and an
// optional block gate, so a concurrency test can hold a fetch in flight and
// assert exactly how many provider calls happened.
type syncProvider struct {
	name    string
	results []discovery.AlbumResult
	tracks  []discovery.TrackInfo

	entered chan struct{} // closed once, on the first provider call
	release chan struct{} // calls block until closed; nil means never block

	searchN int32
	trackN  int32
	once    sync.Once
}

func (p *syncProvider) Name() string                          { return p.name }
func (p *syncProvider) DisplayName() string                   { return p.name }
func (p *syncProvider) IsConfigured() bool                    { return true }
func (p *syncProvider) CheckConnection(context.Context) error { return nil }
func (p *syncProvider) Connected() bool                       { return true }
func (p *syncProvider) CapabilityStatus() map[string]string   { return nil }
func (p *syncProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	return nil, nil
}
func (p *syncProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}

func (p *syncProvider) signalEntered() {
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
	}
}

// block waits for the release gate (when set) or the call's context, whichever
// comes first, so a test can bound a fetch either explicitly or by timeout.
func (p *syncProvider) block(ctx context.Context) error {
	if p.release == nil {
		return nil
	}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *syncProvider) SearchAlbums(ctx context.Context, _ discovery.AlbumQuery, _ int) ([]discovery.AlbumResult, error) {
	atomic.AddInt32(&p.searchN, 1)
	p.signalEntered()
	if err := p.block(ctx); err != nil {
		return nil, err
	}
	return p.results, nil
}

func (p *syncProvider) GetAlbumTracks(ctx context.Context, _ string) ([]discovery.TrackInfo, error) {
	atomic.AddInt32(&p.trackN, 1)
	p.signalEntered()
	if err := p.block(ctx); err != nil {
		return nil, err
	}
	return p.tracks, nil
}

var _ discovery.Provider = (*syncProvider)(nil)

// newColdAlbumLibrary is the single-album library the cold tests fetch.
func newColdAlbumLibrary() *fakeLibrary {
	return &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
}

// inFlightCount reports how many cold calls the fetcher is currently tracking,
// guarded by the fetcher's own mutex so it does not race the run goroutine.
func inFlightCount(c *ColdFetcher) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inflight)
}

// waitForInFlight polls until the fetcher has registered an in-flight call for
// albumID, so a test can release a blocked provider only after the shared call
// exists.
func waitForInFlight(t *testing.T, c *ColdFetcher, albumID int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		_, ok := c.inflight[albumID]
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no in-flight cold call registered for album %d within timeout", albumID)
}

// TestColdFetcher_SuccessMaterializesAndReturnsOutcome covers the happy path:
// one provider fetch materializes the album and returns a matched outcome.
func TestColdFetcher_SuccessMaterializesAndReturnsOutcome(t *testing.T) {
	// Arrange
	store := newFakeStore()
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks: []discovery.TrackInfo{
			{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"},
			{ProviderID: "t2", DiscNumber: 1, TrackNumber: 2, Title: "Song 2"},
		},
	}
	c := NewColdFetcher(newTestService(store, newColdAlbumLibrary(), Options{}, p))

	// Act
	outcome, err := c.Fetch(context.Background(), 42)

	// Assert
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !outcome.Matched || outcome.ProviderName != "alpha" || outcome.ProviderAlbumID != "pa1" {
		t.Fatalf("outcome = %+v, want a matched alpha/pa1", outcome)
	}
	tracks, _ := store.GetAlbumTracks(context.Background(), 42)
	if len(tracks) != 2 {
		t.Fatalf("materialized tracks = %d, want 2", len(tracks))
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), 42)
	if meta == nil || meta.RefreshStatus != catalog.StatusFresh {
		t.Fatalf("meta = %+v, want fresh", meta)
	}
	if got := atomic.LoadInt32(&p.searchN); got != 1 {
		t.Fatalf("SearchAlbums calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&p.trackN); got != 1 {
		t.Fatalf("GetAlbumTracks calls = %d, want 1", got)
	}
}

// TestColdFetcher_ConcurrentOpensDedupeToOneFetch proves the singleflight
// contract: N goroutines opening the same cold album share exactly one provider
// fetch.
func TestColdFetcher_ConcurrentOpensDedupeToOneFetch(t *testing.T) {
	// Arrange
	store := newFakeStore()
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:  []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := NewColdFetcher(newTestService(store, newColdAlbumLibrary(), Options{}, p))
	c.timeout = 5 * time.Second

	const n = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	outcomes := make([]AlbumOutcome, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i], errs[i] = c.Fetch(context.Background(), 42)
		}(i)
	}

	// Act
	close(start)
	waitForInFlight(t, c, 42)
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider was never invoked")
	}
	// Give every racing caller time to join the registered call while the
	// provider is still blocked, then release it.
	time.Sleep(50 * time.Millisecond)
	close(p.release)
	wg.Wait()

	// Assert
	if got := atomic.LoadInt32(&p.searchN); got != 1 {
		t.Fatalf("SearchAlbums calls = %d, want exactly 1 for %d concurrent opens", got, n)
	}
	if got := atomic.LoadInt32(&p.trackN); got != 1 {
		t.Fatalf("GetAlbumTracks calls = %d, want exactly 1 for %d concurrent opens", got, n)
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("caller %d err = %v, want nil", i, errs[i])
		}
		if !outcomes[i].Matched || outcomes[i].ProviderAlbumID != "pa1" {
			t.Fatalf("caller %d outcome = %+v, want the shared matched result", i, outcomes[i])
		}
	}
}

// TestColdFetcher_TimeoutFallsBackPromptly proves the hard timeout bounds the
// fetch: a provider that blocks forever makes Fetch return a deadline error
// promptly and materializes nothing, so the caller can fall back to local
// tracks.
func TestColdFetcher_TimeoutFallsBackPromptly(t *testing.T) {
	// Arrange
	store := newFakeStore()
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		release: make(chan struct{}), // never closed: every call blocks until ctx
	}
	c := NewColdFetcher(newTestService(store, newColdAlbumLibrary(), Options{}, p))
	c.timeout = 50 * time.Millisecond

	// Act
	type result struct {
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, err := c.Fetch(context.Background(), 42)
		done <- result{err: err}
	}()

	// Assert
	select {
	case res := <-done:
		if !errors.Is(res.err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch hung past its hard timeout instead of falling back")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Fetch took %v, want a prompt timeout fallback", elapsed)
	}
	if tracks, _ := store.GetAlbumTracks(context.Background(), 42); len(tracks) != 0 {
		t.Fatalf("materialized tracks = %d, want 0 after a timed-out fetch", len(tracks))
	}
	if got := inFlightCount(c); got != 0 {
		t.Fatalf("in-flight calls = %d after completion, want 0", got)
	}
}

// TestColdFetcher_DistinctAlbumsAreNotDeduped proves the in-flight map is keyed
// per album: two different albums fetch independently (two provider calls) even
// when concurrent.
func TestColdFetcher_DistinctAlbumsAreNotDeduped(t *testing.T) {
	// Arrange
	store := newFakeStore()
	// Both albums share a title so one generic provider result matches both.
	lib := &fakeLibrary{
		albums: map[int64]*domain.Album{
			42: {ID: 42, ArtistID: 7, Title: "Album One"},
			43: {ID: 43, ArtistID: 7, Title: "Album One"},
		},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:  []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
		release: make(chan struct{}),
	}
	c := NewColdFetcher(newTestService(store, lib, Options{}, p))
	c.timeout = 5 * time.Second

	var wg sync.WaitGroup
	for _, albumID := range []int64{42, 43} {
		wg.Add(1)
		go func(albumID int64) {
			defer wg.Done()
			if _, err := c.Fetch(context.Background(), albumID); err != nil {
				t.Errorf("album %d Fetch: %v", albumID, err)
			}
		}(albumID)
	}

	// Act: wait until BOTH albums have their own in-flight call, then release.
	deadline := time.Now().Add(2 * time.Second)
	for inFlightCount(c) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := inFlightCount(c); got != 2 {
		t.Fatalf("in-flight calls = %d, want 2 (one per album)", got)
	}
	close(p.release)
	wg.Wait()

	// Assert
	if got := atomic.LoadInt32(&p.searchN); got != 2 {
		t.Fatalf("SearchAlbums calls = %d, want 2 (one per distinct album)", got)
	}
	if got := atomic.LoadInt32(&p.trackN); got != 2 {
		t.Fatalf("GetAlbumTracks calls = %d, want 2 (one per distinct album)", got)
	}
	for _, albumID := range []int64{42, 43} {
		tracks, _ := store.GetAlbumTracks(context.Background(), albumID)
		if len(tracks) != 1 {
			t.Fatalf("album %d materialized tracks = %d, want 1", albumID, len(tracks))
		}
	}
}

// TestColdFetcher_ReusesSharedCooldown proves the fetch path consults the
// shared provider cooldown: a cooling provider is never called and the fetch
// reports the same transient outcome the engine uses.
func TestColdFetcher_ReusesSharedCooldown(t *testing.T) {
	// Arrange
	store := newFakeStore()
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:  []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
	}
	svc := newTestService(store, newColdAlbumLibrary(), Options{}, p)
	cd := newFakeCooldown()
	cd.cooling["alpha"] = true
	svc.cooldown = cd
	c := NewColdFetcher(svc)

	// Act
	_, err := c.Fetch(context.Background(), 42)

	// Assert
	if !errors.Is(err, errAllCooling) {
		t.Fatalf("err = %v, want errAllCooling", err)
	}
	if got := atomic.LoadInt32(&p.searchN); got != 0 {
		t.Fatalf("SearchAlbums calls = %d, want 0 while the provider is cooling", got)
	}
	if got := atomic.LoadInt32(&p.trackN); got != 0 {
		t.Fatalf("GetAlbumTracks calls = %d, want 0 while the provider is cooling", got)
	}
}

// TestColdFetcher_DisabledReturnsSentinel proves a disabled catalog skips the
// cold fetch entirely and returns a sentinel the handler maps to a local-tracks
// fallback.
func TestColdFetcher_DisabledReturnsSentinel(t *testing.T) {
	// Arrange
	store := newFakeStore()
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		tracks:  []discovery.TrackInfo{{ProviderID: "t1", DiscNumber: 1, TrackNumber: 1, Title: "Song 1"}},
	}
	svc := newTestService(store, newColdAlbumLibrary(), Options{}, p)
	svc.SetEnabledFunc(func() bool { return false })
	c := NewColdFetcher(svc)

	// Act
	_, err := c.Fetch(context.Background(), 42)

	// Assert
	if !errors.Is(err, errColdDisabled) {
		t.Fatalf("err = %v, want errColdDisabled", err)
	}
	if got := atomic.LoadInt32(&p.searchN); got != 0 {
		t.Fatalf("SearchAlbums calls = %d, want 0 while disabled", got)
	}
	if got := atomic.LoadInt32(&p.trackN); got != 0 {
		t.Fatalf("GetAlbumTracks calls = %d, want 0 while disabled", got)
	}
}

// TestColdFetcher_StopDrainsInFlight proves Stop cancels the lifecycle context
// and waits for the in-flight cold goroutine: a Fetch blocked on a provider
// returns promptly with a context error and the fetcher is left empty, and a
// later Fetch is rejected.
func TestColdFetcher_StopDrainsInFlight(t *testing.T) {
	// Arrange
	store := newFakeStore()
	p := &syncProvider{
		name:    "alpha",
		results: []discovery.AlbumResult{{ProviderID: "pa1", ArtistName: "Artist One", Title: "Album One"}},
		release: make(chan struct{}), // never closed: blocks until the context ends
	}
	c := NewColdFetcher(newTestService(store, newColdAlbumLibrary(), Options{}, p))
	c.timeout = 5 * time.Second
	c.Start(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := c.Fetch(context.Background(), 42)
		done <- err
	}()
	waitForInFlight(t, c, 42)

	// Act
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()

	// Assert: Stop returns promptly (the blocked goroutine is cancelled), and
	// the caller's Fetch unblocks with a context error rather than hanging.
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return; the in-flight cold goroutine was not drained")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Fetch err = %v, want a context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch did not return after Stop cancelled the lifecycle")
	}
	if got := inFlightCount(c); got != 0 {
		t.Fatalf("in-flight calls = %d after Stop, want 0", got)
	}
	if _, err := c.Fetch(context.Background(), 43); !errors.Is(err, errColdUnavailable) {
		t.Fatalf("post-Stop Fetch err = %v, want errColdUnavailable", err)
	}
}
