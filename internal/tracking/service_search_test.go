package tracking

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
)

// configWithDownloadClient returns the default config with an album download
// client configured, so tests assert the value is forwarded to the canonical
// queuer unchanged.
func configWithDownloadClient() config.Config {
	cfg := config.DefaultConfig()
	cfg.DownloadClient = "qbittorrent"
	return cfg
}

// queuedAlbumResult is the canonical success result a delegating tracking
// service turns into a `downloading` album status.
func queuedAlbumResult() download.AlbumQueueResult {
	return download.AlbumQueueResult{Mode: download.QueueModeAlbum, Queued: 1}
}

// ─── Monitor primitives ───────────────────────────────────────────────

func TestSetArtistMonitor(t *testing.T) {
	tests := []struct {
		name      string
		mode      domain.MonitorMode
		monitored bool
		wantErr   bool
		wantMon   bool
		wantMode  domain.MonitorMode
	}{
		{name: "all", mode: domain.MonitorModeAll, monitored: true, wantMon: true, wantMode: domain.MonitorModeAll},
		{name: "future", mode: domain.MonitorModeFuture, monitored: true, wantMon: true, wantMode: domain.MonitorModeFuture},
		{name: "none forces unmonitored", mode: domain.MonitorModeNone, monitored: true, wantMon: false, wantMode: domain.MonitorModeNone},
		{name: "invalid rejected", mode: domain.MonitorMode("sometimes"), monitored: true, wantErr: true},
		{name: "empty rejected", mode: domain.MonitorMode(""), monitored: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			artistID := seedTrackedArtist(t, store, "Tool")
			svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())

			err := svc.SetArtistMonitor(context.Background(), artistID, tc.monitored, tc.mode)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if store.monitorCalls != 0 {
					t.Fatalf("store updated on invalid mode: %d", store.monitorCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetArtistMonitor: %v", err)
			}
			got, _ := store.GetTrackedArtist(context.Background(), artistID)
			if got.Monitored != tc.wantMon || got.MonitorMode != tc.wantMode {
				t.Fatalf("got %v/%q, want %v/%q", got.Monitored, got.MonitorMode, tc.wantMon, tc.wantMode)
			}
		})
	}
}

// TestSetArtistMonitorRecomputesAlbums verifies that changing the artist
// monitor mode re-derives every non-ignored album's monitored flag, while an
// ignored album (an explicit skip) is never touched.
func TestSetArtistMonitorRecomputesAlbums(t *testing.T) {
	store := newMockStore()
	ctx := context.Background()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	seedTrackedAlbum(t, store, artistID, "a2", "Undertow", domain.AlbumStatusWanted, true)
	// ignored starts monitored so any touch would be visible.
	seedTrackedAlbum(t, store, artistID, "ign", "Skipped", domain.AlbumStatusIgnored, true)

	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())

	monitored := func() map[string]bool {
		t.Helper()
		albums, _ := store.ListTrackedAlbums(ctx, artistID)
		out := make(map[string]bool, len(albums))
		for _, a := range albums {
			out[a.ProviderAlbumID] = a.Monitored
		}
		return out
	}

	// mode none unmonitors all non-ignored albums; ignored stays untouched.
	if err := svc.SetArtistMonitor(ctx, artistID, true, domain.MonitorModeNone); err != nil {
		t.Fatalf("SetArtistMonitor(none): %v", err)
	}
	got := monitored()
	if got["a1"] || got["a2"] {
		t.Fatalf("mode none left albums monitored: %v", got)
	}
	if !got["ign"] {
		t.Fatalf("ignored album was touched by reconcile: %v", got)
	}

	// mode all re-monitors non-ignored albums; ignored still untouched.
	if err := svc.SetArtistMonitor(ctx, artistID, true, domain.MonitorModeAll); err != nil {
		t.Fatalf("SetArtistMonitor(all): %v", err)
	}
	got = monitored()
	if !got["a1"] || !got["a2"] {
		t.Fatalf("mode all did not re-monitor albums: %v", got)
	}
	if !got["ign"] {
		t.Fatalf("ignored album was touched by reconcile: %v", got)
	}
}

