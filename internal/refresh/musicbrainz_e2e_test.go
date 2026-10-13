package refresh

import (
	"context"
	"testing"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// TestService_MusicBrainzDiscoveryMaterializesUnmatchedAlbum is the end-to-end
// regression guard for the album-359 class: a local album with no stored
// provider link and no materialized tracks (MusicBrainz-only metadata) must be
// materialized by the catalog refresh engine once MusicBrainz registers as a
// discovery.Provider (Phase 3). Before Phase 3 the engine selected discovery
// providers in a non-deterministic registry order and MusicBrainz was not a
// discovery provider at all, so such an album stayed unmatched forever.
//
// It drives the real engine entry point (processBatch) end-to-end through the
// persisted queue — not just RefreshAlbum — so it also pins the queue
// bookkeeping: a successful materialization marks the album fresh and drains the
// queue row.
func TestService_MusicBrainzDiscoveryMaterializesUnmatchedAlbum(t *testing.T) {
	// Arrange: album 359 "Turned Away" by "The Things", queued for refresh,
	// with no provider link and no tracks yet ("unmatched").
	const albumID int64 = 359
	store := newFakeStore()
	store.enqueue(dueAlbumItem(albumID))
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{albumID: {ID: albumID, ArtistID: 11, Title: "Turned Away"}},
		artists: map[int64]*domain.Artist{11: {ID: 11, Name: "The Things"}},
	}
	// A fake discovery.Provider standing in for MusicBrainz: SearchAlbums
	// returns the release group, GetAlbumTracks returns its tracklist. The
	// provider album id is a release-group MBID (the value the engine stores as
	// the provider link and passes back to GetAlbumTracks).
	mb := &fakeProvider{
		name: "musicbrainz",
		searchResults: []discovery.AlbumResult{{
			ProviderID:   "rg-359-abc",
			ProviderName: "musicbrainz",
			ArtistName:   "The Things",
			Title:        "Turned Away",
			Type:         "album",
		}},
		tracks: []discovery.TrackInfo{
			{ProviderID: "rec-1", ArtistName: "The Things", AlbumTitle: "Turned Away", Title: "One", DiscNumber: 1, TrackNumber: 1, DurationMs: 180_000},
			{ProviderID: "rec-2", ArtistName: "The Things", AlbumTitle: "Turned Away", Title: "Two", DiscNumber: 1, TrackNumber: 2, DurationMs: 200_000},
		},
	}
	svc := newTestService(store, lib, Options{}, mb)

	// Act: drain the queue exactly as the background engine does.
	handled, err := svc.processBatch(context.Background())

	// Assert: the MB-only album is materialized.
	if err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if handled != 1 {
		t.Fatalf("handled = %d, want 1", handled)
	}
	if _, stillQueued := store.queue[1]; stillQueued {
		t.Fatal("queue row still present; a materialized album must drain")
	}
	meta, _ := store.GetAlbumMetadata(context.Background(), albumID)
	if meta == nil {
		t.Fatal("album metadata missing after refresh")
	}
	if meta.RefreshStatus != catalog.StatusFresh {
		t.Fatalf("refresh status = %q, want fresh", meta.RefreshStatus)
	}
	if meta.ProviderName != "musicbrainz" || meta.ProviderAlbumID != "rg-359-abc" {
		t.Fatalf("provider link = %s/%s, want musicbrainz/rg-359-abc", meta.ProviderName, meta.ProviderAlbumID)
	}
	tracks, _ := store.GetAlbumTracks(context.Background(), albumID)
	if len(tracks) != 2 {
		t.Fatalf("materialized album_tracks = %d, want 2", len(tracks))
	}
	for _, tr := range tracks {
		if tr.Origin != catalog.OriginProvider {
			t.Fatalf("track %q origin = %q, want provider", tr.Title, tr.Origin)
		}
		if tr.ProviderName != "musicbrainz" {
			t.Fatalf("track %q provider = %q, want musicbrainz", tr.Title, tr.ProviderName)
		}
	}
	if mb.searchCalls != 1 || mb.tracksCalls != 1 {
		t.Fatalf("provider calls search=%d tracks=%d, want 1/1", mb.searchCalls, mb.tracksCalls)
	}
}

// TestService_DiscoveryIterationOrderIsDeterministic pins the Phase 1 fix at the
// engine boundary: with the shared provider order wired, the engine tries the
// configured discovery providers in that order on every album, so the same
// provider wins deterministically. The old
// plugin.Registry.WithCapability iteration over a map did not guarantee this.
//
// Two discovery providers can both materialize the album; the injected slice is
// deliberately in the "wrong" order and the shared order names musicbrainz
// first, so musicbrainz must win and the decoy must never be searched.
func TestService_DiscoveryIterationOrderIsDeterministic(t *testing.T) {
	// Arrange
	lib := &fakeLibrary{
		albums:  map[int64]*domain.Album{42: {ID: 42, ArtistID: 7, Title: "Album One"}},
		artists: map[int64]*domain.Artist{7: {ID: 7, Name: "Artist One"}},
	}
	deezer := &fakeProvider{
		name:          "deezer",
		searchResults: []discovery.AlbumResult{{ProviderID: "dz-1", ProviderName: "deezer", ArtistName: "Artist One", Title: "Album One"}},
		tracks:        []discovery.TrackInfo{{ProviderID: "d1", DiscNumber: 1, TrackNumber: 1, Title: "D"}},
	}
	mb := &fakeProvider{
		name:          "musicbrainz",
		searchResults: []discovery.AlbumResult{{ProviderID: "rg-1", ProviderName: "musicbrainz", ArtistName: "Artist One", Title: "Album One"}},
		tracks:        []discovery.TrackInfo{{ProviderID: "m1", DiscNumber: 1, TrackNumber: 1, Title: "M"}},
	}
	// Inject them in the "wrong" order to prove the shared order, not the
	// providers-closure order, drives the iteration.
	svc := newTestService(newFakeStore(), lib, Options{}, deezer, mb)
	svc.SetProviderOrder(metadata.NewProviderOrder(func() []string {
		return []string{"musicbrainz", "deezer"}
	}))

	// Act: repeat to catch any non-deterministic ordering.
	for i := 0; i < 10; i++ {
		outcome, err := svc.RefreshAlbum(context.Background(), 42)

		// Assert
		if err != nil {
			t.Fatalf("run %d RefreshAlbum: %v", i, err)
		}
		if !outcome.Matched || outcome.ProviderName != "musicbrainz" {
			t.Fatalf("run %d outcome = %+v, want musicbrainz to win by the shared order", i, outcome)
		}
		if deezer.searchCalls != 0 {
			t.Fatalf("deezer searched %d times; the shared order must place musicbrainz first", deezer.searchCalls)
		}
	}
}
