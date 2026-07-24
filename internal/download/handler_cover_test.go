package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

// mockLibStore implements library.Store for testing CoverArtHandler.
type mockLibStore struct {
	artists map[int64]*domain.Artist
	albums  map[int64]*domain.Album
	tracks  map[int64]*domain.Track
	nextID  int64
}

func newMockLibStore() *mockLibStore {
	return &mockLibStore{
		artists: make(map[int64]*domain.Artist),
		albums:  make(map[int64]*domain.Album),
		tracks:  make(map[int64]*domain.Track),
		nextID:  1,
	}
}

func (m *mockLibStore) UpsertArtist(ctx context.Context, a *domain.Artist) (int64, error) {
	if a.ID == 0 {
		a.ID = m.nextID
		m.nextID++
	}
	m.artists[a.ID] = a
	return a.ID, nil
}

func (m *mockLibStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	return m.artists[id], nil
}

func (m *mockLibStore) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	for _, a := range m.artists {
		if a.Name == name {
			return a, nil
		}
	}
	return nil, nil
}

func (m *mockLibStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	return nil, nil
}

func (m *mockLibStore) SearchArtists(ctx context.Context, query string, limit int) ([]domain.Artist, error) {
	return nil, nil
}
func (m *mockLibStore) SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error {
	return nil
}

func (m *mockLibStore) UpsertAlbum(ctx context.Context, a *domain.Album) (int64, error) {
	if a.ID == 0 {
		a.ID = m.nextID
		m.nextID++
	}
	m.albums[a.ID] = a
	return a.ID, nil
}

func (m *mockLibStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	return m.albums[id], nil
}

func (m *mockLibStore) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error) {
	return nil, nil
}

func (m *mockLibStore) SearchAlbums(ctx context.Context, query string, limit int) ([]domain.Album, error) {
	var result []domain.Album
	for _, a := range m.albums {
		if a.Title == query || filepath.Base(a.Title) == query {
			result = append(result, *a)
		}
	}
	return result, nil
}

func (m *mockLibStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) {
	if t.ID == 0 {
		t.ID = m.nextID
		m.nextID++
	}
	cp := *t
	m.tracks[t.ID] = &cp
	return t.ID, nil
}

func (m *mockLibStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error) {
	return m.tracks[id], nil
}

func (m *mockLibStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return nil, nil
}

func (m *mockLibStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	return nil, nil
}

func (m *mockLibStore) SearchTracks(ctx context.Context, query string, limit int) ([]domain.Track, error) {
	return nil, nil
}

func (m *mockLibStore) GetTrackByFilePath(ctx context.Context, filePath string) (*domain.Track, error) {
	for _, t := range m.tracks {
		if t.FilePath == filePath {
			return t, nil
		}
	}
	return nil, nil
}
func (m *mockLibStore) GetTrackByISRC(ctx context.Context, isrc string) (*domain.Track, error) { return nil, nil }

func (m *mockLibStore) DeleteTrack(ctx context.Context, id int64) error { return nil }

func (m *mockLibStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) { return nil, nil }

func (m *mockLibStore) GetArtistByExternalID(ctx context.Context, service, externalID string) (*domain.Artist, error) {
	return nil, nil
}

func (m *mockLibStore) GetAlbumByExternalID(ctx context.Context, service, externalID string) (*domain.Album, error) {
	return nil, nil
}

func (m *mockLibStore) GetTrackByExternalID(ctx context.Context, service, externalID string) (*domain.Track, error) {
	return nil, nil
}

func (m *mockLibStore) UpsertPlaylist(ctx context.Context, p *domain.Playlist) (int64, error) { return 0, nil }
func (m *mockLibStore) GetPlaylist(ctx context.Context, id int64) (*domain.Playlist, error)   { return nil, nil }
func (m *mockLibStore) GetPlaylistBySourceID(ctx context.Context, source, sourceID string) (*domain.Playlist, error) {
	return nil, nil
}

func (m *mockLibStore) ListPlaylists(ctx context.Context) ([]domain.Playlist, error) { return nil, nil }
func (m *mockLibStore) DeletePlaylist(ctx context.Context, id int64) error            { return nil }
func (m *mockLibStore) UpsertPlaylistTrack(ctx context.Context, t *domain.PlaylistTrack) error {
	return nil
}

func (m *mockLibStore) GetPlaylistTracks(ctx context.Context, playlistID int64) ([]domain.PlaylistTrack, error) {
	return nil, nil
}

