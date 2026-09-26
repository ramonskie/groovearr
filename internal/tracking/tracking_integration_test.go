package tracking_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/tracking"
	tracksqlite "github.com/ramonskie/groovearr/internal/tracking/sqlite"
)

// ─── End-to-end harness (synchronous path) ────────────────────────────
//
// These tests exercise the SYNCHRONOUS tracking path against the real SQLite
// store and the real tracking.Service, with only the discovery provider faked
// (no network).
//
// They use the external tracking_test package: a same-package test cannot
// import tracking/sqlite (the store wraps the tracking package), which Go
// rejects as an import cycle. The internal-package sibling file
// tracking_integration_internal_test.go keeps the tests that need unexported
// seams (the addArtistTimeout var).

func extLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// extFakeDiscoveryProvider is a scriptable discovery.Provider: it serves a
// fixed discography and counts GetArtistAlbums calls. It is deliberately
// defined in the external test package because the internal fakes are
// unexported.
type extFakeDiscoveryProvider struct {
	name       string
	albums     []discovery.AlbumResult
	albumCalls int
}

var _ discovery.Provider = (*extFakeDiscoveryProvider)(nil)

func (f *extFakeDiscoveryProvider) Name() string        { return f.name }
func (f *extFakeDiscoveryProvider) DisplayName() string { return f.name }
func (f *extFakeDiscoveryProvider) IsConfigured() bool  { return true }
func (f *extFakeDiscoveryProvider) Connected() bool     { return true }
func (f *extFakeDiscoveryProvider) CapabilityStatus() map[string]string {
	return map[string]string{"discovery": "connected"}
}
func (f *extFakeDiscoveryProvider) CheckConnection(context.Context) error { return nil }
func (f *extFakeDiscoveryProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	return nil, nil
}
func (f *extFakeDiscoveryProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	f.albumCalls++
	return f.albums, nil
}
func (f *extFakeDiscoveryProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	return nil, nil
}
func (f *extFakeDiscoveryProvider) SearchAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}

// extHarness wires the shared library SQLite connection (which owns the
// tracked_* schema), the real tracking store, and the real tracking.Service —
// the same construction app.go performs, minus the network.
type extHarness struct {
	svc      *tracking.Service
	store    *tracksqlite.Store
	lib      *libsqlite.Store
	provider *extFakeDiscoveryProvider
}

func newExtHarness(t *testing.T, provider *extFakeDiscoveryProvider) *extHarness {
	t.Helper()
	libStore, err := libsqlite.New(filepath.Join(t.TempDir(), "test.db"), extLogger())
	if err != nil {
		t.Fatalf("open shared sqlite: %v", err)
	}
	t.Cleanup(func() { _ = libStore.Close() })

	store := tracksqlite.NewSQLiteStore(libStore.DB())
	reg := discovery.NewRegistry(plugin.NewRegistry())
	if err := reg.Inner().Register(provider); err != nil {
		t.Fatalf("register discovery provider: %v", err)
	}
	svc := tracking.NewService(store, reg, libStore, nil, nil, nil,
		func() config.Config { return config.DefaultConfig() }, extLogger())
	return &extHarness{svc: svc, store: store, lib: libStore, provider: provider}
}

func (h *extHarness) albumByProvider(t *testing.T, artistID int64, providerAlbumID string) domain.TrackedAlbum {
	t.Helper()
	got, err := h.store.GetTrackedAlbumByProvider(context.Background(), artistID, providerAlbumID)
	if err != nil || got == nil {
		t.Fatalf("GetTrackedAlbumByProvider(%s) = (%v, %v), want a row", providerAlbumID, got, err)
	}
	return *got
}

func extAlbumByProvider(t *testing.T, albums []domain.TrackedAlbum, providerAlbumID string) domain.TrackedAlbum {
	t.Helper()
	for _, a := range albums {
		if a.ProviderAlbumID == providerAlbumID {
			return a
		}
	}
	t.Fatalf("tracked album %q not found in %+v", providerAlbumID, albums)
	return domain.TrackedAlbum{}
}

