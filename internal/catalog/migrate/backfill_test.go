package migrate

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	catalogsqlite "github.com/ramonskie/groovearr/internal/catalog/sqlite"

	_ "modernc.org/sqlite"
)

// ─── Harness ─────────────────────────────────────────────────────────

// newHarness opens an in-memory SQLite database with the minimal library tables
// the catalog foreign-keys point at plus the exact legacy cache tables, then
// builds the catalog store on the same connection. It returns the raw *sql.DB
// (for seeding legacy rows / asserting queue state) and the catalog.Store.
func newHarness(t *testing.T) (*sql.DB, catalog.Store) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1) // single shared connection = one in-memory database
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS artists (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS albums (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			artist_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			FOREIGN KEY (artist_id) REFERENCES artists(id) ON DELETE CASCADE
		)`,
		// Exact legacy schema (read-only in Backfill; left in place).
		`CREATE TABLE IF NOT EXISTS album_discovery_cache (
			album_id          INTEGER PRIMARY KEY,
			provider_name     TEXT NOT NULL,
			provider_album_id TEXT NOT NULL,
			tracks_json       TEXT NOT NULL,
			cached_at         TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (album_id) REFERENCES albums(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS artist_overview_cache (
			normalized_name    TEXT PRIMARY KEY,
			artist_name        TEXT NOT NULL DEFAULT '',
			provider_name      TEXT NOT NULL,
			provider_artist_id TEXT NOT NULL,
			image_url          TEXT NOT NULL DEFAULT '',
			genres_json        TEXT NOT NULL DEFAULT '[]',
			top_tracks_json    TEXT,
			discography_json   TEXT NOT NULL,
			cached_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	store, err := catalogsqlite.New(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("new catalog store: %v", err)
	}
	return db, store
}

func insertArtist(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO artists (name) VALUES (?)`, name)
	if err != nil {
		t.Fatalf("insert artist: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertAlbum(t *testing.T, db *sql.DB, artistID int64, title string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO albums (artist_id, title) VALUES (?, ?)`, artistID, title)
	if err != nil {
		t.Fatalf("insert album: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedAlbumCache(t *testing.T, db *sql.DB, albumID int64, provider, providerAlbumID, tracksJSON, cachedAt string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO album_discovery_cache (album_id, provider_name, provider_album_id, tracks_json, cached_at)
		 VALUES (?, ?, ?, ?, ?)`,
		albumID, provider, providerAlbumID, tracksJSON, cachedAt); err != nil {
		t.Fatalf("seed album cache: %v", err)
	}
}

func seedArtistCache(t *testing.T, db *sql.DB, normalized, name, provider, providerArtistID, imageURL, genresJSON, topTracksJSON, discographyJSON, cachedAt string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO artist_overview_cache
		 (normalized_name, artist_name, provider_name, provider_artist_id, image_url, genres_json, top_tracks_json, discography_json, cached_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized, name, provider, providerArtistID, imageURL, genresJSON, topTracksJSON, discographyJSON, cachedAt); err != nil {
		t.Fatalf("seed artist cache: %v", err)
	}
}

func runBackfill(t *testing.T, db *sql.DB, store catalog.Store) Result {
	t.Helper()
	res, err := Backfill(context.Background(), db, store, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	return res
}

// ─── Album cache ─────────────────────────────────────────────────────

func TestBackfillDecodesProviderAlbumCache(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Aphex Twin")
	albumID := insertAlbum(t, db, artistID, "Selected Ambient Works")

	const cachedAt = "2026-01-02T03:04:05Z"
	seedAlbumCache(t, db, albumID, "deezer", "12345",
		`[{"provider_id":"t1","artist_name":"Aphex Twin","title":"Xtal","track_number":1,"disc_number":1,"duration_ms":300000,"isrc":"ISRC1"},
		  {"provider_id":"t2","artist_name":"Aphex Twin","title":"Tha","track_number":2,"disc_number":1,"duration_ms":500000}]`,
		cachedAt)

	res := runBackfill(t, db, store)
	if res.AlbumsDecoded != 1 || res.TracksDecoded != 2 {
		t.Fatalf("result = %+v, want albums=1 tracks=2", res)
	}

	tracks, err := store.GetAlbumTracks(ctx, albumID)
	if err != nil {
		t.Fatalf("get album tracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("album tracks = %d, want 2", len(tracks))
	}
	if tracks[0].Title != "Xtal" || tracks[0].Origin != catalog.OriginProvider ||
		tracks[0].ProviderName != "deezer" || tracks[0].ProviderTrackID != "t1" {
		t.Fatalf("track[0] = %+v", tracks[0])
	}
	if tracks[1].TrackNumber != 2 || tracks[1].DurationMs != 500000 {
		t.Fatalf("track[1] = %+v", tracks[1])
	}

	meta, err := store.GetAlbumMetadata(ctx, albumID)
	if err != nil {
		t.Fatalf("get album metadata: %v", err)
	}
	if meta == nil {
		t.Fatal("album metadata missing")
	}
	wantTime, _ := time.Parse(time.RFC3339, cachedAt)
	if meta.ProviderName != "deezer" || meta.ProviderAlbumID != "12345" ||
		meta.RefreshStatus != catalog.StatusFresh || !meta.LastRefreshedAt.Equal(wantTime) {
		t.Fatalf("album metadata = %+v", meta)
	}
}

func TestBackfillDecodesLibraryAlbumCache(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Boards of Canada")
	albumID := insertAlbum(t, db, artistID, "Music Has the Right to Children")

	seedAlbumCache(t, db, albumID, "library", "",
		`[{"title":"Wildlife Analysis","track_number":1,"duration_ms":70000},
		  {"title":"An Eagle in Your Mind","track_number":2,"duration_ms":300000}]`,
		"2026-01-02T03:04:05Z")

	runBackfill(t, db, store)

	tracks, err := store.GetAlbumTracks(ctx, albumID)
	if err != nil {
		t.Fatalf("get album tracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("album tracks = %d, want 2", len(tracks))
	}
	if tracks[0].Origin != catalog.OriginLibrary || tracks[0].ProviderName != "" {
		t.Fatalf("track[0] = %+v, want library origin with empty provider", tracks[0])
	}
	if tracks[0].DiscNumber != 1 {
		t.Fatalf("disc number = %d, want normalized 1", tracks[0].DiscNumber)
	}

	meta, _ := store.GetAlbumMetadata(ctx, albumID)
	if meta == nil || meta.RefreshStatus != catalog.StatusFresh {
		t.Fatalf("album metadata = %+v", meta)
	}
	if meta.ProviderName != "" || meta.ProviderAlbumID != "" {
		t.Fatalf("library row leaked provider link: %+v", meta)
	}
}

// ─── Artist cache ────────────────────────────────────────────────────

func TestBackfillDecodesArtistCache(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Daft Punk")

	const cachedAt = "2026-02-03T04:05:06Z"
	seedArtistCache(t, db, "daftpunk", "Daft Punk", "lastfm", "p1", "http://img/cover.jpg",
		`["electronic","house"]`, `[{"title":"One More Time","track_number":1}]`,
		`{"album":3,"single":5}`, cachedAt)

	res := runBackfill(t, db, store)
	if res.ArtistsDecoded != 1 {
		t.Fatalf("artists decoded = %d, want 1 (%+v)", res.ArtistsDecoded, res)
	}
	// The legacy discography blob is a type->count map with no release detail, so
	// no discography rows are materialized (and it is not treated as malformed).
	if res.ReleasesDecoded != 0 {
		t.Fatalf("releases decoded = %d, want 0 for count-map discography", res.ReleasesDecoded)
	}

	meta, err := store.GetArtistMetadata(ctx, artistID)
	if err != nil {
		t.Fatalf("get artist metadata: %v", err)
	}
	if meta == nil {
		t.Fatal("artist metadata missing")
	}
	wantTime, _ := time.Parse(time.RFC3339, cachedAt)
	if meta.ProviderName != "lastfm" || meta.ProviderArtistID != "p1" ||
		meta.ImageURL != "http://img/cover.jpg" || meta.RefreshStatus != catalog.StatusFresh ||
		!meta.LastRefreshedAt.Equal(wantTime) {
		t.Fatalf("artist metadata = %+v", meta)
	}
	if len(meta.Genres) != 2 || meta.Genres[0] != "electronic" || meta.Genres[1] != "house" {
		t.Fatalf("genres = %v, want [electronic house]", meta.Genres)
	}

	releases, err := store.GetArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("get discography: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("discography = %d rows, want 0 (count map has no release detail)", len(releases))
	}
}

// TestBackfillDecodesArtistDiscographyArrayForm covers the defensive
// array-of-releases decoder so a release-shaped blob is materialized rather than
// silently dropped.
func TestBackfillDecodesArtistDiscographyArrayForm(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Radiohead")

	seedArtistCache(t, db, "radiohead", "Radiohead", "spotify", "p2", "",
		`["rock"]`, `[]`,
		`[{"provider_album_id":"a1","title":"Kid A","year":2000,"type":"album","cover_url":"http://c/a1.jpg","track_count":10},
		  {"provider_id":"a2","title":"Amnesiac","year":2001,"type":"album","cover":"http://c/a2.jpg","track_count":11}]`,
		"2026-03-04T05:06:07Z")

	res := runBackfill(t, db, store)
	if res.ReleasesDecoded != 2 {
		t.Fatalf("releases decoded = %d, want 2 (%+v)", res.ReleasesDecoded, res)
	}

	releases, err := store.GetArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("get discography: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("discography = %d rows, want 2", len(releases))
	}
	// Ordered by year then title.
	if releases[0].Title != "Kid A" || releases[0].ProviderAlbumID != "a1" || releases[0].CoverURL != "http://c/a1.jpg" {
		t.Fatalf("release[0] = %+v", releases[0])
	}
	if releases[1].Title != "Amnesiac" || releases[1].ProviderAlbumID != "a2" || releases[1].CoverURL != "http://c/a2.jpg" {
		t.Fatalf("release[1] = %+v", releases[1])
	}
}

func TestBackfillSkipsArtistWithNoLocalMatch(t *testing.T) {
	db, store := newHarness(t)
	insertAlbum(t, db, insertArtist(t, db, "Someone Else"), "Album")

	seedArtistCache(t, db, "ghost", "Ghost Artist", "lastfm", "p9", "",
		`["ambient"]`, `[]`, `{"album":1}`, "2026-01-01T00:00:00Z")

	res := runBackfill(t, db, store)
	if res.ArtistsDecoded != 0 {
		t.Fatalf("artists decoded = %d, want 0 (no local match)", res.ArtistsDecoded)
	}
	if res.Skipped == 0 {
		t.Fatalf("expected the unmatched artist to be counted as skipped: %+v", res)
	}
}

// ─── Remainder enqueue ───────────────────────────────────────────────

func TestBackfillEnqueuesUnmaterializedRemainder(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Massive Attack")
	cachedAlbum := insertAlbum(t, db, artistID, "Mezzanine")
	remainderAlbum := insertAlbum(t, db, artistID, "Blue Lines")

	// One album has a legacy cache row (materialized), one does not (remainder).
	seedAlbumCache(t, db, cachedAlbum, "deezer", "d1",
		`[{"provider_id":"t1","title":"Angel","track_number":1,"disc_number":1}]`,
		"2026-01-02T03:04:05Z")

	res := runBackfill(t, db, store)
	if res.Enqueued != 1 {
		t.Fatalf("enqueued = %d, want exactly 1 (the unmaterialized album): %+v", res.Enqueued, res)
	}

	items, err := store.ClaimDueRefresh(ctx, time.Now().UTC().Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("claim due refresh: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue items = %d, want 1", len(items))
	}
	if items[0].EntityType != catalog.EntityAlbum || items[0].EntityID != remainderAlbum {
		t.Fatalf("queue item = %+v, want album %d", items[0], remainderAlbum)
	}
	if items[0].Reason != catalog.ReasonBulk || items[0].Priority != catalog.PriorityLow {
		t.Fatalf("queue item reason/priority = %q/%d, want bulk/low", items[0].Reason, items[0].Priority)
	}
}

func TestBackfillEmptyLegacyTablesIsNoOp(t *testing.T) {
	db, store := newHarness(t)
	insertAlbum(t, db, insertArtist(t, db, "Nobody"), "Unmaterialized")

	// No legacy cache rows at all: the guard must skip the remainder enqueue too,
	// so a fresh/never-cached instance pays no cost at startup.
	res := runBackfill(t, db, store)
	if res != (Result{}) {
		t.Fatalf("result = %+v, want zero value", res)
	}
	count, err := store.CountQueuedRefresh(context.Background())
	if err != nil {
		t.Fatalf("count queued: %v", err)
	}
	if count != 0 {
		t.Fatalf("queued = %d, want 0 when legacy tables are empty", count)
	}
}

// ─── Malformed + idempotency ─────────────────────────────────────────

func TestBackfillSkipsMalformedAlbumRow(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Broken Cache")
	albumID := insertAlbum(t, db, artistID, "Bad JSON")

	seedAlbumCache(t, db, albumID, "deezer", "d1", `{not valid json`, "2026-01-02T03:04:05Z")

	res := runBackfill(t, db, store)
	if res.AlbumsDecoded != 0 {
		t.Fatalf("albums decoded = %d, want 0", res.AlbumsDecoded)
	}
	if res.Skipped == 0 {
		t.Fatalf("expected the malformed row to be skipped: %+v", res)
	}

	tracks, err := store.GetAlbumTracks(ctx, albumID)
	if err != nil {
		t.Fatalf("get album tracks: %v", err)
	}
	if len(tracks) != 0 {
		t.Fatalf("album tracks = %d, want 0 for malformed row", len(tracks))
	}
	// The album is now known to be unmaterialized, so it is enqueued for a real
	// provider refresh.
	if res.Enqueued != 1 {
		t.Fatalf("enqueued = %d, want 1 (malformed album becomes remainder)", res.Enqueued)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Idempotent Artist")
	cachedAlbum := insertAlbum(t, db, artistID, "Cached")
	remainderAlbum := insertAlbum(t, db, artistID, "Remainder")

	seedAlbumCache(t, db, cachedAlbum, "deezer", "d1",
		`[{"provider_id":"t1","title":"A","track_number":1,"disc_number":1},
		  {"provider_id":"t2","title":"B","track_number":2,"disc_number":1}]`,
		"2026-01-02T03:04:05Z")
	seedArtistCache(t, db, "idempotentartist", "Idempotent Artist", "lastfm", "p1", "",
		`["pop"]`, `[]`, `{"album":2}`, "2026-01-02T03:04:05Z")

	first := runBackfill(t, db, store)
	second := runBackfill(t, db, store)

	tracks, err := store.GetAlbumTracks(ctx, cachedAlbum)
	if err != nil {
		t.Fatalf("get album tracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("album tracks after two runs = %d, want 2 (replace semantics)", len(tracks))
	}

	// The remainder album was enqueued once; re-running must not duplicate the
	// queue row (the (entity_type, entity_id) upsert re-arms it in place).
	if second.Enqueued != 1 {
		t.Fatalf("second run enqueued = %d, want 1", second.Enqueued)
	}
	count, err := store.CountQueuedRefresh(ctx)
	if err != nil {
		t.Fatalf("count queued: %v", err)
	}
	if count != 1 {
		t.Fatalf("queued after two runs = %d, want 1 (no duplicate)", count)
	}

	if first.AlbumsDecoded != second.AlbumsDecoded || first.ArtistsDecoded != second.ArtistsDecoded {
		t.Fatalf("second run changed decode counts: first=%+v second=%+v", first, second)
	}
	_ = remainderAlbum
}

func TestBackfillNilStoreIsNoOp(t *testing.T) {
	db, _ := newHarness(t)
	res, err := Backfill(context.Background(), db, nil, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("Backfill with nil store: %v", err)
	}
	if res != (Result{}) {
		t.Fatalf("result = %+v, want zero value", res)
	}
}

// ─── Lifecycle ───────────────────────────────────────────────────────

func TestRunnerStartStop(t *testing.T) {
	db, store := newHarness(t)
	albumID := insertAlbum(t, db, insertArtist(t, db, "Runner"), "Album")
	seedAlbumCache(t, db, albumID, "deezer", "d1",
		`[{"provider_id":"t1","title":"A","track_number":1,"disc_number":1}]`,
		"2026-01-02T03:04:05Z")

	r := Start(context.Background(), db, store, slog.New(slog.DiscardHandler), nil)
	r.Stop() // must not block once the goroutine has exited (or been cancelled)

	// A second Stop is a no-op.
	r.Stop()
}

// ─── Terminal-album retry window (M5) ────────────────────────────────

// seedTerminalAlbumMeta parks albumID with a terminal status written at
// updatedAt, so the retry-window exclusion can be exercised.
func seedTerminalAlbumMeta(t *testing.T, store catalog.Store, albumID int64, status catalog.RefreshStatus, updatedAt time.Time) {
	t.Helper()
	if err := store.UpsertAlbumMetadata(context.Background(), catalog.AlbumMetadata{
		AlbumID:       albumID,
		RefreshStatus: status,
		UpdatedAt:     updatedAt,
	}); err != nil {
		t.Fatalf("seed terminal album metadata: %v", err)
	}
}

// seedDecodedAlbum inserts and materializes the album that keeps the legacy
// tables non-empty, so enqueueRemainder is actually reached.
func seedDecodedAlbum(t *testing.T, db *sql.DB, store catalog.Store, artistID int64) int64 {
	t.Helper()
	id := insertAlbum(t, db, artistID, "Decoded")
	seedAlbumCache(t, db, id, "deezer", "d1",
		`[{"provider_id":"t1","title":"A","track_number":1,"disc_number":1}]`,
		"2026-01-02T03:04:05Z")
	_ = store
	return id
}

// TestBackfillDoesNotReArmFreshTerminalAlbum proves that a boot-time remainder
// enqueue leaves an unmatched/failed album alone while its long retry has not
// elapsed, so the queue keeps draining instead of churning.
func TestBackfillDoesNotReArmFreshTerminalAlbum(t *testing.T) {
	for _, status := range []catalog.RefreshStatus{catalog.StatusUnmatched, catalog.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			db, store := newHarness(t)
			artistID := insertArtist(t, db, "Parked")
			seedDecodedAlbum(t, db, store, artistID)
			parked := insertAlbum(t, db, artistID, "Parked")

			seedTerminalAlbumMeta(t, store, parked, status, time.Now().UTC())

			res := runBackfill(t, db, store)
			if res.Enqueued != 0 {
				t.Fatalf("enqueued = %d, want 0 (fresh terminal album must not be re-armed): %+v", res.Enqueued, res)
			}
			count, err := store.CountQueuedRefresh(context.Background())
			if err != nil {
				t.Fatalf("count queued: %v", err)
			}
			if count != 0 {
				t.Fatalf("queued = %d, want 0", count)
			}
		})
	}
}

// TestBackfillReArmsTerminalAlbumAfterRetryElapsed proves the exclusion is a
// window, not permanent: once the 90-day retry has elapsed the album is enqueued
// again.
func TestBackfillReArmsTerminalAlbumAfterRetryElapsed(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Parked")
	seedDecodedAlbum(t, db, store, artistID)
	parked := insertAlbum(t, db, artistID, "Parked")

	seedTerminalAlbumMeta(t, store, parked, catalog.StatusUnmatched,
		time.Now().UTC().Add(-catalog.UnmatchedRetry-time.Hour))

	res := runBackfill(t, db, store)
	if res.Enqueued != 1 {
		t.Fatalf("enqueued = %d, want 1 after the retry elapsed: %+v", res.Enqueued, res)
	}
	items, err := store.ClaimDueRefresh(ctx, time.Now().UTC().Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("claim due: %v", err)
	}
	if len(items) != 1 || items[0].EntityID != parked {
		t.Fatalf("items = %+v, want only parked album %d", items, parked)
	}
}

// TestBackfillLeavesParkedQueueRowUntouched proves a re-enqueue does not reset a
// parked row's due_at/attempts: the excluded album keeps the exact retry the
// engine scheduled.
func TestBackfillLeavesParkedQueueRowUntouched(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Parked")
	seedDecodedAlbum(t, db, store, artistID)
	parked := insertAlbum(t, db, artistID, "Parked")
	seedTerminalAlbumMeta(t, store, parked, catalog.StatusUnmatched, time.Now().UTC())

	future := time.Now().UTC().Add(catalog.UnmatchedRetry)
	id, err := store.EnqueueRefresh(ctx, catalog.RefreshItem{
		EntityType: catalog.EntityAlbum,
		EntityID:   parked,
		Reason:     catalog.ReasonBulk,
		Priority:   catalog.PriorityLow,
		DueAt:      future,
		CreatedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed queue row: %v", err)
	}

	runBackfill(t, db, store)

	if items, _ := store.ClaimDueRefresh(ctx, future.Add(-time.Minute), 10); len(items) != 0 {
		t.Fatalf("parked row became due early: %+v", items)
	}
	items, _ := store.ClaimDueRefresh(ctx, future.Add(time.Minute), 10)
	if len(items) != 1 || items[0].ID != id || items[0].Attempts != 0 {
		t.Fatalf("parked row = %+v, want the untouched row %d (attempts 0)", items, id)
	}
}

// ─── Enabled gate (F2) ───────────────────────────────────────────────

func TestBackfillDisabledSkipsEverything(t *testing.T) {
	db, store := newHarness(t)
	ctx := context.Background()
	artistID := insertArtist(t, db, "Disabled")
	albumID := insertAlbum(t, db, artistID, "Cached")
	seedAlbumCache(t, db, albumID, "deezer", "d1",
		`[{"provider_id":"t1","title":"A","track_number":1,"disc_number":1}]`,
		"2026-01-02T03:04:05Z")

	res, err := Backfill(ctx, db, store, slog.New(slog.DiscardHandler), func() bool { return false })
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res != (Result{}) {
		t.Fatalf("result = %+v, want zero when disabled", res)
	}
	if tracks, _ := store.GetAlbumTracks(ctx, albumID); len(tracks) != 0 {
		t.Fatalf("tracks = %d, want 0 (disabled must not decode)", len(tracks))
	}
	if count, _ := store.CountQueuedRefresh(ctx); count != 0 {
		t.Fatalf("queued = %d, want 0", count)
	}
}

func TestBackfillConsultsEnabledGate(t *testing.T) {
	db, store := newHarness(t)
	artistID := insertArtist(t, db, "Gate")
	albumID := insertAlbum(t, db, artistID, "Cached")
	seedAlbumCache(t, db, albumID, "deezer", "d1",
		`[{"provider_id":"t1","title":"A","track_number":1,"disc_number":1}]`,
		"2026-01-02T03:04:05Z")

	calls := 0
	res, err := Backfill(context.Background(), db, store, slog.New(slog.DiscardHandler), func() bool {
		calls++
		return true
	})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.AlbumsDecoded != 1 {
		t.Fatalf("albums decoded = %d, want 1 with the gate enabled", res.AlbumsDecoded)
	}
	if calls != 1 {
		t.Fatalf("gate calls = %d, want exactly 1", calls)
	}
}

// ─── Start/Stop race (L12) ───────────────────────────────────────────

// TestRunnerStartStopRaceClean runs startWork and Stop on the same Runner
// concurrently; under -race the guarded cancel/done publication must not report
// a data race.
func TestRunnerStartStopRaceClean(t *testing.T) {
	db, store := newHarness(t)
	artistID := insertArtist(t, db, "Race")
	albumID := insertAlbum(t, db, artistID, "Album")
	seedAlbumCache(t, db, albumID, "deezer", "d1",
		`[{"provider_id":"t1","title":"A","track_number":1,"disc_number":1}]`,
		"2026-01-02T03:04:05Z")

	r := &Runner{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.startWork(context.Background(), db, store, slog.New(slog.DiscardHandler), nil)
	}()
	go func() {
		defer wg.Done()
		r.Stop()
	}()
	wg.Wait()
	r.Stop() // drain whatever the overlapping start launched
}
