package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"

	_ "modernc.org/sqlite"
)

// ─── Test harness ────────────────────────────────────────────────────

// newTestStore opens an in-memory SQLite database with minimal stub `artists`
// and `albums` tables (so the catalog foreign keys resolve), then returns the
// catalog Store and the raw *sql.DB for seeding/deleting parent rows.
func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1) // single shared connection = one in-memory database
	t.Cleanup(func() { _ = db.Close() })

	// PRAGMA foreign_keys is per-connection; modernc ignores the DSN param.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}

	stubs := []string{
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
	}
	for _, stmt := range stubs {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create stub table: %v", err)
		}
	}

	s, err := New(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("new catalog store: %v", err)
	}
	return s, db
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

func trackKeys(tracks []catalog.AlbumTrack) []string {
	keys := make([]string, 0, len(tracks))
	for _, t := range tracks {
		keys = append(keys, strconv.Itoa(t.DiscNumber)+"/"+strconv.Itoa(t.TrackNumber))
	}
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ─── Album tracks ────────────────────────────────────────────────────

func TestAlbumTracksRoundTrip(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	albumID := insertAlbum(t, db, artistID, "Album")
	otherID := insertAlbum(t, db, artistID, "Other")

	in := []catalog.AlbumTrack{
		{DiscNumber: 1, TrackNumber: 2, Title: "Two", ArtistName: "Artist",
			DurationMs: 2000, ISRC: "ISRC2", ProviderName: "deezer",
			ProviderTrackID: "p2", Origin: catalog.OriginProvider},
		{DiscNumber: 1, TrackNumber: 1, Title: "One", ArtistName: "Artist",
			DurationMs: 1000, Origin: catalog.OriginProvider},
	}
	if err := s.ReplaceAlbumTracks(ctx, albumID, in); err != nil {
		t.Fatalf("replace: %v", err)
	}

	got, err := s.GetAlbumTracks(ctx, albumID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].TrackNumber != 1 || got[0].Title != "One" {
		t.Errorf("got[0] = %+v, want track 1 'One'", got[0])
	}
	if got[1].ProviderTrackID != "p2" || got[1].Origin != catalog.OriginProvider {
		t.Errorf("got[1] = %+v, want provider p2", got[1])
	}

	if n, err := s.CountAlbumTracks(ctx, albumID); err != nil || n != 2 {
		t.Errorf("count = %d (err %v), want 2", n, err)
	}
	// Isolation: a different album has no rows.
	if n, _ := s.CountAlbumTracks(ctx, otherID); n != 0 {
		t.Errorf("other album count = %d, want 0", n)
	}

	// Replace is delete-then-insert: a smaller set fully replaces the old one.
	if err := s.ReplaceAlbumTracks(ctx, albumID, []catalog.AlbumTrack{
		{DiscNumber: 1, TrackNumber: 1, Title: "Only", Origin: catalog.OriginLibrary},
	}); err != nil {
		t.Fatalf("replace 2: %v", err)
	}
	got, _ = s.GetAlbumTracks(ctx, albumID)
	if len(got) != 1 || got[0].Title != "Only" {
		t.Fatalf("after replace got %+v, want one 'Only'", got)
	}

	if err := s.DeleteAlbumTracks(ctx, albumID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = s.GetAlbumTracks(ctx, albumID)
	if got == nil || len(got) != 0 {
		t.Errorf("after delete got %v, want empty non-nil slice", got)
	}
	if n, _ := s.CountAlbumTracks(ctx, albumID); n != 0 {
		t.Errorf("after delete count = %d, want 0", n)
	}
}

// TestAlbumTracksOrdering is table-driven over the provider ordering contract
// (disc_number, then track_number).
func TestAlbumTracksOrdering(t *testing.T) {
	tests := []struct {
		name string
		in   []catalog.AlbumTrack
		want []string
	}{
		{
			name: "multi-disc grouped by disc then track",
			in: []catalog.AlbumTrack{
				{DiscNumber: 2, TrackNumber: 1},
				{DiscNumber: 1, TrackNumber: 2},
				{DiscNumber: 1, TrackNumber: 1},
			},
			want: []string{"1/1", "1/2", "2/1"},
		},
		{
			name: "numeric not lexicographic",
			in: []catalog.AlbumTrack{
				{DiscNumber: 1, TrackNumber: 10},
				{DiscNumber: 1, TrackNumber: 2},
			},
			want: []string{"1/2", "1/10"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, db := newTestStore(t)
			ctx := context.Background()
			artistID := insertArtist(t, db, "Artist")
			albumID := insertAlbum(t, db, artistID, "Album")

			if err := s.ReplaceAlbumTracks(ctx, albumID, tc.in); err != nil {
				t.Fatalf("replace: %v", err)
			}
			got, err := s.GetAlbumTracks(ctx, albumID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if keys := trackKeys(got); !equalStrings(keys, tc.want) {
				t.Errorf("order = %v, want %v", keys, tc.want)
			}
		})
	}
}

func TestAlbumTracksCascadeOnAlbumDelete(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	albumID := insertAlbum(t, db, artistID, "Album")
	if err := s.ReplaceAlbumTracks(ctx, albumID, []catalog.AlbumTrack{
		{DiscNumber: 1, TrackNumber: 1},
		{DiscNumber: 1, TrackNumber: 2},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM albums WHERE id=?`, albumID); err != nil {
		t.Fatalf("delete album: %v", err)
	}

	if n, err := s.CountAlbumTracks(ctx, albumID); err != nil || n != 0 {
		t.Errorf("count after cascade = %d (err %v), want 0", n, err)
	}
	got, err := s.GetAlbumTracks(ctx, albumID)
	if err != nil || len(got) != 0 {
		t.Errorf("get after cascade = %v (err %v), want empty", got, err)
	}
}

// ─── Album metadata ──────────────────────────────────────────────────

func TestAlbumMetadataUpsertGetAndNotFound(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	albumID := insertAlbum(t, db, artistID, "Album")

	if m, err := s.GetAlbumMetadata(ctx, albumID); err != nil || m != nil {
		t.Fatalf("missing get = (%v, %v), want (nil, nil)", m, err)
	}

	refreshed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	accessed := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	want := catalog.AlbumMetadata{
		AlbumID:             albumID,
		ProviderName:        "deezer",
		ProviderAlbumID:     "dz-42",
		RefreshStatus:       catalog.StatusFresh,
		LastRefreshedAt:     refreshed,
		LastAccessedAt:      accessed,
		ConsecutiveFailures: 2,
		RefreshError:        "boom",
	}
	if err := s.UpsertAlbumMetadata(ctx, want); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetAlbumMetadata(ctx, albumID)
	if err != nil || got == nil {
		t.Fatalf("get = (%v, %v), want row", got, err)
	}
	if got.ProviderName != "deezer" || got.ProviderAlbumID != "dz-42" ||
		got.RefreshStatus != catalog.StatusFresh || got.ConsecutiveFailures != 2 ||
		got.RefreshError != "boom" {
		t.Errorf("got = %+v", got)
	}
	if !got.LastRefreshedAt.Equal(refreshed) || !got.LastAccessedAt.Equal(accessed) {
		t.Errorf("times = (%v, %v), want (%v, %v)", got.LastRefreshedAt, got.LastAccessedAt, refreshed, accessed)
	}

	// Upsert overwrites — still a single row.
	want.ProviderAlbumID = "dz-99"
	want.RefreshStatus = catalog.StatusStale
	want.LastRefreshedAt = time.Time{}
	if err := s.UpsertAlbumMetadata(ctx, want); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got, _ = s.GetAlbumMetadata(ctx, albumID)
	if got.ProviderAlbumID != "dz-99" || got.RefreshStatus != catalog.StatusStale {
		t.Errorf("after upsert got %+v", got)
	}
	if !got.LastRefreshedAt.IsZero() {
		t.Errorf("last_refreshed_at = %v, want zero (NULL)", got.LastRefreshedAt)
	}
}

func TestListAlbumsDueForRefresh(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	neverID := insertAlbum(t, db, artistID, "Never")
	freshID := insertAlbum(t, db, artistID, "Fresh")
	staleID := insertAlbum(t, db, artistID, "Stale")

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := s.UpsertAlbumMetadata(ctx, catalog.AlbumMetadata{
		AlbumID:         freshID,
		RefreshStatus:   catalog.StatusFresh,
		LastRefreshedAt: now,
		LastAccessedAt:  now.Add(-72 * time.Hour),
	}); err != nil {
		t.Fatalf("upsert fresh: %v", err)
	}
	if err := s.UpsertAlbumMetadata(ctx, catalog.AlbumMetadata{
		AlbumID:         staleID,
		RefreshStatus:   catalog.StatusStale,
		LastRefreshedAt: now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}

	// Never-materialized is always due; fresh (within the window) is not.
	got, err := s.ListAlbumsDueForRefresh(ctx, now.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	have := map[int64]bool{}
	for _, id := range got {
		have[id] = true
	}
	if !have[neverID] || !have[staleID] {
		t.Errorf("due = %v, want never=%d and stale=%d", got, neverID, staleID)
	}
	if have[freshID] {
		t.Errorf("fresh album %d should not be due", freshID)
	}

	// Touching a due album makes it the most-recently-accessed → first.
	if err := s.TouchAlbumAccessed(ctx, neverID, now); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, _ = s.ListAlbumsDueForRefresh(ctx, now.Add(-24*time.Hour), 10)
	if len(got) == 0 || got[0] != neverID {
		t.Errorf("order = %v, want %d first", got, neverID)
	}

	// Limit is honored.
	got, _ = s.ListAlbumsDueForRefresh(ctx, now.Add(-24*time.Hour), 1)
	if len(got) != 1 {
		t.Errorf("limited = %v, want 1 row", got)
	}

	// Non-positive limit yields an empty result, not an error.
	got, err = s.ListAlbumsDueForRefresh(ctx, now, 0)
	if err != nil || len(got) != 0 {
		t.Errorf("limit=0 got (%v, %v), want empty nil error", got, err)
	}
}

func TestTouchAlbumAccessedCreatesAndPreserves(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	albumID := insertAlbum(t, db, artistID, "Album")

	at := time.Date(2026, 5, 5, 5, 5, 5, 0, time.UTC)
	if err := s.TouchAlbumAccessed(ctx, albumID, at); err != nil {
		t.Fatalf("touch create: %v", err)
	}
	got, err := s.GetAlbumMetadata(ctx, albumID)
	if err != nil || got == nil {
		t.Fatalf("get after touch = (%v, %v), want row", got, err)
	}
	if !got.LastAccessedAt.Equal(at) || got.RefreshStatus != catalog.StatusPending {
		t.Errorf("after create-touch = %+v, want accessed=%v pending", got, at)
	}

	// Existing bookkeeping survives a later touch.
	if err := s.UpsertAlbumMetadata(ctx, catalog.AlbumMetadata{
		AlbumID:         albumID,
		ProviderName:    "spotify",
		ProviderAlbumID: "sp-1",
		RefreshStatus:   catalog.StatusFresh,
		LastRefreshedAt: at,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	later := at.Add(10 * time.Hour)
	if err := s.TouchAlbumAccessed(ctx, albumID, later); err != nil {
		t.Fatalf("touch update: %v", err)
	}
	got, _ = s.GetAlbumMetadata(ctx, albumID)
	if got.ProviderName != "spotify" || got.RefreshStatus != catalog.StatusFresh ||
		!got.LastRefreshedAt.Equal(at) {
		t.Errorf("touch clobbered bookkeeping: %+v", got)
	}
	if !got.LastAccessedAt.Equal(later) {
		t.Errorf("accessed = %v, want %v", got.LastAccessedAt, later)
	}
}

// ─── Artist metadata ─────────────────────────────────────────────────

// TestArtistMetadataGenresRoundTrip is table-driven over the JSON-encoded
// genres field.
func TestArtistMetadataGenresRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		genres []string
		wantN  int
	}{
		{name: "multiple", genres: []string{"Rock", "Pop"}, wantN: 2},
		{name: "single", genres: []string{"Jazz"}, wantN: 1},
		{name: "empty", genres: []string{}, wantN: 0},
		{name: "nil", genres: nil, wantN: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, db := newTestStore(t)
			ctx := context.Background()
			artistID := insertArtist(t, db, "Artist")

			refreshed := time.Date(2026, 2, 2, 2, 2, 2, 0, time.UTC)
			if err := s.UpsertArtistMetadata(ctx, catalog.ArtistMetadata{
				ArtistID:         artistID,
				ProviderName:     "deezer",
				ProviderArtistID: "dz-art-1",
				ImageURL:         "https://img/1.jpg",
				Genres:           tc.genres,
				Summary:          "bio",
				RefreshStatus:    catalog.StatusFresh,
				LastRefreshedAt:  refreshed,
			}); err != nil {
				t.Fatalf("upsert: %v", err)
			}

			got, err := s.GetArtistMetadata(ctx, artistID)
			if err != nil || got == nil {
				t.Fatalf("get = (%v, %v), want row", got, err)
			}
			if got.ProviderArtistID != "dz-art-1" || got.ImageURL != "https://img/1.jpg" ||
				got.Summary != "bio" || got.RefreshStatus != catalog.StatusFresh {
				t.Errorf("got = %+v", got)
			}
			if len(got.Genres) != tc.wantN {
				t.Errorf("genres = %v, want len %d", got.Genres, tc.wantN)
			}
			if !got.LastRefreshedAt.Equal(refreshed) {
				t.Errorf("refreshed = %v, want %v", got.LastRefreshedAt, refreshed)
			}
		})
	}
}

func TestArtistMetadataNotFoundAndUpsert(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	if m, err := s.GetArtistMetadata(ctx, 12345); err != nil || m != nil {
		t.Fatalf("missing get = (%v, %v), want (nil, nil)", m, err)
	}

	artistID := insertArtist(t, db, "Artist")
	if err := s.UpsertArtistMetadata(ctx, catalog.ArtistMetadata{
		ArtistID:     artistID,
		ProviderName: "spotify",
		Summary:      "first",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.UpsertArtistMetadata(ctx, catalog.ArtistMetadata{
		ArtistID:     artistID,
		ProviderName: "spotify",
		Summary:      "second",
	}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got, _ := s.GetArtistMetadata(ctx, artistID)
	if got.Summary != "second" {
		t.Errorf("summary = %q, want second (upsert)", got.Summary)
	}
}

func TestArtistMetadataCascadeOnArtistDelete(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	if err := s.UpsertArtistMetadata(ctx, catalog.ArtistMetadata{ArtistID: artistID}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM artists WHERE id=?`, artistID); err != nil {
		t.Fatalf("delete artist: %v", err)
	}
	if m, err := s.GetArtistMetadata(ctx, artistID); err != nil || m != nil {
		t.Errorf("after cascade = (%v, %v), want (nil, nil)", m, err)
	}
}

// ─── Artist discography ──────────────────────────────────────────────

// TestArtistDiscographyOrdering is table-driven over the (year, title) order.
func TestArtistDiscographyOrdering(t *testing.T) {
	tests := []struct {
		name string
		in   []catalog.ArtistDiscographyRelease
		want []string
	}{
		{
			name: "year then title",
			in: []catalog.ArtistDiscographyRelease{
				{Title: "Beta", Year: 2001},
				{Title: "Alpha", Year: 1999},
				{Title: "Alpha", Year: 2001},
			},
			want: []string{"1999|Alpha", "2001|Alpha", "2001|Beta"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, db := newTestStore(t)
			ctx := context.Background()
			artistID := insertArtist(t, db, "Artist")

			if err := s.ReplaceArtistDiscography(ctx, artistID, tc.in); err != nil {
				t.Fatalf("replace: %v", err)
			}
			got, err := s.GetArtistDiscography(ctx, artistID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			keys := make([]string, 0, len(got))
			for _, r := range got {
				keys = append(keys, strconv.Itoa(r.Year)+"|"+r.Title)
			}
			if !equalStrings(keys, tc.want) {
				t.Errorf("order = %v, want %v", keys, tc.want)
			}
		})
	}
}

func TestArtistDiscographyReplaceRoundTrip(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	seen := time.Date(2026, 3, 3, 3, 3, 3, 0, time.UTC)
	in := []catalog.ArtistDiscographyRelease{
		{ProviderName: "deezer", ProviderAlbumID: "dz-1", Title: "One",
			Year: 2020, Type: "album", CoverURL: "https://c/1.jpg",
			TrackCount: 12, LastSeenAt: seen},
		{ProviderName: "deezer", ProviderAlbumID: "dz-2", Title: "Two",
			Year: 2021, Type: "ep", TrackCount: 4, LastSeenAt: seen},
	}
	if err := s.ReplaceArtistDiscography(ctx, artistID, in); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := s.GetArtistDiscography(ctx, artistID)
	if err != nil || len(got) != 2 {
		t.Fatalf("get = (%v, %v), want 2", got, err)
	}
	if got[0].ProviderAlbumID != "dz-1" || got[0].TrackCount != 12 ||
		!got[0].LastSeenAt.Equal(seen) {
		t.Errorf("got[0] = %+v", got[0])
	}

	// Replace again with a single release — old rows are gone.
	if err := s.ReplaceArtistDiscography(ctx, artistID, []catalog.ArtistDiscographyRelease{
		{Title: "Only", Year: 2022},
	}); err != nil {
		t.Fatalf("replace 2: %v", err)
	}
	got, _ = s.GetArtistDiscography(ctx, artistID)
	if len(got) != 1 || got[0].Title != "Only" {
		t.Fatalf("after replace got %+v, want one 'Only'", got)
	}

	// Empty replace clears the artist (non-nil empty result).
	if err := s.ReplaceArtistDiscography(ctx, artistID, nil); err != nil {
		t.Fatalf("replace empty: %v", err)
	}
	got, _ = s.GetArtistDiscography(ctx, artistID)
	if got == nil || len(got) != 0 {
		t.Errorf("after empty replace = %v, want empty non-nil slice", got)
	}
}

func TestArtistDiscographyCascadeOnArtistDelete(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	artistID := insertArtist(t, db, "Artist")
	if err := s.ReplaceArtistDiscography(ctx, artistID, []catalog.ArtistDiscographyRelease{
		{Title: "One", Year: 2020},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM artists WHERE id=?`, artistID); err != nil {
		t.Fatalf("delete artist: %v", err)
	}
	got, err := s.GetArtistDiscography(ctx, artistID)
	if err != nil || len(got) != 0 {
		t.Errorf("after cascade = (%v, %v), want empty", got, err)
	}
}
