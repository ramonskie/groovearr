package tracking

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// ─── AddArtist / ReconcileAlbums ──────────────────────────────────────

func TestAddArtistCreatesAndReconciles(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{
		name: "deezer",
		albums: []discovery.AlbumResult{
			{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
		},
	}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())

	artist, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	if artist.ID == 0 {
		t.Fatal("expected assigned artist ID")
	}
	if !artist.Monitored || artist.MonitorMode != domain.MonitorModeAll {
		t.Fatalf("monitor = %v/%q, want true/all", artist.Monitored, artist.MonitorMode)
	}
	if provider.albumCalls != 1 {
		t.Fatalf("album calls = %d, want 1", provider.albumCalls)
	}

	albums, _ := store.ListTrackedAlbums(context.Background(), artist.ID)
	if len(albums) != 1 {
		t.Fatalf("tracked albums = %d, want 1", len(albums))
	}
	if albums[0].Status != domain.AlbumStatusWanted || !albums[0].Monitored {
		t.Fatalf("album = %q/%v, want wanted/monitored", albums[0].Status, albums[0].Monitored)
	}
	if store.touchCalls != 1 {
		t.Fatalf("touch calls = %d, want 1", store.touchCalls)
	}
}

func TestAddArtistIdempotentReAdd(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())
	ctx := context.Background()

	first, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	monitored := false
	second, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{MonitorMode: domain.MonitorModeNone, Monitored: &monitored})
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second add created new row: %d != %d", second.ID, first.ID)
	}
	if len(store.artists) != 1 {
		t.Fatalf("artist rows = %d, want 1", len(store.artists))
	}
	if second.Monitored || second.MonitorMode != domain.MonitorModeNone {
		t.Fatalf("second monitor = %v/%q, want false/none", second.Monitored, second.MonitorMode)
	}
	if store.monitorCalls != 1 {
		t.Fatalf("monitor update calls = %d, want 1", store.monitorCalls)
	}
}

// TestAddArtistReAddUpdatesName pins MINOR 1: re-adding an existing provider
// pair with a corrected name persists it on the existing row.
func TestAddArtistReAddUpdatesName(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())
	ctx := context.Background()

	first, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	second, err := svc.AddArtist(ctx, "deezer", "art1", "Tool (US)", AddArtistOptions{})
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-add created a new row: %d != %d", second.ID, first.ID)
	}
	if second.Name != "Tool (US)" {
		t.Fatalf("returned name = %q, want Tool (US)", second.Name)
	}
	if store.nameUpdateCalls != 1 {
		t.Fatalf("UpdateArtistName calls = %d, want 1", store.nameUpdateCalls)
	}
	got, _ := store.GetTrackedArtist(ctx, first.ID)
	if got.Name != "Tool (US)" {
		t.Fatalf("stored name = %q, want Tool (US)", got.Name)
	}
}

// TestAddArtistReAddSameNameSkipsNameUpdate proves the store is not written when
// the provider repeats the name already on the row.
func TestAddArtistReAddSameNameSkipsNameUpdate(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())
	ctx := context.Background()

	if _, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if store.nameUpdateCalls != 0 {
		t.Fatalf("UpdateArtistName calls = %d, want 0 for an unchanged name", store.nameUpdateCalls)
	}
}

// TestAddArtistReAddNameChangePreservesMonitor proves a name correction does not
// disturb the user's monitor state: identical monitor options leave it untouched.
func TestAddArtistReAddNameChangePreservesMonitor(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())
	ctx := context.Background()
	monitored := false
	opts := AddArtistOptions{MonitorMode: domain.MonitorModeNone, Monitored: &monitored}

	first, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", opts)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	second, err := svc.AddArtist(ctx, "deezer", "art1", "Tool (US)", opts)
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if store.nameUpdateCalls != 1 {
		t.Fatalf("UpdateArtistName calls = %d, want 1", store.nameUpdateCalls)
	}
	if store.monitorCalls != 0 {
		t.Fatalf("UpdateArtistMonitor calls = %d, want 0 (monitor unchanged)", store.monitorCalls)
	}
	if second.ID != first.ID || second.Monitored || second.MonitorMode != domain.MonitorModeNone {
		t.Fatalf("re-add = id %d monitor %v/%q, want id %d and preserved false/none",
			second.ID, second.Monitored, second.MonitorMode, first.ID)
	}
}

