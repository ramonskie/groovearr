package sqlite

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStore_GetArtistByNameCaseInsensitive(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	id, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Acda en De Munnik"})
	if err != nil {
		t.Fatal(err)
	}

	// The same name in different case resolves to the existing artist, so an
	// import of "Acda en de Munnik" reuses it instead of creating a duplicate.
	got, err := store.GetArtistByName(ctx, "Acda en de Munnik")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("case-insensitive lookup got %+v, want id %d", got, id)
	}
}

func TestStore_MergeArtists(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	keepID, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Acda en de Munnik", ThumbURL: "artist.jpg", ExternalIDs: map[string]string{"musicbrainz": "mb-keep"}})
	if err != nil {
		t.Fatal(err)
	}
	removeID, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Acda en De Munnik", ExternalIDs: map[string]string{"deezer": "dz-remove"}})
	if err != nil {
		t.Fatal(err)
	}

	// Two albums under the duplicate artists (same title is allowed — no
	// unique constraint), one track each.
	keepAlbum, err := store.UpsertAlbum(ctx, &domain.Album{ArtistID: keepID, Title: "Hier Zijn", Year: 2004, TrackCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	removeAlbum, err := store.UpsertAlbum(ctx, &domain.Album{ArtistID: removeID, Title: "Hier Zijn", Year: 2004, TrackCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpsertTrack(ctx, &domain.Track{AlbumID: removeAlbum, ArtistID: removeID, Title: "De Kapitein Deel II", FilePath: "/music/x.flac"})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.MergeArtists(ctx, keepID, removeID); err != nil {
		t.Fatal(err)
	}

	// The removed artist is gone.
	if removed, _ := store.GetArtist(ctx, removeID); removed != nil {
		t.Fatal("removed artist still present")
	}
	// Its album now belongs to the surviving artist.
	albums, err := store.GetAlbumsByArtist(ctx, keepID)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 2 {
		t.Fatalf("expected 2 albums under kept artist, got %d", len(albums))
	}
	// Its track moved with it.
	tracks, err := store.GetTracksByAlbum(ctx, removeAlbum)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].ArtistID != keepID {
		t.Fatalf("track not reassigned: %+v", tracks)
	}
	// External ids merged (remove's keys added to keep's).
	keep, err := store.GetArtist(ctx, keepID)
	if err != nil {
		t.Fatal(err)
	}
	if keep.ExternalIDs["musicbrainz"] != "mb-keep" || keep.ExternalIDs["deezer"] != "dz-remove" {
		t.Errorf("external_ids not merged: %v", keep.ExternalIDs)
	}
	if keep.ThumbURL != "artist.jpg" {
		t.Errorf("thumb should stay on the surviving artist, got %q", keep.ThumbURL)
	}
	_ = keepAlbum

	// Merging an artist into itself is rejected.
	if err := store.MergeArtists(ctx, keepID, keepID); err == nil {
		t.Error("expected error when merging an artist into itself")
	}
}

func TestStore_ArtistCRUD(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create.
	id, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Test Artist", Genres: []string{"rock", "pop"}})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero artist ID")
	}

	// Read.
	a, err := store.GetArtist(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Test Artist" {
		t.Errorf("name = %q, want Test Artist", a.Name)
	}
	if len(a.Genres) != 2 || a.Genres[0] != "rock" {
		t.Errorf("genres = %v, want [rock pop]", a.Genres)
	}

	// GetByName.
	a2, err := store.GetArtistByName(ctx, "Test Artist")
	if err != nil {
		t.Fatal(err)
	}
	if a2.ID != id {
		t.Errorf("GetArtistByName ID = %d, want %d", a2.ID, id)
	}

	// Non-existent returns nil, not error.
	a3, err := store.GetArtistByName(ctx, "Nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if a3 != nil {
		t.Error("expected nil for nonexistent artist")
	}

	// Search.
	artists, err := store.SearchArtists(ctx, "Test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 1 {
		t.Errorf("search returned %d artists, want 1", len(artists))
	}

	// List.
	all, err := store.ListArtists(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("list returned %d artists, want 1", len(all))
	}
}

func TestStore_AlbumCRUD(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create artist first.
	artistID, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Album Artist"})
	if err != nil {
		t.Fatal(err)
	}

	// Create album.
	id, err := store.UpsertAlbum(ctx, &domain.Album{
		ArtistID:   artistID,
		Title:      "Test Album",
		Year:       2024,
		TrackCount: 10,
		AlbumType:  domain.AlbumTypeAlbum,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read.
	a, err := store.GetAlbum(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Test Album" {
		t.Errorf("title = %q", a.Title)
	}
	if a.Year != 2024 {
		t.Errorf("year = %d", a.Year)
	}

	// GetByArtist.
	albums, err := store.GetAlbumsByArtist(ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 1 {
		t.Errorf("albums by artist = %d, want 1", len(albums))
	}
}

func TestStore_TrackCRUD(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	artistID, _ := store.UpsertArtist(ctx, &domain.Artist{Name: "Track Artist"})
	albumID, _ := store.UpsertAlbum(ctx, &domain.Album{ArtistID: artistID, Title: "Track Album"})

	// Create track.
	id, err := store.UpsertTrack(ctx, &domain.Track{
		AlbumID:     albumID,
		ArtistID:    artistID,
		Title:       "Test Track",
		TrackNumber: 3,
		Duration:    210000,
		FilePath:    "/music/Artist/Album/03 - Test Track.flac",
		Bitrate:     1411,
		FileSize:    25000000,
		ISRC:        "US-ABC-12-34567",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read.
	track, err := store.GetTrack(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if track.Title != "Test Track" {
		t.Errorf("title = %q", track.Title)
	}
	if track.TrackNumber != 3 {
		t.Errorf("track_number = %d", track.TrackNumber)
	}
	if track.ISRC != "US-ABC-12-34567" {
		t.Errorf("isrc = %q", track.ISRC)
	}

	// GetByFilePath.
	t2, err := store.GetTrackByFilePath(ctx, "/music/Artist/Album/03 - Test Track.flac")
	if err != nil {
		t.Fatal(err)
	}
	if t2 == nil {
		t.Fatal("GetTrackByFilePath returned nil")
	}
	if t2.ID != id {
		t.Errorf("GetTrackByFilePath ID = %d, want %d", t2.ID, id)
	}

	// GetByAlbum.
	tracks, err := store.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Errorf("tracks by album = %d, want 1", len(tracks))
	}

	// Delete.
	if err := store.DeleteTrack(ctx, id); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.GetTrack(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Error("expected nil after delete")
	}
}

func TestStore_ExternalIDLookups(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	_, err = store.UpsertArtist(ctx, &domain.Artist{Name: "Ext Artist", ExternalIDs: map[string]string{"spotify": "spotify:123"}})
	if err != nil {
		t.Fatal(err)
	}

	a, err := store.GetArtistByExternalID(ctx, "spotify", "spotify:123")
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatal("expected artist by Spotify ID")
	}
	if a.Name != "Ext Artist" {
		t.Errorf("name = %q", a.Name)
	}

	// Unknown service now accepted (no whitelist) — returns nil for not found.
	a2, err := store.GetArtistByExternalID(ctx, "unknown", "id")
	if err != nil {
		t.Errorf("unknown service should not error: %v", err)
	}
	if a2 != nil {
		t.Error("expected nil for unknown service with no match")
	}
}

func TestStore_DuplicateUpsert(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// First insert.
	id1, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Duplicate"})
	if err != nil {
		t.Fatal(err)
	}

	// Second insert with same name returns the existing ID (unique constraint).
	id2, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Duplicate"})
	if err != nil {
		t.Fatal(err)
	}

	// Both calls return the same ID — no duplicates created.
	if id1 != id2 {
		t.Errorf("expected same ID for duplicate artist, got %d and %d", id1, id2)
	}

	all, _ := store.ListArtists(ctx, 0, 10)
	if len(all) != 1 {
		t.Errorf("expected 1 artist, got %d", len(all))
	}
}

func TestStore_SetArtistThumbURL(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	id, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Artist Without Image"})
	if err != nil {
		t.Fatal(err)
	}

	// ThumbURL should be empty initially.
	a, _ := store.GetArtist(ctx, id)
	if a.ThumbURL != "" {
		t.Errorf("expected empty thumb_url, got %q", a.ThumbURL)
	}

	// Set thumb_url.
	if err := store.SetArtistThumbURL(ctx, id, "https://example.com/artist.jpg"); err != nil {
		t.Fatal(err)
	}

	// Verify it persisted.
	a, err = store.GetArtist(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.ThumbURL != "https://example.com/artist.jpg" {
		t.Errorf("thumb_url = %q, want https://example.com/artist.jpg", a.ThumbURL)
	}

	// Verify other fields untouched.
	if a.Name != "Artist Without Image" {
		t.Errorf("name = %q, want Artist Without Image", a.Name)
	}
}

func TestStore_FirstAlbumID(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Artist without albums.
	artistID, _ := store.UpsertArtist(ctx, &domain.Artist{Name: "No Albums"})
	a, _ := store.GetArtist(ctx, artistID)
	if a.FirstAlbumID != 0 {
		t.Errorf("expected first_album_id=0 for artist without albums, got %d", a.FirstAlbumID)
	}

	// Artist with albums.
	artistID2, _ := store.UpsertArtist(ctx, &domain.Artist{Name: "Has Albums"})
	albumID, _ := store.UpsertAlbum(ctx, &domain.Album{ArtistID: artistID2, Title: "First Album", Year: 2020})
	store.UpsertAlbum(ctx, &domain.Album{ArtistID: artistID2, Title: "Second Album", Year: 2021})
	_ = albumID

	a, _ = store.GetArtist(ctx, artistID2)
	if a.FirstAlbumID == 0 {
		t.Error("expected non-zero first_album_id for artist with albums")
	}

	// ListArtists should also include first_album_id.
	all, _ := store.ListArtists(ctx, 0, 10)
	for _, artist := range all {
		if artist.Name == "Has Albums" && artist.FirstAlbumID == 0 {
			t.Error("ListArtists: expected non-zero first_album_id")
		}
		if artist.Name == "No Albums" && artist.FirstAlbumID != 0 {
			t.Error("ListArtists: expected first_album_id=0 for artist without albums")
		}
	}
}

func TestStore_DuplicateScanCRUD(t *testing.T) {
	store, err := New(t.TempDir()+"/test.db", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// Nothing scanned yet.
	if _, found, err := store.GetDuplicateCanonical(ctx, "danny de munk"); err != nil || found {
		t.Fatalf("expected not-found for empty scan (found=%v, err=%v)", found, err)
	}

	// Upsert two groups.
	if err := store.UpsertDuplicateCanonical(ctx, "danny de munk", "Danny de Munk"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDuplicateCanonical(ctx, "acda en de munnik", "Acda en de Munnik"); err != nil {
		t.Fatal(err)
	}
	// Upsert overwrites.
	if err := store.UpsertDuplicateCanonical(ctx, "danny de munk", "Danny De Munk"); err != nil {
		t.Fatal(err)
	}

	canon, found, err := store.GetDuplicateCanonical(ctx, "danny de munk")
	if err != nil || !found || canon != "Danny De Munk" {
		t.Fatalf("GetDuplicateCanonical = (%q, %v, %v), want overwritten value", canon, found, err)
	}

	all, err := store.ListDuplicateCanonicals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["acda en de munnik"] != "Acda en de Munnik" {
		t.Fatalf("ListDuplicateCanonicals = %v, want 2 groups", all)
	}

	// Delete a single group.
	if err := store.DeleteDuplicateCanonical(ctx, "danny de munk"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.GetDuplicateCanonical(ctx, "danny de munk"); found {
		t.Error("group should be deleted after DeleteDuplicateCanonical")
	}

	// Clear all.
	if err := store.ClearDuplicateCanonicals(ctx); err != nil {
		t.Fatal(err)
	}
	all, _ = store.ListDuplicateCanonicals(ctx)
	if len(all) != 0 {
		t.Errorf("expected empty scan after clear, got %d groups", len(all))
	}
}

func TestStore_CountPlaylistsByName(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Two tidal playlists share the name "My Mix", one has a unique name.
	upserts := []*domain.Playlist{
		{Source: "tidal", SourcePlaylistID: "aaaa-bbbb", Name: "My Mix"},
		{Source: "tidal", SourcePlaylistID: "1111-2222", Name: "My Mix"},
		{Source: "tidal", SourcePlaylistID: "9999-8888", Name: "Solo Mix"},
		{Source: "deezer", SourcePlaylistID: "dz-1", Name: "My Mix"},
	}
	for _, p := range upserts {
		if _, err := store.UpsertPlaylist(ctx, p); err != nil {
			t.Fatalf("upsert playlist %+v: %v", p, err)
		}
	}

	tests := []struct {
		source, name string
		want         int64
	}{
		{"tidal", "My Mix", 2},   // same-name collision
		{"tidal", "Solo Mix", 1}, // unique name
		{"deezer", "My Mix", 1},  // same name, different source
		{"tidal", "Does Not Exist", 0},
	}
	for _, tc := range tests {
		got, err := store.CountPlaylistsByName(ctx, tc.source, tc.name)
		if err != nil {
			t.Fatalf("CountPlaylistsByName(%q, %q): %v", tc.source, tc.name, err)
		}
		if got != tc.want {
			t.Errorf("CountPlaylistsByName(%q, %q) = %d, want %d", tc.source, tc.name, got, tc.want)
		}
	}
}

// ─── Phase 9 attribution migration ───────────────────────────────────
//
// These tests pin the additive schema contract: New() must produce the same
// attribution columns (and indexes) on a fresh database and on a database
// created before the columns existed.

type columnExpect struct {
	notNull      bool
	defaultValid bool
	defaultValue string
}

type columnInfo struct {
	notNull      bool
	defaultValid bool
	defaultValue string
	primaryKey   bool
}

// attributionColumns is the Phase 9 schema contract every database must
// satisfy after New(), whether created fresh or upgraded additively.
var attributionColumns = map[string]map[string]columnExpect{
	"downloads": {
		"requested_by_user_id":  {notNull: true, defaultValid: true, defaultValue: "0"},
		"requested_by_username": {notNull: true, defaultValid: true, defaultValue: "''"},
	},
	"tracks": {
		"added_by_user_id":  {notNull: false},
		"added_by_username": {notNull: true, defaultValid: true, defaultValue: "''"},
	},
	"albums": {
		"added_by_user_id":  {notNull: false},
		"added_by_username": {notNull: true, defaultValid: true, defaultValue: "''"},
	},
	"playlists": {
		"added_by_user_id":  {notNull: false},
		"added_by_username": {notNull: true, defaultValid: true, defaultValue: "''"},
	},
}

var attributionIndexes = map[string]string{
	"idx_downloads_requested_by_user_id": "downloads",
	"idx_tracks_added_by_user_id":        "tracks",
	"idx_albums_added_by_user_id":        "albums",
	"idx_playlists_added_by_user_id":     "playlists",
}

func readColumns(t *testing.T, db *sql.DB, table string) map[string]columnInfo {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	cols := make(map[string]columnInfo)
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		cols[name] = columnInfo{
			notNull:      notNull == 1,
			defaultValid: dflt.Valid,
			defaultValue: dflt.String,
			primaryKey:   pk == 1,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s) rows: %v", table, err)
	}
	return cols
}

func assertAttributionColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	for table, wants := range attributionColumns {
		cols := readColumns(t, db, table)
		for name, want := range wants {
			got, ok := cols[name]
			if !ok {
				t.Errorf("%s.%s missing after migration", table, name)
				continue
			}
			if got.notNull != want.notNull {
				t.Errorf("%s.%s notNull = %v, want %v", table, name, got.notNull, want.notNull)
			}
			if got.defaultValid != want.defaultValid ||
				(want.defaultValid && got.defaultValue != want.defaultValue) {
				t.Errorf("%s.%s default = (%v, %q), want (%v, %q)",
					table, name, got.defaultValid, got.defaultValue, want.defaultValid, want.defaultValue)
			}
		}
	}
	for index, table := range attributionIndexes {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name=?`,
			index, table,
		).Scan(&count); err != nil {
			t.Fatalf("lookup index %s: %v", index, err)
		}
		if count != 1 {
			t.Errorf("index %s on %s missing", index, table)
		}
	}
}

// legacySchema is the pre-Phase-9 (but post earlier migrations) shape of the
// four attributed tables, used to prove the additive migration upgrades an
// existing database without error.
var legacySchema = []string{
	`CREATE TABLE IF NOT EXISTS albums (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		artist_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		year INTEGER,
		genres TEXT,
		track_count INTEGER,
		duration INTEGER,
		thumb_url TEXT,
		album_type TEXT DEFAULT 'album',
		release_date TEXT,
		external_ids TEXT DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE TABLE IF NOT EXISTS tracks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		album_id INTEGER NOT NULL,
		artist_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		track_number INTEGER,
		disc_number INTEGER DEFAULT 1,
		duration INTEGER,
		file_path TEXT,
		bitrate INTEGER,
		file_size INTEGER,
		external_ids TEXT DEFAULT '{}',
		acoustid TEXT,
		isrc TEXT,
		quality_profile_id INTEGER,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE TABLE IF NOT EXISTS playlists (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		source             TEXT NOT NULL,
		source_playlist_id TEXT NOT NULL,
		name               TEXT NOT NULL,
		description        TEXT,
		track_count        INTEGER,
		cover_url          TEXT,
		owner_name         TEXT,
		is_public          INTEGER DEFAULT 1,
		auto_sync          INTEGER DEFAULT 0,
		sync_mode          TEXT DEFAULT 'mirror',
		synced_at          TEXT,
		created_at         TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at         TEXT NOT NULL DEFAULT (datetime('now')),
		UNIQUE(source, source_playlist_id)
	)`,
	`CREATE TABLE IF NOT EXISTS downloads (
		id TEXT PRIMARY KEY,
		source_name TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		filename TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'initializing',
		progress REAL NOT NULL DEFAULT 0,
		size INTEGER NOT NULL DEFAULT 0,
		transferred INTEGER NOT NULL DEFAULT 0,
		speed INTEGER NOT NULL DEFAULT 0,
		file_path TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		track_id TEXT NOT NULL DEFAULT '',
		cover_url TEXT NOT NULL DEFAULT '',
		artist TEXT NOT NULL DEFAULT '',
		album TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		track_number INTEGER NOT NULL DEFAULT 0,
		disc_number INTEGER NOT NULL DEFAULT 0,
		year INTEGER NOT NULL DEFAULT 0,
		retry_count INTEGER NOT NULL DEFAULT 0,
		retry_after TEXT NOT NULL DEFAULT '',
		bitrate INTEGER NOT NULL DEFAULT 0,
		format TEXT NOT NULL DEFAULT '',
		playlist_id TEXT NOT NULL DEFAULT '',
		quality_profile_id INTEGER,
		isrc TEXT NOT NULL DEFAULT '',
		library_track_id INTEGER NOT NULL DEFAULT 0,
		album_type TEXT NOT NULL DEFAULT '',
		album_tracks TEXT NOT NULL DEFAULT '',
		download_client TEXT NOT NULL DEFAULT '',
		provider_id TEXT NOT NULL DEFAULT '',
		magnet_uri TEXT NOT NULL DEFAULT '',
		folder_path TEXT NOT NULL DEFAULT '',
		imported_track_ids TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	// Seed one row per attributed table to prove existing rows survive and
	// keep NULL/0 attribution after the migration.
	`INSERT INTO albums (artist_id, title) VALUES (1, 'Legacy Album')`,
	`INSERT INTO tracks (album_id, artist_id, title) VALUES (1, 1, 'Legacy Track')`,
	`INSERT INTO playlists (source, source_playlist_id, name) VALUES ('legacy', 'legacy-1', 'Legacy Playlist')`,
	`INSERT INTO downloads (id) VALUES ('legacy-dl')`,
}

func createLegacyDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()

	for _, stmt := range legacySchema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("legacy exec failed: %v\nstmt: %s", err, stmt)
		}
	}
}

func TestStore_AttributionColumns_FreshDB(t *testing.T) {
	store, err := New(t.TempDir()+"/fresh.db", testLogger())
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}
	defer store.Close()

	assertAttributionColumns(t, store.DB())
}

func TestStore_AttributionColumns_PreExistingDB(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	createLegacyDB(t, path)

	// Opening the pre-Phase-9 database must apply the additive migration
	// without error.
	store, err := New(path, testLogger())
	if err != nil {
		t.Fatalf("open and migrate legacy db: %v", err)
	}
	defer store.Close()

	assertAttributionColumns(t, store.DB())

	// Existing rows must survive with system/unknown attribution defaults.
	var requestedBy int64
	if err := store.DB().QueryRow(`SELECT requested_by_user_id FROM downloads WHERE id='legacy-dl'`).Scan(&requestedBy); err != nil {
		t.Fatalf("legacy download row: %v", err)
	}
	if requestedBy != 0 {
		t.Errorf("legacy download requested_by_user_id = %d, want 0", requestedBy)
	}

	var (
		addedByID   sql.NullInt64
		addedByName string
	)
	if err := store.DB().QueryRow(`SELECT added_by_user_id, added_by_username FROM tracks WHERE title='Legacy Track'`).Scan(&addedByID, &addedByName); err != nil {
		t.Fatalf("legacy track row: %v", err)
	}
	if addedByID.Valid {
		t.Errorf("legacy track added_by_user_id = %d, want NULL", addedByID.Int64)
	}
	if addedByName != "" {
		t.Errorf("legacy track added_by_username = %q, want empty", addedByName)
	}
}

func TestStore_AttributionColumns_FreshAndMigratedSchemasMatch(t *testing.T) {
	fresh, err := New(t.TempDir()+"/fresh.db", testLogger())
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}
	defer fresh.Close()

	legacyPath := t.TempDir() + "/legacy.db"
	createLegacyDB(t, legacyPath)
	migrated, err := New(legacyPath, testLogger())
	if err != nil {
		t.Fatalf("open and migrate legacy db: %v", err)
	}
	defer migrated.Close()

	for _, table := range []string{"downloads", "tracks", "albums", "playlists"} {
		freshCols := readColumns(t, fresh.DB(), table)
		migratedCols := readColumns(t, migrated.DB(), table)
		if !reflect.DeepEqual(freshCols, migratedCols) {
			t.Errorf("%s schema mismatch between fresh and migrated paths:\n fresh    = %+v\n migrated = %+v",
				table, freshCols, migratedCols)
		}
	}
}

// readTrackAttribution returns the raw added_by_* values for a track id.
func readTrackAttribution(t *testing.T, db *sql.DB, id int64) (sql.NullInt64, string) {
	t.Helper()
	var uid sql.NullInt64
	var name string
	if err := db.QueryRow(`SELECT added_by_user_id, added_by_username FROM tracks WHERE id=?`, id).
		Scan(&uid, &name); err != nil {
		t.Fatalf("read track attribution: %v", err)
	}
	return uid, name
}

// readAlbumAttribution returns the raw added_by_* values for an album id.
func readAlbumAttribution(t *testing.T, db *sql.DB, id int64) (sql.NullInt64, string) {
	t.Helper()
	var uid sql.NullInt64
	var name string
	if err := db.QueryRow(`SELECT added_by_user_id, added_by_username FROM albums WHERE id=?`, id).
		Scan(&uid, &name); err != nil {
		t.Fatalf("read album attribution: %v", err)
	}
	return uid, name
}

// readPlaylistAttribution returns the raw added_by_* values for a playlist id.
func readPlaylistAttribution(t *testing.T, db *sql.DB, id int64) (sql.NullInt64, string) {
	t.Helper()
	var uid sql.NullInt64
	var name string
	if err := db.QueryRow(`SELECT added_by_user_id, added_by_username FROM playlists WHERE id=?`, id).
		Scan(&uid, &name); err != nil {
		t.Fatalf("read playlist attribution: %v", err)
	}
	return uid, name
}

// TestStore_ImportTrackStampsAttribution verifies a download.requester-carrying
// track is stamped on both the new track row and the album created for it.
func TestStore_ImportTrackStampsAttribution(t *testing.T) {
	store, err := New(t.TempDir()+"/stamp.db", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	track := &domain.Track{
		Title:           "Song",
		FilePath:        "/music/song.flac",
		AddedByUserID:   42,
		AddedByUsername: "alice",
	}
	trackID, err := store.ImportTrack(ctx, track, "Artist", "Album", 2024, nil)
	if err != nil {
		t.Fatalf("ImportTrack: %v", err)
	}

	uid, name := readTrackAttribution(t, store.DB(), trackID)
	if !uid.Valid || uid.Int64 != 42 || name != "alice" {
		t.Errorf("track attribution = (%v, %q), want (42, alice)", uid, name)
	}

	uid, name = readAlbumAttribution(t, store.DB(), track.AlbumID)
	if !uid.Valid || uid.Int64 != 42 || name != "alice" {
		t.Errorf("album attribution = (%v, %q), want (42, alice)", uid, name)
	}
}

// TestStore_ImportTrackSystemStoresNullBlank verifies the scanner/system path
// (zero requester) leaves NULL id and blank username rather than a 0 user id.
func TestStore_ImportTrackSystemStoresNullBlank(t *testing.T) {
	store, err := New(t.TempDir()+"/system.db", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	track := &domain.Track{Title: "Scanned", FilePath: "/music/scanned.flac"}
	trackID, err := store.ImportTrack(ctx, track, "Artist", "Album", 2024, nil)
	if err != nil {
		t.Fatalf("ImportTrack: %v", err)
	}

	uid, name := readTrackAttribution(t, store.DB(), trackID)
	if uid.Valid || name != "" {
		t.Errorf("system track attribution = (%v, %q), want (NULL, \"\")", uid, name)
	}

	uid, name = readAlbumAttribution(t, store.DB(), track.AlbumID)
	if uid.Valid || name != "" {
		t.Errorf("system album attribution = (%v, %q), want (NULL, \"\")", uid, name)
	}
}

// TestStore_UpsertTrackAttributionInsertOnly proves a second upsert (the
// UPDATE branch) never clobbers attribution written on insert.
func TestStore_UpsertTrackAttributionInsertOnly(t *testing.T) {
	store, err := New(t.TempDir()+"/track-upsert.db", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	artistID, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Artist"})
	if err != nil {
		t.Fatalf("UpsertArtist: %v", err)
	}
	albumID, err := store.UpsertAlbum(ctx, &domain.Album{ArtistID: artistID, Title: "Album"})
	if err != nil {
		t.Fatalf("UpsertAlbum: %v", err)
	}

	trackID, err := store.UpsertTrack(ctx, &domain.Track{
		AlbumID: albumID, ArtistID: artistID, Title: "Song",
		AddedByUserID: 7, AddedByUsername: "alice",
	})
	if err != nil {
		t.Fatalf("UpsertTrack insert: %v", err)
	}

	// Update branch: same id, different (would-be) attribution.
	if _, err := store.UpsertTrack(ctx, &domain.Track{
		ID: trackID, AlbumID: albumID, ArtistID: artistID, Title: "Song Renamed",
		AddedByUserID: 99, AddedByUsername: "bob",
	}); err != nil {
		t.Fatalf("UpsertTrack update: %v", err)
	}

	uid, name := readTrackAttribution(t, store.DB(), trackID)
	if !uid.Valid || uid.Int64 != 7 || name != "alice" {
		t.Errorf("attribution after update = (%v, %q), want (7, alice) insert-only", uid, name)
	}
}

// TestStore_UpsertAlbumAttributionInsertOnly proves a second upsert (the
// UPDATE branch) never clobbers album attribution written on insert.
func TestStore_UpsertAlbumAttributionInsertOnly(t *testing.T) {
	store, err := New(t.TempDir()+"/album-upsert.db", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	artistID, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Artist"})
	if err != nil {
		t.Fatalf("UpsertArtist: %v", err)
	}

	albumID, err := store.UpsertAlbum(ctx, &domain.Album{
		ArtistID: artistID, Title: "Album", AddedByUserID: 5, AddedByUsername: "carol",
	})
	if err != nil {
		t.Fatalf("UpsertAlbum insert: %v", err)
	}

	if _, err := store.UpsertAlbum(ctx, &domain.Album{
		ID: albumID, ArtistID: artistID, Title: "Album Renamed",
		AddedByUserID: 88, AddedByUsername: "dave",
	}); err != nil {
		t.Fatalf("UpsertAlbum update: %v", err)
	}

	uid, name := readAlbumAttribution(t, store.DB(), albumID)
	if !uid.Valid || uid.Int64 != 5 || name != "carol" {
		t.Errorf("attribution after update = (%v, %q), want (5, carol) insert-only", uid, name)
	}
}

// TestStore_UpsertPlaylistAttributionInsertOnly proves playlist attribution is
// stamped on INSERT only: the UPDATE branch (re-import / sync) must never
// clobber the original importer.
func TestStore_UpsertPlaylistAttributionInsertOnly(t *testing.T) {
	store, err := New(t.TempDir()+"/playlist-upsert.db", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	playlistID, err := store.UpsertPlaylist(ctx, &domain.Playlist{
		Source: "deezer", SourcePlaylistID: "pl-1", Name: "Mix",
		AddedByUserID: 11, AddedByUsername: "erin",
	})
	if err != nil {
		t.Fatalf("UpsertPlaylist insert: %v", err)
	}

	uid, name := readPlaylistAttribution(t, store.DB(), playlistID)
	if !uid.Valid || uid.Int64 != 11 || name != "erin" {
		t.Fatalf("insert attribution = (%v, %q), want (11, erin)", uid, name)
	}

	// Update branch: same id, different (would-be) attribution + a synced_at bump.
	if _, err := store.UpsertPlaylist(ctx, &domain.Playlist{
		ID: playlistID, Source: "deezer", SourcePlaylistID: "pl-1", Name: "Mix Renamed",
		SyncedAt:      "2026-10-07T00:00:00Z",
		AddedByUserID: 99, AddedByUsername: "bob",
	}); err != nil {
		t.Fatalf("UpsertPlaylist update: %v", err)
	}

	uid, name = readPlaylistAttribution(t, store.DB(), playlistID)
	if !uid.Valid || uid.Int64 != 11 || name != "erin" {
		t.Errorf("attribution after update = (%v, %q), want (11, erin) insert-only", uid, name)
	}
}

// TestStore_UpsertPlaylistSystemStoresNullBlank verifies a system-created
// playlist (zero actor) stores NULL/blank rather than a 0 user id.
func TestStore_UpsertPlaylistSystemStoresNullBlank(t *testing.T) {
	store, err := New(t.TempDir()+"/playlist-system.db", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	playlistID, err := store.UpsertPlaylist(ctx, &domain.Playlist{
		Source: "deezer", SourcePlaylistID: "sys-1", Name: "Auto",
	})
	if err != nil {
		t.Fatalf("UpsertPlaylist: %v", err)
	}

	uid, name := readPlaylistAttribution(t, store.DB(), playlistID)
	if uid.Valid || name != "" {
		t.Errorf("system playlist attribution = (%v, %q), want (NULL, \"\")", uid, name)
	}
}

// findTrack returns the track with the given id from a slice.
func findTrack(tracks []domain.Track, id int64) (domain.Track, bool) {
	for _, tr := range tracks {
		if tr.ID == id {
			return tr, true
		}
	}
	return domain.Track{}, false
}

// findAlbum returns the album with the given id from a slice.
func findAlbum(albums []domain.Album, id int64) (domain.Album, bool) {
	for _, al := range albums {
		if al.ID == id {
			return al, true
		}
	}
	return domain.Album{}, false
}

// findPlaylist returns the playlist with the given id from a slice.
func findPlaylist(playlists []domain.Playlist, id int64) (domain.Playlist, bool) {
	for _, p := range playlists {
		if p.ID == id {
			return p, true
		}
	}
	return domain.Playlist{}, false
}

// TestStore_ReadPathsExposeAttribution pins the Phase 9.5 read contract: every
// read path that feeds the API selects added_by_user_id/added_by_username and
// scans them onto the domain struct. A user-stamped row round-trips; a
// system/scanned row reads back as zero/blank. No authorization is derived.
func TestStore_ReadPathsExposeAttribution(t *testing.T) {
	store, err := New(t.TempDir()+"/read-attribution.db", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// User-requested track + album (attribution stamped on import).
	stamped := &domain.Track{
		Title: "Stamped", FilePath: "/music/stamped.flac", ISRC: "ISRC-STAMP",
		AddedByUserID: 42, AddedByUsername: "alice",
	}
	stampedID, err := store.ImportTrack(ctx, stamped, "Artist", "Stamped Album", 2024, nil)
	if err != nil {
		t.Fatalf("ImportTrack stamped: %v", err)
	}

	// System/scanned track + album (no requester).
	system := &domain.Track{Title: "System", FilePath: "/music/system.flac"}
	systemID, err := store.ImportTrack(ctx, system, "Artist", "System Album", 2024, nil)
	if err != nil {
		t.Fatalf("ImportTrack system: %v", err)
	}

	// ── Track reads ──────────────────────────────────────────────────
	if tr, err := store.GetTrack(ctx, stampedID); err != nil {
		t.Fatalf("GetTrack: %v", err)
	} else if tr.AddedByUserID != 42 || tr.AddedByUsername != "alice" {
		t.Errorf("GetTrack attribution = (%d, %q), want (42, alice)", tr.AddedByUserID, tr.AddedByUsername)
	}

	if tracks, err := store.GetTracksByAlbum(ctx, stamped.AlbumID); err != nil {
		t.Fatalf("GetTracksByAlbum: %v", err)
	} else if tr, ok := findTrack(tracks, stampedID); !ok {
		t.Errorf("GetTracksByAlbum missing stamped track %d", stampedID)
	} else if tr.AddedByUserID != 42 || tr.AddedByUsername != "alice" {
		t.Errorf("GetTracksByAlbum attribution = (%d, %q), want (42, alice)", tr.AddedByUserID, tr.AddedByUsername)
	}

	if tracks, err := store.GetTracksByArtist(ctx, stamped.ArtistID); err != nil {
		t.Fatalf("GetTracksByArtist: %v", err)
	} else if tr, ok := findTrack(tracks, stampedID); !ok {
		t.Errorf("GetTracksByArtist missing stamped track %d", stampedID)
	} else if tr.AddedByUserID != 42 || tr.AddedByUsername != "alice" {
		t.Errorf("GetTracksByArtist attribution = (%d, %q), want (42, alice)", tr.AddedByUserID, tr.AddedByUsername)
	}

	if tracks, err := store.SearchTracks(ctx, "Stamped", 10); err != nil {
		t.Fatalf("SearchTracks: %v", err)
	} else if tr, ok := findTrack(tracks, stampedID); !ok {
		t.Errorf("SearchTracks missing stamped track %d", stampedID)
	} else if tr.AddedByUserID != 42 || tr.AddedByUsername != "alice" {
		t.Errorf("SearchTracks attribution = (%d, %q), want (42, alice)", tr.AddedByUserID, tr.AddedByUsername)
	}

	if tr, err := store.GetTrackByFilePath(ctx, "/music/stamped.flac"); err != nil {
		t.Fatalf("GetTrackByFilePath: %v", err)
	} else if tr == nil || tr.AddedByUserID != 42 || tr.AddedByUsername != "alice" {
		t.Errorf("GetTrackByFilePath attribution = %+v, want (42, alice)", tr)
	}

	if tracks, err := store.ListTracksWithQuality(ctx); err != nil {
		t.Fatalf("ListTracksWithQuality: %v", err)
	} else if tr, ok := findTrack(tracks, stampedID); !ok {
		t.Errorf("ListTracksWithQuality missing stamped track %d", stampedID)
	} else if tr.AddedByUserID != 42 || tr.AddedByUsername != "alice" {
		t.Errorf("ListTracksWithQuality attribution = (%d, %q), want (42, alice)", tr.AddedByUserID, tr.AddedByUsername)
	}

	// ── Album reads ──────────────────────────────────────────────────
	if al, err := store.GetAlbum(ctx, stamped.AlbumID); err != nil {
		t.Fatalf("GetAlbum: %v", err)
	} else if al.AddedByUserID != 42 || al.AddedByUsername != "alice" {
		t.Errorf("GetAlbum attribution = (%d, %q), want (42, alice)", al.AddedByUserID, al.AddedByUsername)
	}

	if albums, err := store.GetAlbumsByArtist(ctx, stamped.ArtistID); err != nil {
		t.Fatalf("GetAlbumsByArtist: %v", err)
	} else if al, ok := findAlbum(albums, stamped.AlbumID); !ok {
		t.Errorf("GetAlbumsByArtist missing stamped album %d", stamped.AlbumID)
	} else if al.AddedByUserID != 42 || al.AddedByUsername != "alice" {
		t.Errorf("GetAlbumsByArtist attribution = (%d, %q), want (42, alice)", al.AddedByUserID, al.AddedByUsername)
	}

	if albums, err := store.SearchAlbums(ctx, "Stamped", 10); err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	} else if al, ok := findAlbum(albums, stamped.AlbumID); !ok {
		t.Errorf("SearchAlbums missing stamped album %d", stamped.AlbumID)
	} else if al.AddedByUserID != 42 || al.AddedByUsername != "alice" {
		t.Errorf("SearchAlbums attribution = (%d, %q), want (42, alice)", al.AddedByUserID, al.AddedByUsername)
	}

	// ── System rows read back blank ──────────────────────────────────
	if tr, err := store.GetTrack(ctx, systemID); err != nil {
		t.Fatalf("GetTrack system: %v", err)
	} else if tr.AddedByUserID != 0 || tr.AddedByUsername != "" {
		t.Errorf("system track attribution = (%d, %q), want (0, \"\")", tr.AddedByUserID, tr.AddedByUsername)
	}
	if al, err := store.GetAlbum(ctx, system.AlbumID); err != nil {
		t.Fatalf("GetAlbum system: %v", err)
	} else if al.AddedByUserID != 0 || al.AddedByUsername != "" {
		t.Errorf("system album attribution = (%d, %q), want (0, \"\")", al.AddedByUserID, al.AddedByUsername)
	}

	// ── Playlist reads ───────────────────────────────────────────────
	playlistID, err := store.UpsertPlaylist(ctx, &domain.Playlist{
		Source: "deezer", SourcePlaylistID: "pl-a", Name: "Stamped Mix",
		AddedByUserID: 11, AddedByUsername: "erin",
	})
	if err != nil {
		t.Fatalf("UpsertPlaylist: %v", err)
	}
	systemPlaylistID, err := store.UpsertPlaylist(ctx, &domain.Playlist{
		Source: "deezer", SourcePlaylistID: "pl-sys", Name: "Auto",
	})
	if err != nil {
		t.Fatalf("UpsertPlaylist system: %v", err)
	}

	if p, err := store.GetPlaylist(ctx, playlistID); err != nil {
		t.Fatalf("GetPlaylist: %v", err)
	} else if p.AddedByUserID != 11 || p.AddedByUsername != "erin" {
		t.Errorf("GetPlaylist attribution = (%d, %q), want (11, erin)", p.AddedByUserID, p.AddedByUsername)
	}

	if p, err := store.GetPlaylistBySourceID(ctx, "deezer", "pl-a"); err != nil {
		t.Fatalf("GetPlaylistBySourceID: %v", err)
	} else if p == nil || p.AddedByUserID != 11 || p.AddedByUsername != "erin" {
		t.Errorf("GetPlaylistBySourceID attribution = %+v, want (11, erin)", p)
	}

	if pls, err := store.ListPlaylists(ctx); err != nil {
		t.Fatalf("ListPlaylists: %v", err)
	} else if p, ok := findPlaylist(pls, playlistID); !ok {
		t.Errorf("ListPlaylists missing stamped playlist %d", playlistID)
	} else if p.AddedByUserID != 11 || p.AddedByUsername != "erin" {
		t.Errorf("ListPlaylists attribution = (%d, %q), want (11, erin)", p.AddedByUserID, p.AddedByUsername)
	}

	if p, err := store.GetPlaylist(ctx, systemPlaylistID); err != nil {
		t.Fatalf("GetPlaylist system: %v", err)
	} else if p.AddedByUserID != 0 || p.AddedByUsername != "" {
		t.Errorf("system playlist attribution = (%d, %q), want (0, \"\")", p.AddedByUserID, p.AddedByUsername)
	}
}
