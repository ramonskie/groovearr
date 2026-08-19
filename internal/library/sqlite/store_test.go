package sqlite

import (
	"context"
	"io"
	"log/slog"
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