// ─── A1: synchronous add reconciles library + new albums ──────────────

// TestTrackingIntegrationAddArtistReconcileLibraryAndNewAlbums covers AddArtist
// end-to-end against real SQLite: a discography containing one album already in
// the library and one new release must persist the library links and per-album
// status, and the returned artist must reflect the persisted linkage (N2).
func TestTrackingIntegrationAddArtistReconcileLibraryAndNewAlbums(t *testing.T) {
	ctx := context.Background()
	h := newExtHarness(t, &extFakeDiscoveryProvider{
		name: "deezer",
		albums: []discovery.AlbumResult{
			{ProviderID: "alb-lib", ProviderName: "deezer", Title: "In Library", Year: 2000, Type: "album"},
			{ProviderID: "alb-new", ProviderName: "deezer", Title: "Brand New", Year: 2024, Type: "album"},
		},
	})

	// Arrange: seed the local library with the artist and one of the releases.
	// The external ID is what the service matches first.
	libArtistID, err := h.lib.UpsertArtist(ctx, &domain.Artist{Name: "Aphex Twin"})
	if err != nil {
		t.Fatalf("seed library artist: %v", err)
	}
	libAlbumID, err := h.lib.UpsertAlbum(ctx, &domain.Album{
		ArtistID: libArtistID, Title: "In Library", Year: 2000,
		ExternalIDs: map[string]string{"deezer": "alb-lib"},
	})
	if err != nil {
		t.Fatalf("seed library album: %v", err)
	}

	// Act
	artist, err := h.svc.AddArtist(ctx, "deezer", "dz-artist-1", "Aphex Twin", tracking.AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}

	// Assert: the returned artist reflects the persisted library link (N2).
	if artist == nil || artist.ID == 0 {
		t.Fatalf("AddArtist returned %+v, want a persisted artist", artist)
	}
	if artist.LibraryArtistID == nil || *artist.LibraryArtistID != libArtistID {
		t.Fatalf("returned artist LibraryArtistID = %v, want %d", artist.LibraryArtistID, libArtistID)
	}
	if artist.ProviderName != "deezer" || artist.ProviderArtistID != "dz-artist-1" {
		t.Errorf("artist provider pair = %s/%s, want deezer/dz-artist-1", artist.ProviderName, artist.ProviderArtistID)
	}

	// The persisted artist row carries the link too.
	storedArtist, err := h.store.GetTrackedArtistByProvider(ctx, "deezer", "dz-artist-1")
	if err != nil || storedArtist == nil {
		t.Fatalf("GetTrackedArtistByProvider = (%v, %v), want a row", storedArtist, err)
	}
	if storedArtist.LibraryArtistID == nil || *storedArtist.LibraryArtistID != libArtistID {
		t.Errorf("persisted LibraryArtistID = %v, want %d", storedArtist.LibraryArtistID, libArtistID)
	}

	albums, err := h.store.ListTrackedAlbums(ctx, artist.ID)
	if err != nil || len(albums) != 2 {
		t.Fatalf("ListTrackedAlbums = %d rows, err=%v; want 2", len(albums), err)
	}

	lib := extAlbumByProvider(t, albums, "alb-lib")
	if lib.Status != domain.AlbumStatusDownloaded {
		t.Errorf("library album status = %q, want downloaded", lib.Status)
	}
	if lib.LibraryAlbumID == nil || *lib.LibraryAlbumID != libAlbumID {
		t.Errorf("library album LibraryAlbumID = %v, want %d", lib.LibraryAlbumID, libAlbumID)
	}
	if !lib.Monitored {
		t.Errorf("library album Monitored = false, want true")
	}

	fresh := extAlbumByProvider(t, albums, "alb-new")
	if fresh.Status != domain.AlbumStatusWanted {
		t.Errorf("new album status = %q, want wanted", fresh.Status)
	}
	if fresh.LibraryAlbumID != nil {
		t.Errorf("new album LibraryAlbumID = %v, want nil", fresh.LibraryAlbumID)
	}
	if !fresh.Monitored {
		t.Errorf("new album Monitored = false, want true")
	}
}

