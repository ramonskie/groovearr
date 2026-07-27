package playlist

import (
	"context"
	"os"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
)

type mockSource struct {
	name       string
	display    string
	configured bool
	playlists  []PlaylistInfo
	tracks     map[string][]TrackInfo
}

func (m *mockSource) Name() string        { return m.name }
func (m *mockSource) DisplayName() string { return m.display }
func (m *mockSource) IsConfigured() bool  { return m.configured }
func (m *mockSource) GetUserPlaylists(ctx context.Context) ([]PlaylistInfo, error) {
	return m.playlists, nil
}
func (m *mockSource) GetPlaylistTracks(ctx context.Context, sourceID string) ([]TrackInfo, string, error) {
	return m.tracks[sourceID], "", nil
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()

	src := &mockSource{name: "deezer", display: "Deezer", configured: true}
	if err := r.Register(src); err != nil {
		t.Fatal(err)
	}

	// Duplicate should fail.
	if err := r.Register(src); err == nil {
		t.Error("expected duplicate registration error")
	}

	if r.Get("deezer") == nil {
		t.Error("Get returned nil for registered source")
	}
	if r.Get("nonexistent") != nil {
		t.Error("Get returned source for unregistered name")
	}

	cfg := r.Configured()
	if len(cfg) != 1 {
		t.Errorf("expected 1 configured source, got %d", len(cfg))
	}
}

type mockStore struct {
	playlists      map[int64]*domain.Playlist
	playlistTracks map[int64][]domain.PlaylistTrack
	artists        map[string]*domain.Artist // name → artist
	searchTracks   []domain.Track            // tracks returned by SearchTracks
	nextID         int64
}

func (m *mockStore) next() int64 { m.nextID++; return m.nextID }

// Store interface methods (only the playlist subset, rest return nil/zero).
func (m *mockStore) UpsertPlaylist(ctx context.Context, p *domain.Playlist) (int64, error) {
	if p.ID == 0 {
		p.ID = m.next()
	}
	m.playlists[p.ID] = p
	return p.ID, nil
}
func (m *mockStore) GetPlaylist(ctx context.Context, id int64) (*domain.Playlist, error) {
	return m.playlists[id], nil
}
func (m *mockStore) GetPlaylistBySourceID(ctx context.Context, source, sourceID string) (*domain.Playlist, error) {
	for _, p := range m.playlists {
		if p.Source == source && p.SourcePlaylistID == sourceID {
			return p, nil
		}
	}
	return nil, nil
}
func (m *mockStore) ListPlaylists(ctx context.Context) ([]domain.Playlist, error) {
	var out []domain.Playlist
	for _, p := range m.playlists {
		out = append(out, *p)
	}
	return out, nil
}
func (m *mockStore) DeletePlaylist(ctx context.Context, id int64) error {
	delete(m.playlists, id)
	return nil
}
func (m *mockStore) UpsertPlaylistTrack(ctx context.Context, t *domain.PlaylistTrack) error {
	m.playlistTracks[t.PlaylistID] = append(m.playlistTracks[t.PlaylistID], *t)
	return nil
}
func (m *mockStore) GetPlaylistTracks(ctx context.Context, playlistID int64) ([]domain.PlaylistTrack, error) {
	return m.playlistTracks[playlistID], nil
}
func (m *mockStore) DeletePlaylistTracks(ctx context.Context, playlistID int64) error {
	delete(m.playlistTracks, playlistID)
	return nil
}

// Remaining Store methods — stubs.
func (m *mockStore) UpsertArtist(ctx context.Context, a *domain.Artist) (int64, error) {
	if m.artists == nil {
		m.artists = map[string]*domain.Artist{}
	}
	existing := m.artists[a.Name]
	if existing != nil {
		return existing.ID, nil
	}
	id := m.next()
	a.ID = id
	m.artists[a.Name] = a
	return id, nil
}
func (m *mockStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	for _, a := range m.artists {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, nil
}
func (m *mockStore) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	return m.artists[name], nil
}
func (m *mockStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	return nil, nil
}
func (m *mockStore) SearchArtists(ctx context.Context, query string, limit int) ([]domain.Artist, error) {
	return nil, nil
}
func (m *mockStore) SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error {
	return nil
}
func (m *mockStore) UpsertAlbum(ctx context.Context, a *domain.Album) (int64, error) { return 0, nil }
func (m *mockStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error)   { return nil, nil }
func (m *mockStore) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error) {
	return nil, nil
}
func (m *mockStore) SearchAlbums(ctx context.Context, query string, limit int) ([]domain.Album, error) {
	return nil, nil
}
func (m *mockStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) { return 0, nil }
func (m *mockStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error)   { return nil, nil }
func (m *mockStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return nil, nil
}
func (m *mockStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	return nil, nil
}
func (m *mockStore) SearchTracks(ctx context.Context, query string, limit int) ([]domain.Track, error) {
	return m.searchTracks, nil
}
func (m *mockStore) GetTrackByFilePath(ctx context.Context, fp string) (*domain.Track, error) {
	return nil, nil
}
func (m *mockStore) GetTrackByISRC(ctx context.Context, isrc string) (*domain.Track, error) {
	return nil, nil
}
func (m *mockStore) DeleteTrack(ctx context.Context, id int64) error { return nil }
func (m *mockStore) GetArtistByExternalID(ctx context.Context, svc, eid string) (*domain.Artist, error) {
	return nil, nil
}
func (m *mockStore) GetAlbumByExternalID(ctx context.Context, svc, eid string) (*domain.Album, error) {
	return nil, nil
}
func (m *mockStore) GetTrackByExternalID(ctx context.Context, svc, eid string) (*domain.Track, error) {
	return nil, nil
}
func (m *mockStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return nil, nil
}
func (m *mockStore) Close() error { return nil }
func (m *mockStore) ImportTrack(ctx context.Context, track *domain.Track, artistName, albumTitle string, albumYear int, genres []string) (int64, error) {
	return m.UpsertTrack(ctx, track)
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/src.txt"
	dst := dir + "/dst.txt"
	content := []byte("test data")
	os.WriteFile(src, content, 0o644)

	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(content) {
		t.Errorf("copied content = %q, want %q", string(got), string(content))
	}
}

func TestFindInLibrary(t *testing.T) {
	// Test findInLibrary with a mock store that returns search results.
	store := &mockStore{
		playlists:      map[int64]*domain.Playlist{},
		playlistTracks: map[int64][]domain.PlaylistTrack{},
		artists:        map[string]*domain.Artist{},
	}
	// Seed an artist and track in the store.
	store.artists["Test Artist"] = &domain.Artist{ID: 1, Name: "Test Artist"}
	store.searchTracks = []domain.Track{
		{ID: 10, ArtistID: 1, Title: "Test Song", Duration: 200000},
	}

	svc := &Service{
		store:   store,
		matcher: matching.New(),
	}

	t.Run("exact match", func(t *testing.T) {
		id := svc.findInLibrary(context.Background(), TrackInfo{
			Title: "Test Song", Artist: "Test Artist",
		})
		if id != 10 {
			t.Errorf("expected track ID 10, got %d", id)
		}
	})

	t.Run("no match", func(t *testing.T) {
		id := svc.findInLibrary(context.Background(), TrackInfo{
			Title: "Nonexistent", Artist: "Nobody",
		})
		if id != 0 {
			t.Errorf("expected 0, got %d", id)
		}
	})
}