// TestAddArtistSearchOnAddGate proves the opt-in post-add search: with
// SearchOnAdd true the canonical queuer is invoked for the wanted album; with
// the default false the add only builds the wanted list.
func TestAddArtistSearchOnAddGate(t *testing.T) {
	tests := []struct {
		name        string
		searchOnAdd bool
		wantQueued  int
	}{
		{name: "opt-in queues missing albums", searchOnAdd: true, wantQueued: 1},
		{name: "default off builds only the wanted list", searchOnAdd: false, wantQueued: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			provider := &fakeDiscoveryProvider{name: "deezer", albums: []discovery.AlbumResult{
				{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
			}}
			q := &fakeQueuer{result: queuedAlbumResult()}
			svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, configWithDownloadClient())

			artist, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{SearchOnAdd: tc.searchOnAdd})
			if err != nil {
				t.Fatalf("AddArtist: %v", err)
			}
			if artist == nil || artist.ID == 0 {
				t.Fatalf("artist = %+v, want the persisted artist", artist)
			}
			if len(q.calls) != tc.wantQueued {
				t.Fatalf("queuer calls = %d, want %d", len(q.calls), tc.wantQueued)
			}
		})
	}
}

// TestAddArtistSearchOnAddFailureNonFatal proves a failed post-add search does
// not fail the add: the artist row and wanted list are already persisted.
func TestAddArtistSearchOnAddFailureNonFatal(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer", albums: []discovery.AlbumResult{
		{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
	}}
	// No queuer: SearchMissing fails immediately with "queuer not configured".
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, configWithDownloadClient())

	artist, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{SearchOnAdd: true})
	if err != nil {
		t.Fatalf("AddArtist: %v (search failure must be non-fatal)", err)
	}
	if artist == nil || artist.ID == 0 {
		t.Fatalf("artist = %+v, want a persisted artist despite search failure", artist)
	}
}

