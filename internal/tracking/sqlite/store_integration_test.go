package sqlite_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/tracking"
	tracksqlite "github.com/ramonskie/groovearr/internal/tracking/sqlite"
)

// Compile-time proof the concrete store satisfies the full tracking.Store contract.
var _ tracking.Store = (*tracksqlite.Store)(nil)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestStore opens a real SQLite database through the library store (so the
// shared schema, including tracked_artists/tracked_albums and foreign_keys=on,
// is applied) and wraps its connection with the tracking store — exactly how
// app.go wires quality and download stores. No network is involved.
func newTestStore(t *testing.T) (*tracksqlite.Store, *libsqlite.Store) {
	t.Helper()
	libStore, err := libsqlite.New(t.TempDir()+"/tracking.db", discardLogger())
	if err != nil {
		t.Fatalf("open shared sqlite: %v", err)
	}
	t.Cleanup(func() { _ = libStore.Close() })
	return tracksqlite.NewSQLiteStore(libStore.DB()), libStore
}

func TestTrackedArtistCRUDAndMonitor(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	a := &domain.TrackedArtist{
		Name:             "Aphex Twin",
		ProviderName:     "musicbrainz",
		ProviderArtistID: "mb-artist-1",
	}
	id, err := store.CreateTrackedArtist(ctx, a)
	if err != nil {
		t.Fatalf("CreateTrackedArtist: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateTrackedArtist returned zero id")
	}

	byID, err := store.GetTrackedArtist(ctx, id)
	if err != nil {
		t.Fatalf("GetTrackedArtist: %v", err)
	}
	if byID == nil || byID.Name != "Aphex Twin" || byID.ProviderName != "musicbrainz" ||
		byID.ProviderArtistID != "mb-artist-1" {
		t.Fatalf("GetTrackedArtist = %+v; want Aphex Twin/musicbrainz/mb-artist-1", byID)
	}
	if byID.Monitored {
		t.Error("new artist Monitored = true, want false")
	}
	if byID.MonitorMode != domain.MonitorModeAll {
		t.Errorf("new artist MonitorMode = %q, want %q", byID.MonitorMode, domain.MonitorModeAll)
	}
	if byID.CreatedAt.IsZero() || byID.UpdatedAt.IsZero() {
		t.Errorf("timestamps not persisted: created=%v updated=%v", byID.CreatedAt, byID.UpdatedAt)
	}
	if byID.LibraryArtistID != nil {
		t.Errorf("new artist LibraryArtistID = %v, want nil", *byID.LibraryArtistID)
	}
	if byID.LastRefreshedAt != nil {
		t.Errorf("new artist LastRefreshedAt = %v, want nil", *byID.LastRefreshedAt)
	}

	byProvider, err := store.GetTrackedArtistByProvider(ctx, "musicbrainz", "mb-artist-1")
	if err != nil {
		t.Fatalf("GetTrackedArtistByProvider: %v", err)
	}
	if byProvider == nil || byProvider.ID != id {
		t.Fatalf("GetTrackedArtistByProvider = %+v; want id %d", byProvider, id)
	}

	// Not-found contract: (nil, nil), never an error.
	if got, err := store.GetTrackedArtist(ctx, 999999); err != nil || got != nil {
		t.Errorf("GetTrackedArtist(missing) = (%v, %v); want (nil, nil)", got, err)
	}
	if got, err := store.GetTrackedArtistByProvider(ctx, "nope", "nope"); err != nil || got != nil {
		t.Errorf("GetTrackedArtistByProvider(missing) = (%v, %v); want (nil, nil)", got, err)
	}

	list, err := store.ListTrackedArtists(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTrackedArtists = %d rows, err=%v; want 1", len(list), err)
	}

	// Re-creating the same provider pair upserts in place (no duplicate) and
	// refreshes the provider-supplied name.
	id2, err := store.CreateTrackedArtist(ctx, &domain.TrackedArtist{
		Name:             "Aphex Twin (canonical)",
		ProviderName:     "musicbrainz",
		ProviderArtistID: "mb-artist-1",
	})
	if err != nil {
		t.Fatalf("CreateTrackedArtist upsert: %v", err)
	}
	if id2 != id {
		t.Fatalf("upsert returned id %d, want %d (same provider pair)", id2, id)
	}
	list, err = store.ListTrackedArtists(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("after upsert ListTrackedArtists = %d rows, err=%v; want 1", len(list), err)
	}
	if list[0].Name != "Aphex Twin (canonical)" {
		t.Errorf("upsert name = %q, want canonical name", list[0].Name)
	}

	// Toggle monitoring.
	if err := store.UpdateArtistMonitor(ctx, id, true, domain.MonitorModeFuture); err != nil {
		t.Fatalf("UpdateArtistMonitor: %v", err)
	}
	byID, err = store.GetTrackedArtist(ctx, id)
	if err != nil {
		t.Fatalf("GetTrackedArtist after monitor: %v", err)
	}
	if !byID.Monitored || byID.MonitorMode != domain.MonitorModeFuture {
		t.Errorf("monitor state = (%v, %q); want (true, future)", byID.Monitored, byID.MonitorMode)
	}
}

func TestUpsertTrackedAlbumAndListWanted(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-a2", "Boards of Canada")

	wanted := &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-wanted", ProviderName: "musicbrainz",
		Title: "Music Has the Right to Children", Year: 1998,
		Monitored: true, Status: domain.AlbumStatusWanted,
	}
	downloading := &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-downloading", ProviderName: "musicbrainz",
		Title: "Geogaddi", Year: 2002, Monitored: true, Status: domain.AlbumStatusDownloading,
	}
	unmonitored := &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-unmonitored", ProviderName: "musicbrainz",
		Title: "Tomorrow's Harvest", Year: 2013, Monitored: false, Status: domain.AlbumStatusWanted,
	}
	ignored := &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-ignored", ProviderName: "musicbrainz",
		Title: "Campfire Headphase", Year: 2005, Monitored: true, Status: domain.AlbumStatusIgnored,
	}

	wantedID, err := store.UpsertTrackedAlbum(ctx, wanted)
	if err != nil {
		t.Fatalf("UpsertTrackedAlbum(wanted): %v", err)
	}
	if wantedID == 0 {
		t.Fatal("UpsertTrackedAlbum returned zero id")
	}
	for _, a := range []*domain.TrackedAlbum{downloading, unmonitored, ignored} {
		if _, err := store.UpsertTrackedAlbum(ctx, a); err != nil {
			t.Fatalf("UpsertTrackedAlbum(%s): %v", a.ProviderAlbumID, err)
		}
	}

	// Idempotent upsert on (artist, provider album): same id, provider metadata
	// refreshed, user/lifecycle state preserved.
	wanted.Title = "Music Has the Right to Children (Remaster)"
	wanted.Monitored = false // must NOT overwrite the stored value
	wanted.Status = domain.AlbumStatusIgnored
	again, err := store.UpsertTrackedAlbum(ctx, wanted)
	if err != nil {
		t.Fatalf("UpsertTrackedAlbum(again): %v", err)
	}
	if again != wantedID {
		t.Fatalf("album upsert returned id %d, want %d", again, wantedID)
	}
	got, err := store.GetTrackedAlbumByProvider(ctx, artistID, "alb-wanted")
	if err != nil {
		t.Fatalf("GetTrackedAlbumByProvider: %v", err)
	}
	if got == nil {
		t.Fatal("GetTrackedAlbumByProvider returned nil for existing album")
	}
	if got.Title != "Music Has the Right to Children (Remaster)" {
		t.Errorf("upsert title = %q, want refreshed title", got.Title)
	}
	if !got.Monitored || got.Status != domain.AlbumStatusWanted {
		t.Errorf("upsert overwrote user state: monitored=%v status=%q; want (true, wanted)",
			got.Monitored, got.Status)
	}
	if got.FirstSeenAt.IsZero() || got.LastSeenAt.IsZero() {
		t.Errorf("album timestamps not persisted: first=%v last=%v", got.FirstSeenAt, got.LastSeenAt)
	}

	if got, err := store.GetTrackedAlbumByProvider(ctx, artistID, "missing"); err != nil || got != nil {
		t.Errorf("GetTrackedAlbumByProvider(missing) = (%v, %v); want (nil, nil)", got, err)
	}

	all, err := store.ListTrackedAlbums(ctx, artistID)
	if err != nil || len(all) != 4 {
		t.Fatalf("ListTrackedAlbums = %d rows, err=%v; want 4", len(all), err)
	}

	// Wanted = monitored AND status in (wanted, downloading): only two qualify.
	worklist, err := store.ListWantedAlbums(ctx)
	if err != nil {
		t.Fatalf("ListWantedAlbums: %v", err)
	}
	if len(worklist) != 2 {
		t.Fatalf("ListWantedAlbums = %d rows, want 2: %+v", len(worklist), worklist)
	}
	gotIDs := map[string]bool{}
	for _, a := range worklist {
		gotIDs[a.ProviderAlbumID] = true
	}
	if !gotIDs["alb-wanted"] || !gotIDs["alb-downloading"] {
		t.Errorf("ListWantedAlbums ids = %v; want alb-wanted + alb-downloading", gotIDs)
	}
}

