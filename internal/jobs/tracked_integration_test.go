package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/tracking"
	tracksqlite "github.com/ramonskie/groovearr/internal/tracking/sqlite"
)

// ─── End-to-end job-path harness ──────────────────────────────────────
//
// These tests drive real Runners with a real tracking.Service over real SQLite;
// only the discovery provider, the album searcher, and the downloader are
// faked (no network). They live in package jobs (which may import tracking)
// rather than package tracking, because internal/jobs imports
// internal/tracking and a same-package test there would form a cycle (AGENTS §11).

// jobFakeDiscoveryProvider is a scriptable discovery.Provider for the tracked
// job tests. It serves a fixed discography/track list and counts artist-album
// calls so a skipped artist can be proven not to have touched the provider.
type jobFakeDiscoveryProvider struct {
	name       string
	albums     []discovery.AlbumResult
	tracks     []discovery.TrackInfo
	albumCalls int
}

var _ discovery.Provider = (*jobFakeDiscoveryProvider)(nil)

func (f *jobFakeDiscoveryProvider) Name() string        { return f.name }
func (f *jobFakeDiscoveryProvider) DisplayName() string { return f.name }
func (f *jobFakeDiscoveryProvider) IsConfigured() bool  { return true }
func (f *jobFakeDiscoveryProvider) Connected() bool     { return true }
func (f *jobFakeDiscoveryProvider) CapabilityStatus() map[string]string {
	return map[string]string{"discovery": "connected"}
}
func (f *jobFakeDiscoveryProvider) CheckConnection(context.Context) error { return nil }
func (f *jobFakeDiscoveryProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	return nil, nil
}
func (f *jobFakeDiscoveryProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	f.albumCalls++
	return f.albums, nil
}
func (f *jobFakeDiscoveryProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	return f.tracks, nil
}
func (f *jobFakeDiscoveryProvider) SearchAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}

// jobFakeQueuer implements tracking.DownloadQueuer and
// tracking.ActiveDownloadFinder so one value backs the delegated queue call and
// the dedup record lookup. It records the exact delegation arguments and
// returns a scripted canonical result — it does not re-implement the
// album-first policy (covered by internal/download's own tests).
type jobFakeQueuer struct {
	result   download.AlbumQueueResult
	queueErr error
	records  []download.Record
	calls    []jobQueuerCall
}

// jobQueuerCall captures one delegated QueueAlbumWithFallback invocation.
type jobQueuerCall struct {
	artist         string
	album          string
	tracks         []download.TrackQueue
	downloadClient string
	albumSources   []string
}

var (
	_ tracking.DownloadQueuer       = (*jobFakeQueuer)(nil)
	_ tracking.ActiveDownloadFinder = (*jobFakeQueuer)(nil)
)

func (f *jobFakeQueuer) QueueAlbumWithFallback(_ context.Context, artist, album string, tracks []download.TrackQueue, downloadClient string, albumSources []string) (download.AlbumQueueResult, error) {
	f.calls = append(f.calls, jobQueuerCall{artist: artist, album: album, tracks: tracks, downloadClient: downloadClient, albumSources: albumSources})
	if f.queueErr != nil {
		return f.result, f.queueErr
	}
	return f.result, nil
}

func (f *jobFakeQueuer) List(context.Context) ([]download.Record, error) {
	out := make([]download.Record, len(f.records))
	copy(out, f.records)
	return out, nil
}

func (f *jobFakeQueuer) Retry(context.Context, string) error { return nil }

// trackedJobHarness wires the real Runners dependencies: the shared library
// SQLite connection (owns the tracked_* schema), the real tracking store, the
// real tracking.Service, and the fakes above.
type trackedJobHarness struct {
	svc        *tracking.Service
	trackStore *tracksqlite.Store
	libStore   *libsqlite.Store
	provider   *jobFakeDiscoveryProvider
	q          *jobFakeQueuer
	cooling    *stubCooldown
}

func newTrackedJobHarness(t *testing.T, provider *jobFakeDiscoveryProvider, q *jobFakeQueuer, cfg config.Config) *trackedJobHarness {
	t.Helper()
	if provider == nil {
		provider = &jobFakeDiscoveryProvider{name: "deezer"}
	}
	libStore, err := libsqlite.New(filepath.Join(t.TempDir(), "test.db"), testLogger())
	if err != nil {
		t.Fatalf("open shared sqlite: %v", err)
	}
	t.Cleanup(func() { _ = libStore.Close() })

	trackStore := tracksqlite.NewSQLiteStore(libStore.DB())
	reg := discovery.NewRegistry(plugin.NewRegistry())
	if err := reg.Inner().Register(provider); err != nil {
		t.Fatalf("register discovery provider: %v", err)
	}

	var queuer tracking.DownloadQueuer
	var finder tracking.ActiveDownloadFinder
	if q != nil {
		queuer = q
		finder = q
	}
	svc := tracking.NewService(trackStore, reg, libStore, nil, queuer, finder,
		func() config.Config { return cfg }, testLogger())
	return &trackedJobHarness{svc: svc, trackStore: trackStore, libStore: libStore, provider: provider, q: q}
}