func TestReconcileMatchesLibraryByExternalID(t *testing.T) {
	store := newMockStore()
	lib := newMockLibraryStore()
	lib.albumByExt["deezer/a1"] = &domain.Album{ID: 42, Title: "Lateralus", Year: 2001}
	provider := &fakeDiscoveryProvider{
		name: "deezer",
		albums: []discovery.AlbumResult{
			{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
		},
	}
	svc := newTestService(t, store, lib, provider, nil, config.DefaultConfig())
	ctx := context.Background()

	artist, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	albums, _ := store.ListTrackedAlbums(ctx, artist.ID)
	if len(albums) != 1 || albums[0].LibraryAlbumID == nil || *albums[0].LibraryAlbumID != 42 {
		t.Fatalf("expected library link 42, got %+v", albums)
	}
	if albums[0].Status != domain.AlbumStatusDownloaded {
		t.Fatalf("status = %q, want downloaded", albums[0].Status)
	}
	wanted, _ := svc.ListWanted(ctx, artist.ID)
	if len(wanted) != 0 {
		t.Fatalf("wanted = %d, want 0", len(wanted))
	}
}

func TestReconcileMatchesLibraryByNameFallback(t *testing.T) {
	store := newMockStore()
	lib := newMockLibraryStore()
	lib.artistByName["Tool"] = &domain.Artist{ID: 7, Name: "Tool"}
	lib.albumsByArtist[7] = []domain.Album{
		{ID: 9, Title: "  LATERALUS! ", Year: 2001},
	}
	provider := &fakeDiscoveryProvider{
		name: "deezer",
		albums: []discovery.AlbumResult{
			{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
		},
	}
	svc := newTestService(t, store, lib, provider, nil, config.DefaultConfig())
	ctx := context.Background()

	artist, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	albums, _ := store.ListTrackedAlbums(ctx, artist.ID)
	if len(albums) != 1 || albums[0].LibraryAlbumID == nil || *albums[0].LibraryAlbumID != 9 {
		t.Fatalf("expected name-fallback link 9, got %+v", albums)
	}
	if albums[0].Status != domain.AlbumStatusDownloaded {
		t.Fatalf("status = %q, want downloaded", albums[0].Status)
	}
}

func TestReconcilePersistsArtistLibraryLink(t *testing.T) {
	store := newMockStore()
	lib := newMockLibraryStore()
	lib.artistByName["Tool"] = &domain.Artist{ID: 7, Name: "Tool"}
	lib.albumsByArtist[7] = []domain.Album{{ID: 9, Title: "Lateralus", Year: 2001}}
	provider := &fakeDiscoveryProvider{name: "deezer", albums: []discovery.AlbumResult{
		{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
	}}
	svc := newTestService(t, store, lib, provider, nil, config.DefaultConfig())
	ctx := context.Background()

	artist, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	got, err := store.GetTrackedArtist(ctx, artist.ID)
	if err != nil {
		t.Fatalf("GetTrackedArtist: %v", err)
	}
	if got.LibraryArtistID == nil || *got.LibraryArtistID != 7 {
		t.Fatalf("LibraryArtistID = %v, want 7", got.LibraryArtistID)
	}
	if store.libraryLinkCalls != 1 {
		t.Fatalf("UpdateArtistLibraryLink calls = %d, want 1", store.libraryLinkCalls)
	}

	// A second reconcile must not rewrite an unchanged link.
	if _, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{}); err != nil {
		t.Fatalf("second AddArtist: %v", err)
	}
	if store.libraryLinkCalls != 1 {
		t.Fatalf("UpdateArtistLibraryLink calls = %d after re-reconcile, want 1", store.libraryLinkCalls)
	}
}

func TestAddArtistReturnsResolvedArtist(t *testing.T) {
	store := newMockStore()
	lib := newMockLibraryStore()
	lib.artistByName["Tool"] = &domain.Artist{ID: 7, Name: "Tool"}
	lib.albumsByArtist[7] = []domain.Album{{ID: 9, Title: "Lateralus", Year: 2001}}
	provider := &fakeDiscoveryProvider{name: "deezer", albums: []discovery.AlbumResult{
		{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
	}}
	svc := newTestService(t, store, lib, provider, nil, config.DefaultConfig())

	artist, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	// The returned copy must reflect the link ReconcileAlbums just persisted,
	// not the stale pre-reconcile snapshot.
	if artist.LibraryArtistID == nil || *artist.LibraryArtistID != 7 {
		t.Fatalf("AddArtist returned LibraryArtistID = %v, want resolved 7", artist.LibraryArtistID)
	}
}

func TestReconcileResolvesLibraryArtistOnce(t *testing.T) {
	store := newMockStore()
	lib := newMockLibraryStore()
	lib.artistByName["Tool"] = &domain.Artist{ID: 7, Name: "Tool"}
	lib.albumsByArtist[7] = []domain.Album{
		{ID: 9, Title: "Lateralus", Year: 2001},
		{ID: 10, Title: "Undertow", Year: 1993},
	}
	provider := &fakeDiscoveryProvider{name: "deezer", albums: []discovery.AlbumResult{
		{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
		{ProviderID: "a2", ProviderName: "deezer", ArtistName: "Tool", Title: "Undertow", Year: 1993, Type: "album"},
		{ProviderID: "a3", ProviderName: "deezer", ArtistName: "Tool", Title: "Aenima", Year: 1996, Type: "album"},
	}}
	svc := newTestService(t, store, lib, provider, nil, config.DefaultConfig())

	artist, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	if lib.artistByNameCalls != 1 {
		t.Fatalf("GetArtistByName calls = %d, want 1 (once per reconcile)", lib.artistByNameCalls)
	}
	if lib.albumsByArtistCalls != 1 {
		t.Fatalf("GetAlbumsByArtist calls = %d, want 1 (once per reconcile)", lib.albumsByArtistCalls)
	}
	albums, _ := store.ListTrackedAlbums(context.Background(), artist.ID)
	linked := map[string]bool{}
	for _, a := range albums {
		if a.LibraryAlbumID != nil {
			linked[a.ProviderAlbumID] = true
		}
	}
	if !linked["a1"] || !linked["a2"] || linked["a3"] {
		t.Fatalf("linked = %v; want a1 and a2 matched, a3 unmatched", linked)
	}
}

func TestAddArtistDiscographyTimeout(t *testing.T) {
	orig := addArtistTimeout
	addArtistTimeout = 20 * time.Millisecond
	t.Cleanup(func() { addArtistTimeout = orig })

	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer", blockAlbums: true}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())

	_, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want wrapped context.DeadlineExceeded", err)
	}
	if len(store.artists) != 0 {
		t.Fatalf("artist rows = %d after timeout, want 0 (no orphan artist)", len(store.artists))
	}
}

func TestAddArtistErrorSentinels(t *testing.T) {
	t.Run("unregistered provider", func(t *testing.T) {
		store := newMockStore()
		svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())
		_, err := svc.AddArtist(context.Background(), "nope", "art1", "Tool", AddArtistOptions{})
		if !errors.Is(err, ErrProviderNotRegistered) {
			t.Fatalf("error = %v, want ErrProviderNotRegistered", err)
		}
	})
	t.Run("cooling provider", func(t *testing.T) {
		store := newMockStore()
		rl := newFakeRateLimiter()
		rl.cooling["deezer"] = true
		provider := &fakeDiscoveryProvider{name: "deezer"}
		svc := newTestService(t, store, newMockLibraryStore(), provider, rl, config.DefaultConfig())
		_, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
		if !errors.Is(err, ErrProviderCoolingDown) {
			t.Fatalf("error = %v, want ErrProviderCoolingDown", err)
		}
	})
}

func TestAddArtistReadBackErrorPropagates(t *testing.T) {
	store := newMockStore()
	boom := errors.New("read-back failed")
	dep := failingStore{Store: store, getArtistErr: boom}
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, dep, newMockLibraryStore(), provider, nil, config.DefaultConfig())

	_, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
	if err == nil {
		t.Fatal("expected read-back error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapped read-back error", err)
	}
}

func TestReconcileMonitorModeFutureAndNone(t *testing.T) {
	albums := []discovery.AlbumResult{
		{ProviderID: "old", ProviderName: "deezer", ArtistName: "Tool", Title: "Undertow", Year: 1993, Type: "album"},
		{ProviderID: "new", ProviderName: "deezer", ArtistName: "Tool", Title: "Future Record", Year: 2999, Type: "album"},
	}
	tests := []struct {
		name      string
		mode      domain.MonitorMode
		wantOld   bool
		wantNew   bool
		artistMon bool
	}{
		{name: "future keeps back-catalogue unmonitored", mode: domain.MonitorModeFuture, wantOld: false, wantNew: true, artistMon: true},
		{name: "none monitors nothing", mode: domain.MonitorModeNone, wantOld: false, wantNew: false, artistMon: false},
		{name: "all monitors everything", mode: domain.MonitorModeAll, wantOld: true, wantNew: true, artistMon: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			provider := &fakeDiscoveryProvider{name: "deezer", albums: albums}
			svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())
			ctx := context.Background()

			artist, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{MonitorMode: tc.mode})
			if err != nil {
				t.Fatalf("AddArtist: %v", err)
			}
			if artist.Monitored != tc.artistMon {
				t.Fatalf("artist monitored = %v, want %v", artist.Monitored, tc.artistMon)
			}
			tracked, _ := store.ListTrackedAlbums(ctx, artist.ID)
			monitored := map[string]bool{}
			for _, a := range tracked {
				monitored[a.ProviderAlbumID] = a.Monitored
			}
			if monitored["old"] != tc.wantOld {
				t.Fatalf("old monitored = %v, want %v", monitored["old"], tc.wantOld)
			}
			if monitored["new"] != tc.wantNew {
				t.Fatalf("new monitored = %v, want %v", monitored["new"], tc.wantNew)
			}
		})
	}
}