func TestAlbumStatusAndMonitor(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-a3", "Autechre")
	albumID, err := store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-ae", ProviderName: "musicbrainz",
		Title: "Amber", Year: 1994, Monitored: true, Status: domain.AlbumStatusWanted,
	})
	if err != nil {
		t.Fatalf("UpsertTrackedAlbum: %v", err)
	}

	// Toggle album monitoring off → drops out of the wanted worklist.
	if err := store.UpdateAlbumMonitor(ctx, albumID, false); err != nil {
		t.Fatalf("UpdateAlbumMonitor: %v", err)
	}
	got, err := store.GetTrackedAlbumByProvider(ctx, artistID, "alb-ae")
	if err != nil {
		t.Fatalf("GetTrackedAlbumByProvider: %v", err)
	}
	if got.Monitored {
		t.Error("UpdateAlbumMonitor(false) did not clear Monitored")
	}
	if wl, err := store.ListWantedAlbums(ctx); err != nil || len(wl) != 0 {
		t.Fatalf("ListWantedAlbums after unmonitor = %d rows, err=%v; want 0", len(wl), err)
	}
	if err := store.UpdateAlbumMonitor(ctx, albumID, true); err != nil {
		t.Fatalf("UpdateAlbumMonitor(true): %v", err)
	}

	// MarkAlbumStatus drives the lifecycle; downloading still counts as wanted,
	// downloaded does not.
	if err := store.MarkAlbumStatus(ctx, albumID, domain.AlbumStatusDownloading); err != nil {
		t.Fatalf("MarkAlbumStatus(downloading): %v", err)
	}
	if wl, err := store.ListWantedAlbums(ctx); err != nil || len(wl) != 1 {
		t.Fatalf("ListWantedAlbums(downloading) = %d rows, err=%v; want 1", len(wl), err)
	}
	if err := store.MarkAlbumStatus(ctx, albumID, domain.AlbumStatusDownloaded); err != nil {
		t.Fatalf("MarkAlbumStatus(downloaded): %v", err)
	}
	got, _ = store.GetTrackedAlbumByProvider(ctx, artistID, "alb-ae")
	if got.Status != domain.AlbumStatusDownloaded {
		t.Errorf("status = %q, want downloaded", got.Status)
	}
	if wl, err := store.ListWantedAlbums(ctx); err != nil || len(wl) != 0 {
		t.Fatalf("ListWantedAlbums(downloaded) = %d rows, err=%v; want 0", len(wl), err)
	}
}