func TestSetArtistMonitorUnknownArtist(t *testing.T) {
	store := newMockStore()
	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())
	if err := svc.SetArtistMonitor(context.Background(), 99, true, domain.MonitorModeAll); err == nil {
		t.Fatal("expected error for unknown artist")
	}
}

func TestSetAlbumMonitoredAndDeleteArtist(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	album := seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())
	ctx := context.Background()

	if err := svc.SetAlbumMonitored(ctx, album.ID, false); err != nil {
		t.Fatalf("SetAlbumMonitored: %v", err)
	}
	albums, _ := store.ListTrackedAlbums(ctx, artistID)
	if albums[0].Monitored {
		t.Fatal("album still monitored after toggle off")
	}

	if err := svc.DeleteTrackedArtist(ctx, artistID); err != nil {
		t.Fatalf("DeleteTrackedArtist: %v", err)
	}
	if got, _ := store.GetTrackedArtist(ctx, artistID); got != nil {
		t.Fatal("artist not deleted")
	}
	if len(store.albums[artistID]) != 0 {
		t.Fatalf("albums not cascaded: %d", len(store.albums[artistID]))
	}
}

// ─── RefreshArtist ────────────────────────────────────────────────────

func TestRefreshArtistReconcilesAndMarksRefreshed(t *testing.T) {
	store := newMockStore()
	provider := &fakeDiscoveryProvider{name: "deezer", albums: []discovery.AlbumResult{
		{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
	}}
	artistID := seedTrackedArtist(t, store, "Tool")
	svc := newTestService(t, store, newMockLibraryStore(), provider, nil, config.DefaultConfig())

	res, err := svc.RefreshArtist(context.Background(), artistID)
	if err != nil {
		t.Fatalf("RefreshArtist: %v", err)
	}
	if res.AlbumsSeen != 1 || res.NewlyWanted != 1 || res.Missing != 1 {
		t.Fatalf("result = %+v, want seen=1 wanted=1 missing=1", res)
	}
	if store.touchCalls != 1 {
		t.Fatalf("touch calls = %d, want 1", store.touchCalls)
	}
	if provider.albumCalls != 1 {
		t.Fatalf("provider album calls = %d, want 1", provider.albumCalls)
	}
}

func TestRefreshArtistErrors(t *testing.T) {
	store := newMockStore()
	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())
	ctx := context.Background()

	if _, err := svc.RefreshArtist(ctx, 1); err == nil {
		t.Fatal("expected error for unknown artist")
	}
	artistID := seedTrackedArtist(t, store, "Tool")
	if _, err := svc.RefreshArtist(ctx, artistID); err == nil {
		t.Fatal("expected error for unregistered discovery provider")
	}
}

func TestRefreshArtistAutoSearchGate(t *testing.T) {
	tests := []struct {
		name       string
		autoSearch bool
		wantQueued int
	}{
		{name: "gate off does not queue", autoSearch: false, wantQueued: 0},
		{name: "gate on queues missing", autoSearch: true, wantQueued: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			artistID := seedTrackedArtist(t, store, "Tool")
			seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
			provider := &fakeDiscoveryProvider{name: "deezer", albums: []discovery.AlbumResult{
				{ProviderID: "a1", ProviderName: "deezer", ArtistName: "Tool", Title: "Lateralus", Year: 2001, Type: "album"},
			}}
			q := &fakeQueuer{result: queuedAlbumResult()}
			cfg := configWithDownloadClient()
			cfg.Tracking.AutoSearchMissing = boolPtr(tc.autoSearch)
			svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, cfg)

			if _, err := svc.RefreshArtist(context.Background(), artistID); err != nil {
				t.Fatalf("RefreshArtist: %v", err)
			}
			if len(q.calls) != tc.wantQueued {
				t.Fatalf("queuer calls = %d, want %d", len(q.calls), tc.wantQueued)
			}
		})
	}
}

// ─── SearchMissing ────────────────────────────────────────────────────

