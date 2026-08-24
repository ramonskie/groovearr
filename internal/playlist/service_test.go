package playlist

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
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
func (m *mockStore) CountPlaylistsByName(ctx context.Context, source, name string) (int64, error) {
	var n int64
	for _, p := range m.playlists {
		if p.Source == source && p.Name == name {
			n++
		}
	}
	return n, nil
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
func (m *mockStore) MergeArtists(ctx context.Context, keepID, removeID int64) error      { return nil }
func (m *mockStore) RenameArtist(ctx context.Context, artistID int64, name string) error { return nil }
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

func TestShortSourceID(t *testing.T) {
	if got := shortSourceID("12345678-aaaa"); got != "12345678" {
		t.Errorf("shortSourceID(long) = %q, want %q", got, "12345678")
	}
	if got := shortSourceID("short"); got != "short" {
		t.Errorf("shortSourceID(short) = %q, want %q", got, "short")
	}
}

func TestResolvePlaylistDisplay(t *testing.T) {
	store := &mockStore{
		playlists:      map[int64]*domain.Playlist{},
		playlistTracks: map[int64][]domain.PlaylistTrack{},
		artists:        map[string]*domain.Artist{},
	}
	// Two tidal playlists share the name "My Mix".
	store.playlists[1] = &domain.Playlist{ID: 1, Source: "tidal", SourcePlaylistID: "aaaa-bbbb-cccc-dddd", Name: "My Mix"}
	store.playlists[2] = &domain.Playlist{ID: 2, Source: "tidal", SourcePlaylistID: "1111-2222-3333-4444", Name: "My Mix"}
	svc := &Service{store: store}

	t.Run("same-name conflict is flagged and suffixed", func(t *testing.T) {
		p := &domain.Playlist{Source: "tidal", SourcePlaylistID: "aaaa-bbbb-cccc-dddd", Name: "My Mix"}
		svc.ResolvePlaylistDisplay(context.Background(), p)
		if !p.NameConflict {
			t.Error("expected NameConflict=true for same-name playlist")
		}
		if p.FolderName != "My Mix (aaaa-bbb)" {
			t.Errorf("FolderName = %q, want %q", p.FolderName, "My Mix (aaaa-bbb)")
		}
	})

	t.Run("unique name stays plain", func(t *testing.T) {
		p := &domain.Playlist{Source: "tidal", SourcePlaylistID: "9999-8888-7777-6666", Name: "Solo Mix"}
		svc.ResolvePlaylistDisplay(context.Background(), p)
		if p.NameConflict {
			t.Error("expected NameConflict=false for unique-name playlist")
		}
		if p.FolderName != "Solo Mix" {
			t.Errorf("FolderName = %q, want %q", p.FolderName, "Solo Mix")
		}
	})
}

func TestBuildPlaylistFolderNameConflict(t *testing.T) {
	root := t.TempDir()
	store := &mockStore{
		playlists:      map[int64]*domain.Playlist{},
		playlistTracks: map[int64][]domain.PlaylistTrack{},
		artists:        map[string]*domain.Artist{},
	}
	svc := &Service{
		store: store,
		cfgFn: func() config.Config {
			return config.Config{Library: config.LibraryConfig{PlaylistPath: root}}
		},
		log: slog.Default(),
	}

	ctx := context.Background()
	store.playlists[1] = &domain.Playlist{ID: 1, Source: "tidal", SourcePlaylistID: "aaaa-bbbb-cccc-dddd", Name: "My Mix"}
	store.playlists[2] = &domain.Playlist{ID: 2, Source: "tidal", SourcePlaylistID: "1111-2222-3333-4444", Name: "My Mix"}
	store.playlists[3] = &domain.Playlist{ID: 3, Source: "tidal", SourcePlaylistID: "9999-8888-7777-6666", Name: "Solo Mix"}

	svc.buildPlaylistFolder(ctx, 1)
	svc.buildPlaylistFolder(ctx, 2)
	svc.buildPlaylistFolder(ctx, 3)

	// Same-name playlists get ID-suffixed folders so their files never mix.
	for _, tc := range []struct {
		id   int64
		want string
	}{
		{1, "My Mix (aaaa-bbb)"},
		{2, "My Mix (1111-222)"},
	} {
		if _, err := os.Stat(filepath.Join(root, tc.want)); err != nil {
			t.Errorf("playlist %d: folder %q not created: %v", tc.id, tc.want, err)
		}
	}
	// Unique-name playlists keep the plain folder name.
	if _, err := os.Stat(filepath.Join(root, "Solo Mix")); err != nil {
		t.Errorf("playlist 3: plain folder %q not created: %v", "Solo Mix", err)
	}
}