// TestUpsertTrackedAlbumStatusForwardOnly pins the conflict-update semantics:
// a wanted album is promoted to downloaded, but a stored downloaded or ignored
// status never regresses, and monitored is never overwritten by an upsert.
func TestUpsertTrackedAlbumStatusForwardOnly(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-fwd", "Autechre")
	upsert := func(pid string, monitored bool, status domain.AlbumStatus) {
		t.Helper()
		if _, err := store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
			TrackedArtistID: artistID, ProviderAlbumID: pid, ProviderName: "musicbrainz",
			Title: "Album " + pid, Year: 1994, Monitored: monitored, Status: status,
		}); err != nil {
			t.Fatalf("UpsertTrackedAlbum(%s/%s): %v", pid, status, err)
		}
	}
	statusOf := func(pid string) domain.TrackedAlbum {
		t.Helper()
		got, err := store.GetTrackedAlbumByProvider(ctx, artistID, pid)
		if err != nil || got == nil {
			t.Fatalf("GetTrackedAlbumByProvider(%s) = (%v, %v); want a row", pid, got, err)
		}
		return *got
	}

	// wanted -> downloaded promotion.
	upsert("fwd", true, domain.AlbumStatusWanted)
	upsert("fwd", true, domain.AlbumStatusDownloaded)
	if got := statusOf("fwd"); got.Status != domain.AlbumStatusDownloaded {
		t.Fatalf("status after promotion = %q, want downloaded", got.Status)
	}

	// downloaded never regresses to a later wanted upsert.
	upsert("fwd", true, domain.AlbumStatusWanted)
	if got := statusOf("fwd"); got.Status != domain.AlbumStatusDownloaded {
		t.Fatalf("downloaded regressed to %q, want downloaded", got.Status)
	}

	// monitored is preserved: an explicit toggle off survives a monitored=true upsert.
	if err := store.UpdateAlbumMonitor(ctx, statusOf("fwd").ID, false); err != nil {
		t.Fatalf("UpdateAlbumMonitor(false): %v", err)
	}
	upsert("fwd", true, domain.AlbumStatusDownloaded)
	if got := statusOf("fwd"); got.Monitored {
		t.Error("monitored regressed to true; want preserved false")
	}

	// ignored never regresses, even against a downloaded upsert.
	upsert("ign", true, domain.AlbumStatusIgnored)
	upsert("ign", true, domain.AlbumStatusDownloaded)
	if got := statusOf("ign"); got.Status != domain.AlbumStatusIgnored {
		t.Fatalf("ignored regressed to %q, want ignored", got.Status)
	}
}