// TestSearchMissingDelegatesToQueuer proves tracking forwards every resolved
// input to the canonical policy and turns a non-zero Queued result into the
// downloading status. It asserts delegation, not the album-first policy itself
// (that lives in internal/download).
func TestSearchMissingDelegatesToQueuer(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	provider := &fakeDiscoveryProvider{name: "deezer", albumTracks: []discovery.TrackInfo{
		{ArtistName: "Tool", AlbumTitle: "Lateralus", Title: "The Grudge", TrackNumber: 1, DiscNumber: 1, ISRC: "US1"},
		{ArtistName: "Tool", AlbumTitle: "Lateralus", Title: "Mantra", TrackNumber: 2, DiscNumber: 1},
	}}
	q := &fakeQueuer{result: queuedAlbumResult()}
	cfg := configWithDownloadClient()
	svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, cfg)

	res, err := svc.SearchMissing(context.Background(), artistID)
	if err != nil {
		t.Fatalf("SearchMissing: %v", err)
	}
	if res.Queued != 1 || res.Skipped != 0 || res.Errors != 0 {
		t.Fatalf("result = %+v, want queued=1", res)
	}
	if len(q.calls) != 1 {
		t.Fatalf("queuer calls = %d, want 1", len(q.calls))
	}
	call := q.calls[0]
	if call.artist != "Tool" || call.album != "Lateralus" {
		t.Fatalf("delegated %q/%q, want Tool/Lateralus", call.artist, call.album)
	}
	if call.downloadClient != cfg.DownloadClient {
		t.Fatalf("download client = %q, want %q", call.downloadClient, cfg.DownloadClient)
	}
	if !reflect.DeepEqual(call.albumSources, cfg.AlbumSources) {
		t.Fatalf("album sources = %v, want %v", call.albumSources, cfg.AlbumSources)
	}
	if len(call.tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(call.tracks))
	}
	want := download.TrackQueue{Artist: "Tool", Album: "Lateralus", Title: "The Grudge", TrackNumber: 1, DiscNumber: 1, ISRC: "US1"}
	if call.tracks[0] != want {
		t.Fatalf("track[0] = %+v, want %+v", call.tracks[0], want)
	}
	albums, _ := store.ListTrackedAlbums(context.Background(), artistID)
	if albums[0].Status != domain.AlbumStatusDownloading {
		t.Fatalf("status = %q, want downloading", albums[0].Status)
	}
}

// TestSearchMissingTrackResolutionErrorDoesNotBlock proves a failed track
// lookup is best-effort: the album-first attempt still reaches the canonical
// queuer with an empty track slice.
func TestSearchMissingTrackResolutionErrorDoesNotBlock(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	provider := &fakeDiscoveryProvider{name: "deezer", tracksErr: errors.New("tracks exploded")}
	q := &fakeQueuer{result: queuedAlbumResult()}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, configWithDownloadClient())

	res, err := svc.SearchMissing(context.Background(), artistID)
	if err != nil {
		t.Fatalf("SearchMissing: %v", err)
	}
	if res.Queued != 1 || res.Errors != 0 {
		t.Fatalf("result = %+v, want queued=1 errors=0", res)
	}
	if len(q.calls) != 1 {
		t.Fatalf("queuer calls = %d, want 1 despite track error", len(q.calls))
	}
	if len(q.calls[0].tracks) != 0 {
		t.Fatalf("tracks = %d, want 0 after resolution error", len(q.calls[0].tracks))
	}
}

func TestSearchMissingSkipsExisting(t *testing.T) {
	tests := []struct {
		name string
		rec  download.Record
	}{
		{name: "already queued", rec: download.Record{ID: "q1", Artist: "Tool", Title: "Lateralus", State: download.StateQueued}},
		{name: "already downloading", rec: download.Record{ID: "d1", Artist: "Tool", Title: "Lateralus", State: download.StateDownloading}},
		{name: "already downloaded", rec: download.Record{ID: "i1", Artist: "Tool", Title: "Lateralus", State: download.StateImported}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			artistID := seedTrackedArtist(t, store, "Tool")
			seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
			provider := &fakeDiscoveryProvider{name: "deezer"}
			q := &fakeQueuer{records: []download.Record{tc.rec}}
			svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, configWithDownloadClient())

			res, err := svc.SearchMissing(context.Background(), artistID)
			if err != nil {
				t.Fatalf("SearchMissing: %v", err)
			}
			if res.Skipped != 1 || res.Queued != 0 {
				t.Fatalf("result = %+v, want skipped=1", res)
			}
			if len(q.calls) != 0 {
				t.Fatalf("queuer calls = %d, want 0", len(q.calls))
			}
		})
	}
}