func TestReconcilePreservesExistingStatusAndMonitor(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())
	ctx := context.Background()

	artistID, _ := store.CreateTrackedArtist(ctx, &domain.TrackedArtist{
		Name: "Tool", ProviderName: "deezer", ProviderArtistID: "art1",
		Monitored: true, MonitorMode: domain.MonitorModeAll,
	})
	_, _ = store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "a1", ProviderName: "deezer",
		Title: "Lateralus", Year: 2001, Monitored: false, Status: domain.AlbumStatusDownloading,
	})

	result := discovery.AlbumResult{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001}
	if err := svc.ReconcileAlbums(ctx, artistID, []discovery.AlbumResult{result}); err != nil {
		t.Fatalf("ReconcileAlbums: %v", err)
	}
	tracked, _ := store.ListTrackedAlbums(ctx, artistID)
	if len(tracked) != 1 {
		t.Fatalf("albums = %d, want 1", len(tracked))
	}
	if tracked[0].Status != domain.AlbumStatusDownloading {
		t.Fatalf("status = %q, want preserved downloading", tracked[0].Status)
	}
	if tracked[0].Monitored {
		t.Fatal("monitored flag regressed to true; want preserved false")
	}
}

// TestReconcilePromotesWantedAndPreservesMonitor locks the reconciled contract
// the mock now mirrors from the real store: a library match promotes an
// existing wanted album to downloaded, while an explicit monitored=false
// toggle survives the reconcile.
func TestReconcilePromotesWantedAndPreservesMonitor(t *testing.T) {
	store := newMockStore()
	lib := newMockLibraryStore()
	lib.albumByExt["deezer/a1"] = &domain.Album{ID: 42, Title: "Lateralus", Year: 2001}
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, store, lib, provider, nil, config.DefaultConfig())
	ctx := context.Background()

	artistID, _ := store.CreateTrackedArtist(ctx, &domain.TrackedArtist{
		Name: "Tool", ProviderName: "deezer", ProviderArtistID: "art1",
		Monitored: true, MonitorMode: domain.MonitorModeAll,
	})
	_, _ = store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "a1", ProviderName: "deezer",
		Title: "Lateralus", Year: 2001, Monitored: false, Status: domain.AlbumStatusWanted,
	})

	result := discovery.AlbumResult{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001}
	if err := svc.ReconcileAlbums(ctx, artistID, []discovery.AlbumResult{result}); err != nil {
		t.Fatalf("ReconcileAlbums: %v", err)
	}

	tracked, _ := store.ListTrackedAlbums(ctx, artistID)
	if len(tracked) != 1 {
		t.Fatalf("albums = %d, want 1", len(tracked))
	}
	if tracked[0].Status != domain.AlbumStatusDownloaded {
		t.Fatalf("status = %q, want downloaded after library match", tracked[0].Status)
	}
	if tracked[0].Monitored {
		t.Fatal("monitored regressed to true; want preserved false")
	}
}