// runners builds a Runners over the harness service, wiring the fake cooldown
// bucket only when set (a nil *stubCooldown must not become a non-nil interface).
func (h *trackedJobHarness) runners() *Runners {
	deps := RunnerDeps{Log: testLogger(), Tracking: h.svc}
	if h.cooling != nil {
		deps.RateLimit = h.cooling
	}
	return NewRunners(deps)
}

func createTrackedArtistRow(t *testing.T, ctx context.Context, store *tracksqlite.Store, name, provider, providerArtistID string, auto bool) int64 {
	t.Helper()
	id, err := store.CreateTrackedArtist(ctx, &domain.TrackedArtist{
		Name: name, ProviderName: provider, ProviderArtistID: providerArtistID,
		Monitored: true, MonitorMode: domain.MonitorModeAll, AutoRefresh: auto,
	})
	if err != nil {
		t.Fatalf("CreateTrackedArtist(%s): %v", providerArtistID, err)
	}
	return id
}

func seedTrackedWantedAlbum(t *testing.T, ctx context.Context, store *tracksqlite.Store, artistID int64, providerAlbumID, title string) int64 {
	t.Helper()
	id, err := store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: providerAlbumID, ProviderName: "deezer",
		Title: title, Year: 2001, Monitored: true, Status: domain.AlbumStatusWanted,
	})
	if err != nil {
		t.Fatalf("UpsertTrackedAlbum(%s): %v", providerAlbumID, err)
	}
	return id
}

// ─── B4: only auto-refresh artists are reconciled ─────────────────────

// TestTrackedIntegrationRefreshTrackedAutoOnly drives the job end-to-end:
// only the AutoRefresh artist's discography is pulled and reconciled into
// tracked_albums; the manual artist never reaches the provider.
func TestTrackedIntegrationRefreshTrackedAutoOnly(t *testing.T) {
	ctx := context.Background()
	provider := &jobFakeDiscoveryProvider{
		name: "deezer",
		albums: []discovery.AlbumResult{
			{ProviderID: "alb-1", ProviderName: "deezer", Title: "Album One", Year: 2020, Type: "album"},
		},
	}
	h := newTrackedJobHarness(t, provider, nil, config.DefaultConfig())
	autoID := createTrackedArtistRow(t, ctx, h.trackStore, "Auto Artist", "deezer", "dz-auto", true)
	manualID := createTrackedArtistRow(t, ctx, h.trackStore, "Manual Artist", "deezer", "dz-manual", false)

	var reports []Report
	err := h.runners().RefreshTracked()(ctx, func(r Report) { reports = append(reports, r) })
	if err != nil {
		t.Fatalf("RefreshTracked: %v", err)
	}

	if provider.albumCalls != 1 {
		t.Errorf("provider album calls = %d, want 1 (only the auto-refresh artist)", provider.albumCalls)
	}
	autoAlbums, err := h.trackStore.ListTrackedAlbums(ctx, autoID)
	if err != nil || len(autoAlbums) != 1 {
		t.Fatalf("auto artist albums = %d, err=%v; want 1 reconciled", len(autoAlbums), err)
	}
	if autoAlbums[0].ProviderAlbumID != "alb-1" {
		t.Errorf("reconciled album = %q, want alb-1", autoAlbums[0].ProviderAlbumID)
	}
	manualAlbums, err := h.trackStore.ListTrackedAlbums(ctx, manualID)
	if err != nil || len(manualAlbums) != 0 {
		t.Fatalf("manual artist albums = %d, err=%v; want 0 (never refreshed)", len(manualAlbums), err)
	}
	if !reportsContain(reports, "Auto Artist") {
		t.Errorf("progress did not mention the refreshed artist; reports = %v", reports)
	}
}

// ─── B5: cooling provider is skipped with no provider call ────────────

// TestTrackedIntegrationRefreshTrackedSkipsCoolingProvider verifies the shared
// cooldown pre-filter: a cooling provider is skipped with zero provider calls
// and the run still completes successfully.
func TestTrackedIntegrationRefreshTrackedSkipsCoolingProvider(t *testing.T) {
	ctx := context.Background()
	provider := &jobFakeDiscoveryProvider{
		name:   "deezer",
		albums: []discovery.AlbumResult{{ProviderID: "alb-1", ProviderName: "deezer", Title: "Album One", Year: 2020}},
	}
	h := newTrackedJobHarness(t, provider, nil, config.DefaultConfig())
	createTrackedArtistRow(t, ctx, h.trackStore, "Cool Artist", "deezer", "dz-cool", true)
	h.cooling = &stubCooldown{cooling: map[string]bool{"deezer": true}}

	var reports []Report
	err := h.runners().RefreshTracked()(ctx, func(r Report) { reports = append(reports, r) })
	if err != nil {
		t.Fatalf("RefreshTracked: %v", err)
	}
	if provider.albumCalls != 0 {
		t.Errorf("provider album calls = %d, want 0 (cooling provider must not be called)", provider.albumCalls)
	}
	if !reportsContain(reports, "cooling down") {
		t.Errorf("no cooling-down report; reports = %v", reports)
	}
}

