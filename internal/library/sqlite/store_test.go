package sqlite

import (
	"context"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestStore_ArtistCRUD(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath)
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
	store, err := New(dbPath)
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
	store, err := New(dbPath)
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
	store, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	_, err = store.UpsertArtist(ctx, &domain.Artist{Name: "Ext Artist", SpotifyID: "spotify:123"})
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

	// Unknown service returns error.
	_, err = store.GetArtistByExternalID(ctx, "unknown", "id")
	if err == nil {
		t.Error("expected error for unknown service")
	}
}

func TestStore_DuplicateUpsert(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath)
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