func TestSearchMissingIdempotentAcrossCalls(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	provider := &fakeDiscoveryProvider{name: "deezer"}
	q := &fakeQueuer{result: queuedAlbumResult()}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, configWithDownloadClient())
	ctx := context.Background()

	first, err := svc.SearchMissing(ctx, artistID)
	if err != nil {
		t.Fatalf("first SearchMissing: %v", err)
	}
	if first.Queued != 1 {
		t.Fatalf("first queued = %d, want 1", first.Queued)
	}
	// The first call marked the album downloading, so the second must queue nothing.
	second, err := svc.SearchMissing(ctx, artistID)
	if err != nil {
		t.Fatalf("second SearchMissing: %v", err)
	}
	if second.Queued != 0 {
		t.Fatalf("second queued = %d, want 0", second.Queued)
	}
	if len(q.calls) != 1 {
		t.Fatalf("queuer calls = %d, want 1 (no duplicates)", len(q.calls))
	}
}

func TestSearchMissingRearmsExhaustedAfterCooldown(t *testing.T) {
	tests := []struct {
		name        string
		updated     time.Time
		retries     int
		state       download.State
		wantRetry   bool
		wantSkipped int
	}{
		{name: "exhausted past cooldown re-armed", updated: time.Now().UTC().Add(-25 * time.Hour), retries: download.MaxRetries, state: download.StateFailed, wantRetry: true},
		{name: "failedPending exhausted past cooldown re-armed", updated: time.Now().UTC().Add(-25 * time.Hour), retries: download.MaxRetries, state: download.StateFailedPending, wantRetry: true},
		{name: "exhausted within cooldown blocked", updated: time.Now().UTC().Add(-1 * time.Hour), retries: download.MaxRetries, state: download.StateFailed, wantSkipped: 1},
		{name: "retry budget remains blocked", updated: time.Now().UTC().Add(-25 * time.Hour), retries: 1, state: download.StateFailed, wantSkipped: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMockStore()
			artistID := seedTrackedArtist(t, store, "Tool")
			seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
			provider := &fakeDiscoveryProvider{name: "deezer"}
			q := &fakeQueuer{records: []download.Record{{
				ID: "f1", Artist: "Tool", Title: "Lateralus",
				State: tc.state, RetryCount: tc.retries, UpdatedAt: tc.updated,
			}}}
			svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, configWithDownloadClient())

			res, err := svc.SearchMissing(context.Background(), artistID)
			if err != nil {
				t.Fatalf("SearchMissing: %v", err)
			}
			if tc.wantRetry {
				if len(q.retried) != 1 || q.retried[0] != "f1" {
					t.Fatalf("retried = %v, want [f1]", q.retried)
				}
				if res.Queued != 1 {
					t.Fatalf("queued = %d, want 1", res.Queued)
				}
				if len(q.calls) != 0 {
					t.Fatalf("queuer called, want 0 for re-arm")
				}
				return
			}
			if len(q.retried) != 0 {
				t.Fatalf("unexpected retry: %v", q.retried)
			}
			if res.Skipped != tc.wantSkipped {
				t.Fatalf("skipped = %d, want %d", res.Skipped, tc.wantSkipped)
			}
		})
	}
}

func TestSearchMissingRearmFailureCountsError(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	provider := &fakeDiscoveryProvider{name: "deezer"}
	q := &fakeQueuer{
		retryErr: errors.New("boom"),
		records: []download.Record{{
			ID: "f1", Artist: "Tool", Title: "Lateralus",
			State: download.StateFailed, RetryCount: download.MaxRetries, UpdatedAt: time.Now().UTC().Add(-25 * time.Hour),
		}},
	}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, config.DefaultConfig())

	res, err := svc.SearchMissing(context.Background(), artistID)
	if err != nil {
		t.Fatalf("SearchMissing: %v", err)
	}
	if res.Errors != 1 || res.Queued != 0 {
		t.Fatalf("result = %+v, want errors=1", res)
	}
}