// TestUpsertTrackedAlbumPersistsLibraryLink is the regression test for the bug
// where UpsertTrackedAlbum omitted library_album_id: the link computed during
// reconcile was silently dropped, so it must survive both the initial upsert
// and a later link-less re-upsert.
func TestUpsertTrackedAlbumPersistsLibraryLink(t *testing.T) {
	ctx := context.Background()
	store, libStore := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-link", "Autechre")

	// The link must reference a real library album (foreign key).
	libArtistID, err := libStore.UpsertArtist(ctx, &domain.Artist{Name: "Autechre"})
	if err != nil {
		t.Fatalf("UpsertArtist: %v", err)
	}
	libAlbumID, err := libStore.UpsertAlbum(ctx, &domain.Album{ArtistID: libArtistID, Title: "Amber"})
	if err != nil {
		t.Fatalf("UpsertAlbum: %v", err)
	}

	link := libAlbumID
	if _, err := store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-ae", ProviderName: "musicbrainz",
		Title: "Amber", Year: 1994, Monitored: true, Status: domain.AlbumStatusDownloaded,
		LibraryAlbumID: &link,
	}); err != nil {
		t.Fatalf("UpsertTrackedAlbum(with link): %v", err)
	}
	got, err := store.GetTrackedAlbumByProvider(ctx, artistID, "alb-ae")
	if err != nil {
		t.Fatalf("GetTrackedAlbumByProvider: %v", err)
	}
	if got.LibraryAlbumID == nil || *got.LibraryAlbumID != libAlbumID {
		t.Fatalf("LibraryAlbumID = %v after upsert, want %d", got.LibraryAlbumID, libAlbumID)
	}

	// A transient miss re-upserts with a nil link; COALESCE must preserve it.
	if _, err := store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-ae", ProviderName: "musicbrainz",
		Title: "Amber", Year: 1994, Monitored: true, Status: domain.AlbumStatusWanted,
	}); err != nil {
		t.Fatalf("UpsertTrackedAlbum(nil link): %v", err)
	}
	got, err = store.GetTrackedAlbumByProvider(ctx, artistID, "alb-ae")
	if err != nil {
		t.Fatalf("GetTrackedAlbumByProvider after nil re-upsert: %v", err)
	}
	if got.LibraryAlbumID == nil || *got.LibraryAlbumID != libAlbumID {
		t.Fatalf("LibraryAlbumID = %v after nil re-upsert, want preserved %d", got.LibraryAlbumID, libAlbumID)
	}
}

// TestUpdateArtistLibraryLink verifies the tracked artist's library linkage is
// persisted and readable via UpdateArtistLibraryLink.
func TestUpdateArtistLibraryLink(t *testing.T) {
	ctx := context.Background()
	store, libStore := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-liblink", "Boards of Canada")
	libArtistID, err := libStore.UpsertArtist(ctx, &domain.Artist{Name: "Boards of Canada"})
	if err != nil {
		t.Fatalf("UpsertArtist: %v", err)
	}
	if err := store.UpdateArtistLibraryLink(ctx, artistID, libArtistID); err != nil {
		t.Fatalf("UpdateArtistLibraryLink: %v", err)
	}
	got, err := store.GetTrackedArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("GetTrackedArtist: %v", err)
	}
	if got.LibraryArtistID == nil || *got.LibraryArtistID != libArtistID {
		t.Fatalf("LibraryArtistID = %v, want %d", got.LibraryArtistID, libArtistID)
	}
}