func TestAddArtistSkipsCoolingProvider(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer"}
	rl := newFakeRateLimiter()
	rl.cooling["deezer"] = true
	svc := newTestService(t, store, newMockLibraryStore(), provider, rl, config.DefaultConfig())

	_, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
	if err == nil {
		t.Fatal("expected error for cooling provider")
	}
	if provider.albumCalls != 0 {
		t.Fatalf("provider called while cooling: %d", provider.albumCalls)
	}
	if len(store.artists) != 0 {
		t.Fatalf("artist created for cooling provider: %d", len(store.artists))
	}
}

func TestAddArtistNilRateLimiterProceeds(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer"}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())

	if _, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{}); err != nil {
		t.Fatalf("AddArtist with nil limiter: %v", err)
	}
	if provider.albumCalls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.albumCalls)
	}
}

func TestAddArtistMarksProviderRateLimited(t *testing.T) {
	store := newMockStore()
	rl := newFakeRateLimiter()
	provider := &fakeDiscoveryProvider{
		name:      "deezer",
		albumsErr: metadata.NewRateLimitError("deezer", 90*time.Second, "429"),
	}
	svc := newTestService(t, store, newMockLibraryStore(), provider, rl, config.DefaultConfig())

	_, err := svc.AddArtist(context.Background(), "deezer", "art1", "Tool", AddArtistOptions{})
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	if rl.marked["deezer"] != 90*time.Second {
		t.Fatalf("marked retryAfter = %v, want 90s", rl.marked["deezer"])
	}
}