func TestSearchMissingNilRateLimiter(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	provider := &fakeDiscoveryProvider{name: "deezer"}
	q := &fakeQueuer{result: queuedAlbumResult()}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), provider, nil, q, config.DefaultConfig())

	if _, err := svc.SearchMissing(context.Background(), artistID); err != nil {
		t.Fatalf("SearchMissing with nil rate limiter: %v", err)
	}
}

func TestSearchMissingNoQueuer(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	svc := newTestService(t, store, newMockLibraryStore(), &fakeDiscoveryProvider{name: "deezer"}, nil, config.DefaultConfig())
	if _, err := svc.SearchMissing(context.Background(), artistID); err == nil {
		t.Fatal("expected error without a queuer")
	}
}

func TestDownloadKeyNormalizes(t *testing.T) {
	if got, want := downloadKey("  Tool ", " LATERALUS"), "tool|lateralus"; got != want {
		t.Fatalf("downloadKey = %q, want %q", got, want)
	}
}

// TestSearchMissingBatchCap proves a single run processes at most
// searchBatchSize albums in deterministic oldest-first order and reports the
// remainder, and that a second run resumes with the next batch instead of
// starving the later albums.
func TestSearchMissingBatchCap(t *testing.T) {
	orig := searchBatchSize
	searchBatchSize = 2
	t.Cleanup(func() { searchBatchSize = orig })

	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	// Seeded out of order: the deterministic sort must pick oldest first.
	seedTrackedAlbumYear(t, store, artistID, "newest", "Newest", 2010, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "oldest", "Oldest", 1990, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "middle", "Middle", 2000, domain.AlbumStatusWanted, true)
	q := &fakeQueuer{result: queuedAlbumResult()}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), &fakeDiscoveryProvider{name: "deezer"}, nil, q, configWithDownloadClient())
	ctx := context.Background()

	first, err := svc.SearchMissing(ctx, artistID)
	if err != nil {
		t.Fatalf("first SearchMissing: %v", err)
	}
	if len(q.calls) != 2 {
		t.Fatalf("first run queuer calls = %d, want 2 (batch cap)", len(q.calls))
	}
	if first.Queued != 2 || first.Remaining != 1 {
		t.Fatalf("first result = %+v, want queued=2 remaining=1", first)
	}
	if q.calls[0].album != "Oldest" || q.calls[1].album != "Middle" {
		t.Fatalf("first batch order = [%q,%q], want oldest-first", q.calls[0].album, q.calls[1].album)
	}

	second, err := svc.SearchMissing(ctx, artistID)
	if err != nil {
		t.Fatalf("second SearchMissing: %v", err)
	}
	if len(q.calls) != 3 {
		t.Fatalf("total queuer calls = %d, want 3", len(q.calls))
	}
	if q.calls[2].album != "Newest" {
		t.Fatalf("second batch album = %q, want the remaining Newest", q.calls[2].album)
	}
	if second.Queued != 1 || second.Remaining != 0 {
		t.Fatalf("second result = %+v, want queued=1 remaining=0", second)
	}
}

// TestSearchMissingRotationNoOpDoesNotBlock proves a canonical-policy no-op
// (Queued:0, Errors:0) counts as Skipped, is stamped as searched, and therefore
// stops occupying the oldest-first batch: later albums rotate in across runs
// and Remaining decreases to zero.
func TestSearchMissingRotationNoOpDoesNotBlock(t *testing.T) {
	orig := searchBatchSize
	searchBatchSize = 2
	t.Cleanup(func() { searchBatchSize = orig })

	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbumYear(t, store, artistID, "a", "NoOp", 1990, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "b", "Second", 1991, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "c", "Third", 1992, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "d", "Fourth", 1993, domain.AlbumStatusWanted, true)
	q := &fakeQueuer{
		result:        queuedAlbumResult(),
		resultByAlbum: map[string]download.AlbumQueueResult{"NoOp": {}},
	}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), &fakeDiscoveryProvider{name: "deezer"}, nil, q, configWithDownloadClient())
	ctx := context.Background()

	first, err := svc.SearchMissing(ctx, artistID)
	if err != nil {
		t.Fatalf("first SearchMissing: %v", err)
	}
	if first.Queued != 1 || first.Skipped != 1 || first.Remaining != 2 {
		t.Fatalf("first result = %+v, want queued=1 skipped=1 remaining=2", first)
	}
	if q.calls[0].album != "NoOp" || q.calls[1].album != "Second" {
		t.Fatalf("first batch = [%q,%q], want [NoOp,Second]", q.calls[0].album, q.calls[1].album)
	}

	// NoOp is now stamped searched, so the never-searched Third/Fourth rotate
	// into the batch instead of NoOp blocking them forever.
	second, err := svc.SearchMissing(ctx, artistID)
	if err != nil {
		t.Fatalf("second SearchMissing: %v", err)
	}
	if second.Queued != 2 || second.Remaining != 1 {
		t.Fatalf("second result = %+v, want queued=2 remaining=1", second)
	}
	if q.calls[2].album != "Third" || q.calls[3].album != "Fourth" {
		t.Fatalf("second batch = [%q,%q], want [Third,Fourth]", q.calls[2].album, q.calls[3].album)
	}

	third, err := svc.SearchMissing(ctx, artistID)
	if err != nil {
		t.Fatalf("third SearchMissing: %v", err)
	}
	if third.Skipped != 1 || third.Remaining != 0 {
		t.Fatalf("third result = %+v, want skipped=1 remaining=0", third)
	}
}