func TestTouchArtistRefreshed(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-a4", "Squarepusher")
	before, err := store.GetTrackedArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("GetTrackedArtist: %v", err)
	}
	if before.LastRefreshedAt != nil {
		t.Fatalf("LastRefreshedAt = %v before touch, want nil", *before.LastRefreshedAt)
	}

	if err := store.TouchArtistRefreshed(ctx, artistID); err != nil {
		t.Fatalf("TouchArtistRefreshed: %v", err)
	}
	after, err := store.GetTrackedArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("GetTrackedArtist after touch: %v", err)
	}
	if after.LastRefreshedAt == nil {
		t.Fatal("LastRefreshedAt = nil after touch, want set")
	}
	if after.UpdatedAt.Before(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved backwards: before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestDeleteTrackedArtistCascades(t *testing.T) {
	ctx := context.Background()
	store, libStore := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-del", "Deleted Artist")
	for _, pid := range []string{"alb-1", "alb-2", "alb-3"} {
		if _, err := store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
			TrackedArtistID: artistID, ProviderAlbumID: pid, ProviderName: "musicbrainz",
			Title: "Album " + pid, Monitored: true, Status: domain.AlbumStatusWanted,
		}); err != nil {
			t.Fatalf("UpsertTrackedAlbum(%s): %v", pid, err)
		}
	}
	if albums, err := store.ListTrackedAlbums(ctx, artistID); err != nil || len(albums) != 3 {
		t.Fatalf("pre-delete ListTrackedAlbums = %d rows, err=%v; want 3", len(albums), err)
	}

	if err := store.DeleteTrackedArtist(ctx, artistID); err != nil {
		t.Fatalf("DeleteTrackedArtist: %v", err)
	}

	// Artist gone.
	if got, err := store.GetTrackedArtist(ctx, artistID); err != nil || got != nil {
		t.Errorf("GetTrackedArtist after delete = (%v, %v); want (nil, nil)", got, err)
	}

	// Explicit FK-cascade check straight against the shared DB, independent of
	// store read helpers.
	var remaining int
	if err := libStore.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tracked_albums WHERE tracked_artist_id = ?`, artistID,
	).Scan(&remaining); err != nil {
		t.Fatalf("count orphan albums: %v", err)
	}
	if remaining != 0 {
		t.Errorf("tracked_albums remaining after artist delete = %d, want 0 (ON DELETE CASCADE)", remaining)
	}
	if albums, err := store.ListTrackedAlbums(ctx, artistID); err != nil || len(albums) != 0 {
		t.Errorf("ListTrackedAlbums after delete = %d rows, err=%v; want 0", len(albums), err)
	}
	if wl, err := store.ListWantedAlbums(ctx); err != nil || len(wl) != 0 {
		t.Errorf("ListWantedAlbums after delete = %d rows, err=%v; want 0", len(wl), err)
	}
}

// TestMarkAlbumSearchedAndGetTrackedAlbum pins the R1 persistence: a fresh
// album has nil LastSearchedAt (migration/default), MarkAlbumSearched stamps
// it, and GetTrackedAlbum reads it back by internal ID.
func TestMarkAlbumSearchedAndGetTrackedAlbum(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	artistID := createArtist(t, ctx, store, "musicbrainz", "mb-search", "Autechre")
	albumID, err := store.UpsertTrackedAlbum(ctx, &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: "alb-searched", ProviderName: "musicbrainz",
		Title: "Amber", Year: 1994, Monitored: true, Status: domain.AlbumStatusWanted,
	})
	if err != nil {
		t.Fatalf("UpsertTrackedAlbum: %v", err)
	}

	before, err := store.GetTrackedAlbum(ctx, albumID)
	if err != nil || before == nil {
		t.Fatalf("GetTrackedAlbum = (%v, %v); want a row", before, err)
	}
	if before.LastSearchedAt != nil {
		t.Fatalf("LastSearchedAt = %v before search, want nil", *before.LastSearchedAt)
	}

	if err := store.MarkAlbumSearched(ctx, albumID); err != nil {
		t.Fatalf("MarkAlbumSearched: %v", err)
	}
	after, err := store.GetTrackedAlbum(ctx, albumID)
	if err != nil || after == nil {
		t.Fatalf("GetTrackedAlbum after search = (%v, %v); want a row", after, err)
	}
	if after.LastSearchedAt == nil {
		t.Fatal("LastSearchedAt = nil after MarkAlbumSearched, want set")
	}

	// Missing album contract: (nil, nil).
	if got, err := store.GetTrackedAlbum(ctx, 999999); err != nil || got != nil {
		t.Errorf("GetTrackedAlbum(missing) = (%v, %v); want (nil, nil)", got, err)
	}
}

// createArtist is a test helper for the common "one tracked artist" setup.
func createArtist(t *testing.T, ctx context.Context, store *tracksqlite.Store, provider, providerID, name string) int64 {
	t.Helper()
	id, err := store.CreateTrackedArtist(ctx, &domain.TrackedArtist{
		Name:             name,
		ProviderName:     provider,
		ProviderArtistID: providerID,
	})
	if err != nil {
		t.Fatalf("CreateTrackedArtist(%s): %v", providerID, err)
	}
	return id
}
