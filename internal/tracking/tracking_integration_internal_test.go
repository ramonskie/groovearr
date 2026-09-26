package tracking

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
)

// TestTrackingIntegrationAddArtistTimeoutLeavesNoOrphan proves the synchronous
// fetch budget: a provider that hangs past addArtistTimeout fails AddArtist
// before any write, so no tracked artist row (and no orphan) is created (N3).
//
// This case stays in the internal tracking package because it needs the
// unexported addArtistTimeout seam. It uses the in-memory mockStore rather than
// the real SQLite store: a same-package test cannot import tracking/sqlite
// (which wraps this package) without an import cycle, and the assertion — that
// no row is written when the bounded fetch fails — is about AddArtist's write
// ordering, identical on either store.
func TestTrackingIntegrationAddArtistTimeoutLeavesNoOrphan(t *testing.T) {
	prev := addArtistTimeout
	addArtistTimeout = 20 * time.Millisecond
	defer func() { addArtistTimeout = prev }()

	ctx := context.Background()
	provider := &fakeDiscoveryProvider{name: "deezer", blockAlbums: true}
	store := newMockStore()
	svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, newFakeRateLimiter(), nil, config.DefaultConfig())

	_, err := svc.AddArtist(ctx, "deezer", "dz-artist-3", "Slow Artist", AddArtistOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AddArtist error = %v, want context.DeadlineExceeded", err)
	}
	if provider.albumCalls != 1 {
		t.Errorf("provider album calls = %d, want 1", provider.albumCalls)
	}

	artists, err := store.ListTrackedArtists(ctx)
	if err != nil {
		t.Fatalf("ListTrackedArtists: %v", err)
	}
	if len(artists) != 0 {
		t.Fatalf("tracked artists = %d, want 0 (no orphan row on fetch failure)", len(artists))
	}
}