func (m *mockLibStore) DeletePlaylistTracks(ctx context.Context, playlistID int64) error { return nil }
func (m *mockLibStore) Close() error                                                      { return nil }
func (m *mockLibStore) ImportTrack(ctx context.Context, track *domain.Track, artistName, albumTitle string, albumYear int, genres []string) (int64, error) {
	// Get or create artist (like real store does).
	existingArtist, _ := m.GetArtistByName(ctx, artistName)
	if existingArtist == nil {
		m.UpsertArtist(ctx, &domain.Artist{Name: artistName})
		existingArtist, _ = m.GetArtistByName(ctx, artistName)
	}
	// Get or create album.
	albums, _ := m.SearchAlbums(ctx, albumTitle, 10)
	var albumID int64
	for _, al := range albums {
		if al.Title == albumTitle && al.ArtistID == existingArtist.ID {
			albumID = al.ID
			break
		}
	}
	if albumID == 0 {
		m.UpsertAlbum(ctx, &domain.Album{ArtistID: existingArtist.ID, Title: albumTitle, Year: albumYear, AlbumType: domain.AlbumTypeAlbum})
		albums, _ = m.SearchAlbums(ctx, albumTitle, 10)
		for _, al := range albums {
			if al.Title == albumTitle && al.ArtistID == existingArtist.ID {
				albumID = al.ID
				break
			}
		}
	}
	track.ArtistID = existingArtist.ID
	track.AlbumID = albumID
	return m.UpsertTrack(ctx, track)
}

func TestCoverArtHandler_DownloadsCover(t *testing.T) {
	// Start test HTTP server serving a fake image.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-jpeg-data"))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	albumDir := filepath.Join(tmpDir, "Test Artist", "Test Album")
	os.MkdirAll(albumDir, 0o755)

	// Create artist and album in mock store.
	libStore := newMockLibStore()
	libStore.UpsertArtist(context.Background(), &domain.Artist{ID: 1, Name: "Test Artist"})
	libStore.UpsertAlbum(context.Background(), &domain.Album{
		ID:       1,
		ArtistID: 1,
		Title:    "Test Album",
	})

	handler := NewCoverArtHandler(libStore, testLogger())
	record := &domain.DownloadRecord{
		ID:       "test-cover-1",
		FilePath: filepath.Join(albumDir, "01 track.mp3"),
		CoverURL: srv.URL + "/cover.jpg",
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Verify cover.jpg was downloaded.
	coverPath := filepath.Join(albumDir, "cover.jpg")
	data, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatalf("cover.jpg not found: %v", err)
	}
	if string(data) != "fake-jpeg-data" {
		t.Errorf("expected fake-jpeg-data, got %q", string(data))
	}

	// Verify album thumb was updated.
	album, _ := libStore.GetAlbum(context.Background(), 1)
	if album.ThumbURL != "cover.jpg" {
		t.Errorf("expected thumb_url cover.jpg, got %q", album.ThumbURL)
	}
}

func TestCoverArtHandler_EmptyCoverURL(t *testing.T) {
	handler := NewCoverArtHandler(newMockLibStore(), testLogger())
	record := &domain.DownloadRecord{
		ID:       "test-cover-2",
		FilePath: "/tmp/somefile.mp3",
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle should succeed with empty CoverURL: %v", err)
	}
}

func TestCoverArtHandler_SkipsExistingCover(t *testing.T) {
	tmpDir := t.TempDir()
	albumDir := filepath.Join(tmpDir, "Test Artist", "Test Album2")
	os.MkdirAll(albumDir, 0o755)

	// Pre-create a cover.jpg.
	coverPath := filepath.Join(albumDir, "cover.jpg")
	os.WriteFile(coverPath, []byte("existing-cover"), 0o644)

	libStore := newMockLibStore()
	libStore.UpsertArtist(context.Background(), &domain.Artist{ID: 1, Name: "Test Artist"})
	libStore.UpsertAlbum(context.Background(), &domain.Album{
		ID:       1,
		ArtistID: 1,
		Title:    "Test Album2",
	})

	handler := NewCoverArtHandler(libStore, testLogger())
	record := &domain.DownloadRecord{
		ID:       "test-cover-3",
		FilePath: filepath.Join(albumDir, "01 track.mp3"),
		CoverURL: "http://invalid-url-that-would-fail/cover.jpg",
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Original cover should remain.
	data, _ := os.ReadFile(coverPath)
	if string(data) != "existing-cover" {
		t.Errorf("existing cover should remain, got %q", string(data))
	}
}
