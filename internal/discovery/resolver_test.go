package discovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/metadata"
)

// fakeReleaseResolver is a configurable ReleaseResolver test double. It records
// how many times ResolveTracksForCount was actually called so a test can prove
// the decorator short-circuited a cooled-down provider.
type fakeReleaseResolver struct {
	name string
	// tracks/mbid/err are returned by ResolveTracksForCount.
	tracks []TrackInfo
	mbid   string
	err    error

	mu     sync.Mutex
	called int
}

var _ ReleaseResolver = (*fakeReleaseResolver)(nil)

func (f *fakeReleaseResolver) Name() string                          { return f.name }
func (f *fakeReleaseResolver) DisplayName() string                   { return "display:" + f.name }
func (f *fakeReleaseResolver) IsConfigured() bool                    { return true }
func (f *fakeReleaseResolver) CheckConnection(context.Context) error { return nil }
func (f *fakeReleaseResolver) Connected() bool                       { return true }
func (f *fakeReleaseResolver) CapabilityStatus() map[string]string {
	return map[string]string{"metadata": "connected"}
}

func (f *fakeReleaseResolver) ResolveTracksForCount(context.Context, string, string, int, string) ([]TrackInfo, string, error) {
	f.mu.Lock()
	f.called++
	f.mu.Unlock()
	return f.tracks, f.mbid, f.err
}

func (f *fakeReleaseResolver) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

// fakeCooldown records the shared-bucket interaction the decorator performs.
type fakeCooldown struct {
	mu      sync.Mutex
	cooling map[string]bool
	marked  map[string]time.Duration
}

var _ Cooldown = (*fakeCooldown)(nil)

func newFakeCooldown() *fakeCooldown {
	return &fakeCooldown{cooling: map[string]bool{}, marked: map[string]time.Duration{}}
}

func (c *fakeCooldown) CoolingDown(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cooling[name]
}

func (c *fakeCooldown) MarkAfter(name string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.marked[name] = d
}

func (c *fakeCooldown) markedWith(name string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.marked[name]
	return d, ok
}

func TestNewCooldownReleaseResolver_NilInnerYieldsNil(t *testing.T) {
	if got := NewCooldownReleaseResolver(nil, newFakeCooldown()); got != nil {
		t.Fatalf("NewCooldownReleaseResolver(nil, c) = %v, want nil", got)
	}
}

func TestCooldownReleaseResolver_DelegatesBasePluginAndName(t *testing.T) {
	inner := &fakeReleaseResolver{name: "musicbrainz"}
	d := NewCooldownReleaseResolver(inner, newFakeCooldown())

	if got := d.Name(); got != "musicbrainz" {
		t.Errorf("Name() = %q, want the inner provider name (bucket keying)", got)
	}
	if got := d.DisplayName(); got != "display:musicbrainz" {
		t.Errorf("DisplayName() = %q, want delegated", got)
	}
	if !d.IsConfigured() || !d.Connected() {
		t.Error("IsConfigured/Connected did not delegate")
	}
	if got := d.CapabilityStatus()["metadata"]; got != "connected" {
		t.Errorf("CapabilityStatus() = %v, want delegated map", got)
	}
	if err := d.CheckConnection(context.Background()); err != nil {
		t.Errorf("CheckConnection() = %v, want delegated nil", err)
	}
}

func TestCooldownReleaseResolver_SkipsWhenCooling(t *testing.T) {
	inner := &fakeReleaseResolver{name: "musicbrainz"}
	cool := newFakeCooldown()
	cool.cooling["musicbrainz"] = true
	d := NewCooldownReleaseResolver(inner, cool)

	_, _, err := d.ResolveTracksForCount(context.Background(), "artist", "album", 0, "")
	if err == nil {
		t.Fatal("ResolveTracksForCount() = nil error, want a cooling-down error")
	}
	if inner.callCount() != 0 {
		t.Errorf("inner called %d times, want 0 (must not probe a cooled-down provider)", inner.callCount())
	}
}

func TestCooldownReleaseResolver_MarksOnRateLimitWithRetryAfter(t *testing.T) {
	inner := &fakeReleaseResolver{
		name: "musicbrainz",
		err:  metadata.NewRateLimitError("musicbrainz", 90*time.Second, "503"),
	}
	cool := newFakeCooldown()
	d := NewCooldownReleaseResolver(inner, cool)

	_, _, err := d.ResolveTracksForCount(context.Background(), "artist", "album", 0, "")
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("err = %v, want it to still wrap ErrRateLimited", err)
	}
	got, ok := cool.markedWith("musicbrainz")
	if !ok {
		t.Fatal("MarkAfter was not called on a rate-limited resolve")
	}
	if got != 90*time.Second {
		t.Errorf("MarkAfter duration = %v, want the server RetryAfter 90s", got)
	}
}

func TestCooldownReleaseResolver_MarksOnBareSentinel(t *testing.T) {
	inner := &fakeReleaseResolver{name: "musicbrainz", err: metadata.ErrRateLimited}
	cool := newFakeCooldown()
	d := NewCooldownReleaseResolver(inner, cool)

	_, _, _ = d.ResolveTracksForCount(context.Background(), "artist", "album", 0, "")

	got, ok := cool.markedWith("musicbrainz")
	if !ok {
		t.Fatal("MarkAfter was not called for a bare ErrRateLimited")
	}
	if got != 0 {
		t.Errorf("MarkAfter duration = %v, want 0 (default cooldown) for a bare sentinel", got)
	}
}

func TestCooldownReleaseResolver_PassesThroughNonRateLimit(t *testing.T) {
	wantTracks := []TrackInfo{{Title: "t1", TrackNumber: 1}}
	inner := &fakeReleaseResolver{
		name:   "musicbrainz",
		tracks: wantTracks,
		mbid:   "release-mbid",
		err:    errors.New("boom"),
	}
	cool := newFakeCooldown()
	d := NewCooldownReleaseResolver(inner, cool)

	tracks, mbid, err := d.ResolveTracksForCount(context.Background(), "artist", "album", 3, "torrent")
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want the inner non-rate-limit error passed through", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "t1" || mbid != "release-mbid" {
		t.Errorf("result = (%v, %q), want the inner result passed through", tracks, mbid)
	}
	if _, ok := cool.markedWith("musicbrainz"); ok {
		t.Error("a non-rate-limit error must not mark the provider cooling")
	}
	if inner.callCount() != 1 {
		t.Errorf("inner called %d times, want 1", inner.callCount())
	}
}

func TestCooldownReleaseResolver_SuccessUnchanged(t *testing.T) {
	inner := &fakeReleaseResolver{name: "musicbrainz", tracks: []TrackInfo{{Title: "ok"}}, mbid: "m"}
	cool := newFakeCooldown()
	d := NewCooldownReleaseResolver(inner, cool)

	tracks, mbid, err := d.ResolveTracksForCount(context.Background(), "a", "b", 1, "")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(tracks) != 1 || mbid != "m" {
		t.Errorf("result = (%v, %q), want inner result", tracks, mbid)
	}
	if _, ok := cool.markedWith("musicbrainz"); ok {
		t.Error("a successful resolve must not mark the provider")
	}
}

func TestCooldownReleaseResolver_NilCooldownIsTransparent(t *testing.T) {
	inner := &fakeReleaseResolver{name: "musicbrainz", err: metadata.ErrRateLimited}
	d := NewCooldownReleaseResolver(inner, nil)

	// Must not panic on the nil bucket and must still delegate.
	_, _, err := d.ResolveTracksForCount(context.Background(), "a", "b", 0, "")
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("err = %v, want the inner error passed through", err)
	}
}