// TestSearchMissingMarksEveryProcessedAlbumSearched proves MarkAlbumSearched is
// called once for every processed album across all outcomes: queued, active
// skip, canonical no-op, and re-arm.
func TestSearchMissingMarksEveryProcessedAlbumSearched(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbumYear(t, store, artistID, "q", "Queued", 1990, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "act", "Active", 1991, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "no", "NoOp", 1992, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "re", "Rearm", 1993, domain.AlbumStatusWanted, true)
	q := &fakeQueuer{
		result:        queuedAlbumResult(),
		resultByAlbum: map[string]download.AlbumQueueResult{"NoOp": {}},
		records: []download.Record{
			{ID: "act1", Artist: "Tool", Title: "Active", State: download.StateQueued},
			{ID: "re1", Artist: "Tool", Title: "Rearm", State: download.StateFailed, RetryCount: download.MaxRetries, UpdatedAt: time.Now().UTC().Add(-25 * time.Hour)},
		},
	}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), &fakeDiscoveryProvider{name: "deezer"}, nil, q, configWithDownloadClient())

	res, err := svc.SearchMissing(context.Background(), artistID)
	if err != nil {
		t.Fatalf("SearchMissing: %v", err)
	}
	if len(store.searchedIDs) != 4 {
		t.Fatalf("MarkAlbumSearched calls = %d, want 4 (every processed album)", len(store.searchedIDs))
	}
	if res.Queued != 2 || res.Skipped != 2 || res.Errors != 0 {
		t.Fatalf("result = %+v, want queued=2 skipped=2 errors=0", res)
	}
}

// TestSearchMissingRequeuesStalledDownloading proves a failed download stuck at
// status downloading is reset to wanted so the re-arm branch can retry the
// existing record (R2): downloading → wanted is the only allowed regression.
func TestSearchMissingRequeuesStalledDownloading(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	album := seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusDownloading, true)
	q := &fakeQueuer{records: []download.Record{{
		ID: "f1", Artist: "Tool", Title: "Lateralus",
		State: download.StateFailed, RetryCount: download.MaxRetries, UpdatedAt: time.Now().UTC().Add(-25 * time.Hour),
	}}}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), &fakeDiscoveryProvider{name: "deezer"}, nil, q, configWithDownloadClient())

	res, err := svc.SearchMissing(context.Background(), artistID)
	if err != nil {
		t.Fatalf("SearchMissing: %v", err)
	}
	if len(q.retried) != 1 || q.retried[0] != "f1" {
		t.Fatalf("retried = %v, want [f1]", q.retried)
	}
	if res.Queued != 1 {
		t.Fatalf("queued = %d, want 1", res.Queued)
	}
	got, _ := store.GetTrackedAlbum(context.Background(), album.ID)
	if got.Status != domain.AlbumStatusWanted {
		t.Fatalf("status after requeue = %q, want wanted", got.Status)
	}
}