// ─── B6: job delegates album acquisition to the canonical policy ──────

// TestTrackedIntegrationSearchMissingArtistDelegatesToQueuer drives the job
// end-to-end and asserts tracking delegates each wanted album to the canonical
// policy with the right inputs, then marks the album downloading on a Queued
// result. The album-first-vs-per-track policy itself is covered by
// internal/download/album_queue_test.go; the fake only scripts the result.
func TestTrackedIntegrationSearchMissingArtistDelegatesToQueuer(t *testing.T) {
	tests := []struct {
		name       string
		result     download.AlbumQueueResult
		queueErr   error
		wantErr    bool
		wantStatus domain.AlbumStatus
	}{
		{
			name:       "queued result marks downloading",
			result:     download.AlbumQueueResult{Mode: download.QueueModeAlbum, Queued: 1},
			wantStatus: domain.AlbumStatusDownloading,
		},
		{
			name:       "queuer error leaves album wanted",
			queueErr:   errors.New("queue exploded"),
			wantErr:    true,
			wantStatus: domain.AlbumStatusWanted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			provider := &jobFakeDiscoveryProvider{name: "deezer", tracks: []discovery.TrackInfo{
				{ArtistName: "Tool", AlbumTitle: "Lateralus", Title: "The Grudge", TrackNumber: 1},
			}}
			q := &jobFakeQueuer{result: tt.result, queueErr: tt.queueErr}
			cfg := config.DefaultConfig()
			cfg.DownloadClient = "qbittorrent"
			h := newTrackedJobHarness(t, provider, q, cfg)
			artistID := createTrackedArtistRow(t, ctx, h.trackStore, "Tool", "deezer", "dz-tool", false)
			seedTrackedWantedAlbum(t, ctx, h.trackStore, artistID, "alb-lat", "Lateralus")
			r := NewRunners(RunnerDeps{Log: testLogger(), Tracking: h.svc})

			err := r.SearchMissingArtist(artistID)(ctx, func(Report) {})
			if tt.wantErr && err == nil {
				t.Fatal("expected SearchMissingArtist to report the queuer error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("SearchMissingArtist: %v", err)
			}

			if len(q.calls) != 1 {
				t.Fatalf("queuer calls = %d, want 1", len(q.calls))
			}
			call := q.calls[0]
			if call.artist != "Tool" || call.album != "Lateralus" {
				t.Errorf("delegated %q/%q, want Tool/Lateralus", call.artist, call.album)
			}
			if call.downloadClient != cfg.DownloadClient {
				t.Errorf("download client = %q, want %q", call.downloadClient, cfg.DownloadClient)
			}
			if !reflect.DeepEqual(call.albumSources, cfg.AlbumSources) {
				t.Errorf("album sources = %v, want %v", call.albumSources, cfg.AlbumSources)
			}
			if len(call.tracks) != 1 || call.tracks[0].Title != "The Grudge" {
				t.Errorf("tracks = %+v, want one Grudge", call.tracks)
			}

			got, err := h.trackStore.GetTrackedAlbumByProvider(ctx, artistID, "alb-lat")
			if err != nil || got == nil {
				t.Fatalf("GetTrackedAlbumByProvider = (%v, %v), want a row", got, err)
			}
			if got.Status != tt.wantStatus {
				t.Fatalf("album status = %q, want %q", got.Status, tt.wantStatus)
			}

			// Idempotent re-run: a downloading album is no longer wanted, so no
			// duplicate is delegated.
			if tt.wantStatus == domain.AlbumStatusDownloading {
				if err := r.SearchMissingArtist(artistID)(ctx, func(Report) {}); err != nil {
					t.Fatalf("SearchMissingArtist re-run: %v", err)
				}
				if len(q.calls) != 1 {
					t.Errorf("re-run queuer calls = %d, want 1 (no duplicate)", len(q.calls))
				}
			}
		})
	}
}

// ─── B7: pre-cancelled parent is a clean stop ─────────────────────────

// TestTrackedIntegrationSearchMissingArtistParentCancellation proves a
// pre-cancelled parent context short-circuits before any provider call.
func TestTrackedIntegrationSearchMissingArtistParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	provider := &jobFakeDiscoveryProvider{name: "deezer", tracks: []discovery.TrackInfo{{Title: "T", TrackNumber: 1}}}
	q := &jobFakeQueuer{}
	h := newTrackedJobHarness(t, provider, q, config.DefaultConfig())
	artistID := createTrackedArtistRow(t, context.Background(), h.trackStore, "Tool", "deezer", "dz-tool2", false)
	seedTrackedWantedAlbum(t, context.Background(), h.trackStore, artistID, "alb-lat2", "Lateralus")

	err := h.runners().SearchMissingArtist(artistID)(ctx, func(Report) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(q.calls) != 0 {
		t.Errorf("queuer calls = %d, want 0 (cancelled before any call)", len(q.calls))
	}
	if provider.albumCalls != 0 {
		t.Errorf("provider album calls = %d, want 0", provider.albumCalls)
	}
}