// ─── A2: re-refresh keeps Option-B state, promotes a new match ─────────

// TestTrackingIntegrationRefreshArtistPreservesState covers the Option-B
// reconcile semantics against real SQLite: a re-refresh must not regress a
// terminal status or flip a per-album monitored=false toggle, while a wanted
// album that newly gains a library match is promoted to downloaded.
func TestTrackingIntegrationRefreshArtistPreservesState(t *testing.T) {
	ctx := context.Background()
	h := newExtHarness(t, &extFakeDiscoveryProvider{
		name: "deezer",
		albums: []discovery.AlbumResult{
			{ProviderID: "alb-lib", ProviderName: "deezer", Title: "In Library", Year: 2000, Type: "album"},
			{ProviderID: "alb-later", ProviderName: "deezer", Title: "Later Match", Year: 2001, Type: "album"},
			{ProviderID: "alb-skip", ProviderName: "deezer", Title: "Skip Me", Year: 2002, Type: "album"},
		},
	})

	libArtistID, err := h.lib.UpsertArtist(ctx, &domain.Artist{Name: "Boards of Canada"})
	if err != nil {
		t.Fatalf("seed library artist: %v", err)
	}
	if _, err := h.lib.UpsertAlbum(ctx, &domain.Album{
		ArtistID: libArtistID, Title: "In Library", Year: 2000,
		ExternalIDs: map[string]string{"deezer": "alb-lib"},
	}); err != nil {
		t.Fatalf("seed library album: %v", err)
	}

	artist, err := h.svc.AddArtist(ctx, "deezer", "dz-artist-2", "Boards of Canada", tracking.AddArtistOptions{})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}

	// User intent recorded before the refresh: turn the per-album monitor off
	// for "Later Match", and mark "Skip Me" ignored (a terminal state).
	later := h.albumByProvider(t, artist.ID, "alb-later")
	if err := h.store.UpdateAlbumMonitor(ctx, later.ID, false); err != nil {
		t.Fatalf("UpdateAlbumMonitor(false): %v", err)
	}
	skip := h.albumByProvider(t, artist.ID, "alb-skip")
	if err := h.store.MarkAlbumStatus(ctx, skip.ID, domain.AlbumStatusIgnored); err != nil {
		t.Fatalf("MarkAlbumStatus(ignored): %v", err)
	}

	// A library release appears for the previously-wanted "Later Match".
	if _, err := h.lib.UpsertAlbum(ctx, &domain.Album{
		ArtistID: libArtistID, Title: "Later Match", Year: 2001,
		ExternalIDs: map[string]string{"deezer": "alb-later"},
	}); err != nil {
		t.Fatalf("seed newly-matched library album: %v", err)
	}

	// Act: re-refresh the discography.
	if _, err := h.svc.RefreshArtist(ctx, artist.ID); err != nil {
		t.Fatalf("RefreshArtist: %v", err)
	}

	// Assert: newly-matched wanted album is promoted, and the user's per-album
	// toggle is preserved through the promotion.
	later = h.albumByProvider(t, artist.ID, "alb-later")
	if later.Status != domain.AlbumStatusDownloaded {
		t.Errorf("newly-matched album status = %q, want downloaded (promotion)", later.Status)
	}
	if later.Monitored {
		t.Errorf("newly-matched album Monitored = true, want the user's false toggle preserved")
	}

	// Terminal "ignored" never regresses.
	skip = h.albumByProvider(t, artist.ID, "alb-skip")
	if skip.Status != domain.AlbumStatusIgnored {
		t.Errorf("ignored album status = %q, want ignored (terminal status must not regress)", skip.Status)
	}

	// A downloaded album stays downloaded.
	inLib := h.albumByProvider(t, artist.ID, "alb-lib")
	if inLib.Status != domain.AlbumStatusDownloaded {
		t.Errorf("downloaded album status = %q, want downloaded", inLib.Status)
	}
}