// TestSearchMissingLeavesActiveDownloadingStalled proves the requeue is
// failure-driven only: a downloading album whose record is still active is not
// reset.
func TestSearchMissingLeavesActiveDownloadingStalled(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	album := seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusDownloading, true)
	q := &fakeQueuer{records: []download.Record{{
		ID: "d1", Artist: "Tool", Title: "Lateralus", State: download.StateDownloading,
	}}}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), &fakeDiscoveryProvider{name: "deezer"}, nil, q, configWithDownloadClient())

	if _, err := svc.SearchMissing(context.Background(), artistID); err != nil {
		t.Fatalf("SearchMissing: %v", err)
	}
	got, _ := store.GetTrackedAlbum(context.Background(), album.ID)
	if got.Status != domain.AlbumStatusDownloading {
		t.Fatalf("status = %q, want downloading left untouched", got.Status)
	}
}

// TestSetAlbumStatus locks the wanted|ignored-only validation, the unknown
// album contract, and that a valid status is persisted.
func TestSetAlbumStatus(t *testing.T) {
	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	album := seedTrackedAlbum(t, store, artistID, "a1", "Lateralus", domain.AlbumStatusWanted, true)
	svc := newTestService(t, store, newMockLibraryStore(), nil, nil, config.DefaultConfig())
	ctx := context.Background()

	if err := svc.SetAlbumStatus(ctx, album.ID, domain.AlbumStatusIgnored); err != nil {
		t.Fatalf("SetAlbumStatus(ignored): %v", err)
	}
	if got, _ := store.GetTrackedAlbum(ctx, album.ID); got.Status != domain.AlbumStatusIgnored {
		t.Fatalf("status = %q, want ignored", got.Status)
	}
	if err := svc.SetAlbumStatus(ctx, album.ID, domain.AlbumStatusWanted); err != nil {
		t.Fatalf("SetAlbumStatus(wanted): %v", err)
	}
	if got, _ := store.GetTrackedAlbum(ctx, album.ID); got.Status != domain.AlbumStatusWanted {
		t.Fatalf("status = %q, want wanted", got.Status)
	}

	for _, invalid := range []domain.AlbumStatus{domain.AlbumStatusDownloading, domain.AlbumStatusDownloaded, ""} {
		err := svc.SetAlbumStatus(ctx, album.ID, invalid)
		if !errors.Is(err, ErrAlbumStatusInvalid) {
			t.Fatalf("SetAlbumStatus(%q) error = %v, want ErrAlbumStatusInvalid", invalid, err)
		}
	}
	if got, _ := store.GetTrackedAlbum(ctx, album.ID); got.Status != domain.AlbumStatusWanted {
		t.Fatalf("invalid status changed the row: %q", got.Status)
	}

	if err := svc.SetAlbumStatus(ctx, 99999, domain.AlbumStatusIgnored); !errors.Is(err, ErrAlbumNotFound) {
		t.Fatalf("unknown album error = %v, want ErrAlbumNotFound", err)
	}
}

// TestSearchMissingUnlimitedBatch locks the <=0 sentinel: a non-positive batch
// size processes every wanted album in one run.
func TestSearchMissingUnlimitedBatch(t *testing.T) {
	orig := searchBatchSize
	searchBatchSize = 0
	t.Cleanup(func() { searchBatchSize = orig })

	store := newMockStore()
	artistID := seedTrackedArtist(t, store, "Tool")
	seedTrackedAlbumYear(t, store, artistID, "a1", "One", 1990, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "a2", "Two", 2000, domain.AlbumStatusWanted, true)
	seedTrackedAlbumYear(t, store, artistID, "a3", "Three", 2010, domain.AlbumStatusWanted, true)
	q := &fakeQueuer{result: queuedAlbumResult()}
	svc := newTestServiceWith(t, store, newMockLibraryStore(), &fakeDiscoveryProvider{name: "deezer"}, nil, q, configWithDownloadClient())

	res, err := svc.SearchMissing(context.Background(), artistID)
	if err != nil {
		t.Fatalf("SearchMissing: %v", err)
	}
	if len(q.calls) != 3 || res.Queued != 3 || res.Remaining != 0 {
		t.Fatalf("calls=%d result=%+v, want 3 processed with no remainder", len(q.calls), res)
	}
}