func TestListWantedFiltersMonitoredWanted(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{
		name: "deezer",
		albums: []discovery.AlbumResult{
			{ProviderID: "old", ProviderName: "deezer", ArtistName: "Tool", Title: "Undertow", Year: 1993, Type: "album"},
			{ProviderID: "new", ProviderName: "deezer", ArtistName: "Tool", Title: "Future Record", Year: 2999, Type: "album"},
		},
	}
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())
	ctx := context.Background()

	artist, err := svc.AddArtist(ctx, "deezer", "art1", "Tool", AddArtistOptions{MonitorMode: domain.MonitorModeFuture})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	wanted, err := svc.ListWanted(ctx, artist.ID)
	if err != nil {
		t.Fatalf("ListWanted: %v", err)
	}
	if len(wanted) != 1 || wanted[0].ProviderAlbumID != "new" {
		t.Fatalf("wanted = %+v, want only the future monitored album", wanted)
	}
}

func TestListTrackedArtists(t *testing.T) {
	store := newMockStore()
	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())
	ctx := context.Background()

	empty, err := svc.ListTrackedArtists(ctx)
	if err != nil {
		t.Fatalf("ListTrackedArtists: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("artists = %d, want 0", len(empty))
	}

	seedTrackedArtist(t, store, "Tool")
	seedTrackedArtist(t, store, "A Perfect Circle")
	got, err := svc.ListTrackedArtists(ctx)
	if err != nil {
		t.Fatalf("ListTrackedArtists: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("artists = %d, want 2", len(got))
	}
}

func TestNormalizeAlbumText(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "Lateralus", want: "lateralus"},
		{in: "  LATERALUS! ", want: "lateralus"},
		{in: "Lateralus (Remastered)", want: "lateralus remastered"},
		{in: "AC/DC", want: "ac dc"},
		{in: "The   Band", want: "the band"},
		{in: "", want: ""},
		{in: "!!!", want: ""},
	}
	for _, tc := range tests {
		if got := normalizeAlbumText(tc.in); got != tc.want {
			t.Errorf("normalizeAlbumText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAlbumMatches(t *testing.T) {
	tests := []struct {
		name                  string
		libArtist, libTitle   string
		libYear               int
		discArtist, discTitle string
		discYear              int
		want                  bool
	}{
		{name: "case and punctuation ignored", libArtist: "Tool", libTitle: "LATERALUS!", libYear: 2001, discArtist: "tool", discTitle: "Lateralus", discYear: 2001, want: true},
		{name: "different year both set", libArtist: "Tool", libTitle: "Lateralus", libYear: 2001, discArtist: "Tool", discTitle: "Lateralus", discYear: 2006, want: false},
		{name: "unknown library year still matches", libArtist: "Tool", libTitle: "Lateralus", libYear: 0, discArtist: "Tool", discTitle: "Lateralus", discYear: 2001, want: true},
		{name: "different title", libArtist: "Tool", libTitle: "Undertow", libYear: 1993, discArtist: "Tool", discTitle: "Lateralus", discYear: 2001, want: false},
		{name: "different artist", libArtist: "A Perfect Circle", libTitle: "Lateralus", libYear: 2001, discArtist: "Tool", discTitle: "Lateralus", discYear: 2001, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := albumMatches(tc.libArtist, tc.libTitle, tc.libYear, tc.discArtist, tc.discTitle, tc.discYear)
			if got != tc.want {
				t.Fatalf("albumMatches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAddArtistUnregisteredProvider(t *testing.T) {
	store := newMockStore()
	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())

	if _, err := svc.AddArtist(context.Background(), "nope", "art1", "Tool", AddArtistOptions{}); err == nil {
		t.Fatal("expected error for unregistered provider")
	}
}

func TestAddArtistRequiresProviderIdentity(t *testing.T) {
	store := newMockStore()
	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())

	if _, err := svc.AddArtist(context.Background(), "", "", "Tool", AddArtistOptions{}); err == nil {
		t.Fatal("expected error for missing provider identity")
	}
}

// TestLinkImportedAlbum proves the post-import contract: a matching tracked
// album is promoted to downloaded with the resolved library link, an unmatched
// album is untouched, and a terminal downloaded/ignored row is never rewritten.
func TestLinkImportedAlbum(t *testing.T) {
	ctx := context.Background()

	t.Run("match promotes and links", func(t *testing.T) {
		store := newMockStore()
		lib := newMockLibraryStore()
		lib.artistByName["Tool"] = &domain.Artist{ID: 7, Name: "Tool"}
		lib.albumsByArtist[7] = []domain.Album{{ID: 9, Title: "Lateralus", Year: 2001}}
		artistID := seedTrackedArtist(t, store, "Tool")
		album := seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
		svc := newTestService(t, store, lib, nil, nil, config.DefaultConfig())

		if err := svc.LinkImportedAlbum(ctx, "Tool", "Lateralus"); err != nil {
			t.Fatalf("LinkImportedAlbum: %v", err)
		}
		got, _ := store.GetTrackedAlbum(ctx, album.ID)
		if got.Status != domain.AlbumStatusDownloaded {
			t.Fatalf("status = %q, want downloaded", got.Status)
		}
		if got.LibraryAlbumID == nil || *got.LibraryAlbumID != 9 {
			t.Fatalf("LibraryAlbumID = %v, want 9", got.LibraryAlbumID)
		}
		if !got.Monitored {
			t.Fatal("monitored was cleared by the import link")
		}
	})

	t.Run("no matching album is a no-op", func(t *testing.T) {
		store := newMockStore()
		artistID := seedTrackedArtist(t, store, "Tool")
		album := seedTrackedAlbum(t, store, artistID, "a1", "Undertow", domain.AlbumStatusWanted, true)
		svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())

		if err := svc.LinkImportedAlbum(ctx, "Tool", "Lateralus"); err != nil {
			t.Fatalf("LinkImportedAlbum: %v", err)
		}
		got, _ := store.GetTrackedAlbum(ctx, album.ID)
		if got.Status != domain.AlbumStatusWanted {
			t.Fatalf("status = %q, want untouched wanted", got.Status)
		}
	})

	t.Run("unknown artist is a no-op", func(t *testing.T) {
		store := newMockStore()
		artistID := seedTrackedArtist(t, store, "Tool")
		album := seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
		svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())

		if err := svc.LinkImportedAlbum(ctx, "A Perfect Circle", "Lateralus"); err != nil {
			t.Fatalf("LinkImportedAlbum: %v", err)
		}
		got, _ := store.GetTrackedAlbum(ctx, album.ID)
		if got.Status != domain.AlbumStatusWanted {
			t.Fatalf("status = %q, want untouched wanted", got.Status)
		}
	})

	for _, terminal := range []domain.AlbumStatus{domain.AlbumStatusDownloaded, domain.AlbumStatusIgnored} {
		t.Run("terminal "+string(terminal)+" is not rewritten", func(t *testing.T) {
			store := newMockStore()
			lib := newMockLibraryStore()
			lib.artistByName["Tool"] = &domain.Artist{ID: 7, Name: "Tool"}
			lib.albumsByArtist[7] = []domain.Album{{ID: 9, Title: "Lateralus", Year: 2001}}
			artistID := seedTrackedArtist(t, store, "Tool")
			album := seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", terminal, true)
			svc := newTestService(t, store, lib, nil, nil, config.DefaultConfig())

			if err := svc.LinkImportedAlbum(ctx, "Tool", "Lateralus"); err != nil {
				t.Fatalf("LinkImportedAlbum: %v", err)
			}
			got, _ := store.GetTrackedAlbum(ctx, album.ID)
			if got.Status != terminal {
				t.Fatalf("status = %q, want preserved %q", got.Status, terminal)
			}
			if got.LibraryAlbumID != nil {
				t.Fatalf("LibraryAlbumID = %v, want untouched nil", *got.LibraryAlbumID)
			}
		})
	}
}
