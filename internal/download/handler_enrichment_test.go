package download

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockMetadataProvider implements metadata.Provider for testing.
type mockMetadataProvider struct {
	name       string
	cover      *metadata.CoverResult
	trackMeta  *metadata.TrackMetadata
	configured bool
	connected  bool
}

func (m *mockMetadataProvider) Name() string              { return m.name }
func (m *mockMetadataProvider) DisplayName() string       { return m.name }
func (m *mockMetadataProvider) IsConfigured() bool        { return m.configured }
func (m *mockMetadataProvider) IsMetadataAvailable() bool { return m.configured }
func (m *mockMetadataProvider) CapabilityStatus() map[string]string {
	s := "not_configured"
	if m.configured {
		s = "connected"
	}
	return map[string]string{"metadata": s}
}
func (m *mockMetadataProvider) CheckConnection(ctx context.Context) error { return nil }
func (m *mockMetadataProvider) Connected() bool                           { return m.connected }

func (m *mockMetadataProvider) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	return m.cover, nil
}

func (m *mockMetadataProvider) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}

func (m *mockMetadataProvider) SearchAlbum(ctx context.Context, artist, title string) string {
	return "" // default: no album found
}

func (m *mockMetadataProvider) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	return m.trackMeta, nil
}

// mockLibraryStore provides the methods needed by MetadataEnrichmentHandler.
type mockLibraryStore struct {
	track       *domain.Track
	artist      *domain.Artist
	album       *domain.Album
	tracks      []domain.Track
	getTrackErr error
}

func (m *mockLibraryStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error) {
	if m.getTrackErr != nil {
		return nil, m.getTrackErr
	}
	return m.track, nil
}

func (m *mockLibraryStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	return m.artist, nil
}

func (m *mockLibraryStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	return m.album, nil
}

func (m *mockLibraryStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return m.tracks, nil
}

func (m *mockLibraryStore) UpsertTrack(ctx context.Context, track *domain.Track) (int64, error) {
	m.track = track
	return track.ID, nil
}

func (m *mockLibraryStore) UpsertAlbum(ctx context.Context, album *domain.Album) (int64, error) {
	m.album = album
	return album.ID, nil
}

func (m *mockLibraryStore) SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error {
	if m.artist != nil {
		m.artist.ThumbURL = thumbURL
	}
	return nil
}

// mockDiscoveryProvider counts SearchArtists calls, standing in for a
// discovery provider (Deezer, Spotify, etc.) that serves artist images.
type mockDiscoveryProvider struct {
	calls *int
}

var _ discovery.Provider = (*mockDiscoveryProvider)(nil)

func (m *mockDiscoveryProvider) Name() string        { return "mockdisc" }
func (m *mockDiscoveryProvider) DisplayName() string { return "mockdisc" }
func (m *mockDiscoveryProvider) IsConfigured() bool  { return true }
func (m *mockDiscoveryProvider) Connected() bool     { return true }
func (m *mockDiscoveryProvider) CapabilityStatus() map[string]string {
	return map[string]string{"discovery": "connected"}
}
func (m *mockDiscoveryProvider) CheckConnection(context.Context) error { return nil }
func (m *mockDiscoveryProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	*m.calls++
	return nil, nil
}
func (m *mockDiscoveryProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}
func (m *mockDiscoveryProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	return nil, nil
}
func (m *mockDiscoveryProvider) SearchAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}

type mockDiscoveryFactory struct {
	calls *int
}

var _ plugin.PluginFactory = (*mockDiscoveryFactory)(nil)

func (f *mockDiscoveryFactory) Name() string        { return "mockdisc" }
func (f *mockDiscoveryFactory) DisplayName() string { return "mockdisc" }
func (f *mockDiscoveryFactory) Capabilities() []string {
	return []string{"discovery"}
}
func (f *mockDiscoveryFactory) Create(json.RawMessage, plugin.PluginResources) (plugin.BasePlugin, error) {
	return &mockDiscoveryProvider{calls: f.calls}, nil
}
func (f *mockDiscoveryFactory) ValidateConfig(json.RawMessage) error { return nil }
func (f *mockDiscoveryFactory) DefaultConfig() json.RawMessage       { return json.RawMessage("{}") }

func newDiscoveryRegistryWithMock(calls *int) *discovery.Registry {
	discReg := discovery.NewRegistry(plugin.NewRegistry())
	_ = discReg.RegisterFactory(&mockDiscoveryFactory{calls: calls})
	_ = discReg.Inner().Register(&mockDiscoveryProvider{calls: calls})
	return discReg
}

func TestMetadataEnrichmentHandler_SkipNoLibraryTrack(t *testing.T) {
	reg := metadata.NewRegistry()
	store := &mockLibraryStore{}
	handler := NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	err := handler.Handle(context.Background(), &Record{
		LibraryTrackID: 0,
		FilePath:       "/tmp/test.mp3",
	})
	if err != nil {
		t.Errorf("expected nil error for missing library track, got %v", err)
	}
}

func TestMetadataEnrichmentHandler_NoProviders(t *testing.T) {
	reg := metadata.NewRegistry()
	store := &mockLibraryStore{
		track:  &domain.Track{ID: 1, ArtistID: 1, AlbumID: 1, FilePath: "/tmp/test.mp3"},
		artist: &domain.Artist{ID: 1, Name: "Test Artist"},
		album:  &domain.Album{ID: 1, Title: "Test Album", ArtistID: 1},
		tracks: []domain.Track{{ID: 1, FilePath: "/tmp/test.mp3"}},
	}
	handler := NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	err := handler.Handle(context.Background(), &Record{
		LibraryTrackID: 1,
		FilePath:       "/tmp/test.mp3",
	})
	if err != nil {
		t.Errorf("expected nil error with no providers, got %v", err)
	}
}

func TestMetadataEnrichmentHandler_EnrichISRC(t *testing.T) {
	reg := metadata.NewRegistry()
	provider := &mockMetadataProvider{
		name:       "test",
		configured: true,
		connected:  true,
		trackMeta: &metadata.TrackMetadata{
			ISRC: "US-ABC-12-34567",
			ExternalIDs: map[string]string{
				"musicbrainz_release": "mbid-123",
			},
		},
	}
	reg.Register(provider)

	track := &domain.Track{ID: 1, ArtistID: 1, AlbumID: 1, FilePath: "/tmp/test.mp3"}
	store := &mockLibraryStore{
		track:  track,
		artist: &domain.Artist{ID: 1, Name: "Test Artist"},
		album:  &domain.Album{ID: 1, Title: "Test Album", ArtistID: 1},
		tracks: []domain.Track{{ID: 1, FilePath: "/tmp/test.mp3"}},
	}
	handler := NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	err := handler.Handle(context.Background(), &Record{
		LibraryTrackID: 1,
		FilePath:       "/tmp/test.mp3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.track.ISRC != "US-ABC-12-34567" {
		t.Errorf("expected ISRC to be set, got %q", store.track.ISRC)
	}
	if store.track.ExternalIDs["musicbrainz_release"] != "mbid-123" {
		t.Errorf("expected external ID to be set, got %q", store.track.ExternalIDs["musicbrainz_release"])
	}
}

func TestMetadataEnrichmentHandler_DoesNotOverwriteExisting(t *testing.T) {
	reg := metadata.NewRegistry()
	provider := &mockMetadataProvider{
		name:       "test",
		configured: true,
		connected:  true,
		trackMeta: &metadata.TrackMetadata{
			ISRC: "new-isrc",
		},
	}
	reg.Register(provider)

	store := &mockLibraryStore{
		track:  &domain.Track{ID: 1, ArtistID: 1, AlbumID: 1, FilePath: "/tmp/test.mp3", ISRC: "existing-isrc"},
		artist: &domain.Artist{ID: 1, Name: "Test Artist"},
		album:  &domain.Album{ID: 1, Title: "Test Album", ArtistID: 1},
		tracks: []domain.Track{{ID: 1, FilePath: "/tmp/test.mp3"}},
	}
	handler := NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	err := handler.Handle(context.Background(), &Record{
		LibraryTrackID: 1,
		FilePath:       "/tmp/test.mp3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.track.ISRC != "existing-isrc" {
		t.Errorf("expected existing ISRC to be preserved, got %q", store.track.ISRC)
	}
}

func TestMetadataEnrichmentHandler_NoOpProvider(t *testing.T) {
	reg := metadata.NewRegistry()
	provider := &mockMetadataProvider{
		name:       "test",
		configured: true,
		connected:  true,
		trackMeta:  nil, // returns nil, nil — no enrichment available
		cover:      nil,
	}
	reg.Register(provider)

	store := &mockLibraryStore{
		track:  &domain.Track{ID: 1, ArtistID: 1, AlbumID: 1, FilePath: "/tmp/test.mp3"},
		artist: &domain.Artist{ID: 1, Name: "Test Artist"},
		album:  &domain.Album{ID: 1, Title: "Test Album", ArtistID: 1},
		tracks: []domain.Track{{ID: 1, FilePath: "/tmp/test.mp3"}},
	}
	handler := NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	err := handler.Handle(context.Background(), &Record{
		LibraryTrackID: 1,
		FilePath:       "/tmp/test.mp3",
	})
	if err != nil {
		t.Fatalf("expected nil error for nil metadata, got %v", err)
	}
}

func TestMetadataEnrichmentHandler_TrackNotFound(t *testing.T) {
	reg := metadata.NewRegistry()
	store := &mockLibraryStore{
		getTrackErr: os.ErrNotExist,
	}
	handler := NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	err := handler.Handle(context.Background(), &Record{
		LibraryTrackID: 999,
		FilePath:       "/tmp/test.mp3",
	})
	if err != nil {
		t.Errorf("expected nil error for missing track, got %v", err)
	}
}

func TestMetadataEnrichmentHandler_CoverDoesNotOverwrite(t *testing.T) {
	// Create a real temp directory with an existing cover.jpg.
	tmpDir := t.TempDir()
	coverPath := filepath.Join(tmpDir, "cover.jpg")
	if err := os.WriteFile(coverPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	reg := metadata.NewRegistry()
	provider := &mockMetadataProvider{
		name:       "test",
		configured: true,
		connected:  true,
		cover: &metadata.CoverResult{
			ImageURL: "http://localhost/not-called.jpg",
			ThumbURL: "http://localhost/not-called.jpg",
			Source:   "test",
		},
	}
	reg.Register(provider)

	store := &mockLibraryStore{
		track:  &domain.Track{ID: 1, ArtistID: 1, AlbumID: 1, FilePath: filepath.Join(tmpDir, "test.mp3")},
		artist: &domain.Artist{ID: 1, Name: "Test Artist"},
		album:  &domain.Album{ID: 1, Title: "Test Album", ArtistID: 1, ThumbURL: "cover.jpg"},
		tracks: []domain.Track{{ID: 1, FilePath: filepath.Join(tmpDir, "test.mp3")}},
	}
	handler := NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	err := handler.Handle(context.Background(), &Record{
		LibraryTrackID: 1,
		FilePath:       filepath.Join(tmpDir, "test.mp3"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify existing cover was not overwritten.
	data, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Errorf("cover was overwritten, expected 'existing', got %q", string(data))
	}
}

// Compile-time interface checks for mocks.
var _ metadata.Provider = (*mockMetadataProvider)(nil)
var _ ImportHandler = (*MetadataEnrichmentHandler)(nil)

// Ensure mockMetadataProvider satisfies plugin.BasePlugin (embedded in metadata.Provider).
var _ plugin.BasePlugin = (*mockMetadataProvider)(nil)

func TestArtistImageSearchDedupedPerTTL(t *testing.T) {
	root := t.TempDir()
	trackPath := filepath.Join(root, "Artist", "Album", "01 - Track.flac")
	if err := os.MkdirAll(filepath.Dir(trackPath), 0o755); err != nil {
		t.Fatal(err)
	}

	reg := metadata.NewRegistry()
	_ = reg.Register(&mockMetadataProvider{name: "mock", configured: true, connected: true})

	store := &mockLibraryStore{
		track:  &domain.Track{ID: 1, ArtistID: 1, AlbumID: 1, FilePath: trackPath},
		artist: &domain.Artist{ID: 1, Name: "Test Artist"},
		album:  &domain.Album{ID: 1, Title: "Test Album", ArtistID: 1},
		tracks: []domain.Track{{ID: 1, FilePath: trackPath}},
	}

	var discCalls int
	handler := NewMetadataEnrichmentHandler(reg, newDiscoveryRegistryWithMock(&discCalls), store, testLogger())

	// First import triggers the artist image search.
	if err := handler.Handle(context.Background(), &Record{LibraryTrackID: 1}); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// Second import of the same artist within the retry window must not
	// re-hit the discovery provider.
	if err := handler.Handle(context.Background(), &Record{LibraryTrackID: 1}); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if discCalls != 1 {
		t.Errorf("SearchArtists called %d times, want 1 (TTL dedup)", discCalls)
	}
}

func TestArtistImageSearchRetriesAfterTTL(t *testing.T) {
	root := t.TempDir()
	trackPath := filepath.Join(root, "Artist", "Album", "01 - Track.flac")
	if err := os.MkdirAll(filepath.Dir(trackPath), 0o755); err != nil {
		t.Fatal(err)
	}

	reg := metadata.NewRegistry()
	_ = reg.Register(&mockMetadataProvider{name: "mock", configured: true, connected: true})

	store := &mockLibraryStore{
		track:  &domain.Track{ID: 1, ArtistID: 1, AlbumID: 1, FilePath: trackPath},
		artist: &domain.Artist{ID: 1, Name: "Test Artist"},
		album:  &domain.Album{ID: 1, Title: "Test Album", ArtistID: 1},
		tracks: []domain.Track{{ID: 1, FilePath: trackPath}},
	}

	var discCalls int
	handler := NewMetadataEnrichmentHandler(reg, newDiscoveryRegistryWithMock(&discCalls), store, testLogger())

	if err := handler.Handle(context.Background(), &Record{LibraryTrackID: 1}); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if discCalls != 1 {
		t.Fatalf("SearchArtists called %d times after first import, want 1", discCalls)
	}

	// Expire the retry window by back-dating the recorded attempt.
	handler.imageMu.Lock()
	handler.imageAttempted[1] = handler.imageAttempted[1].Add(-artistImageRetryTTL - time.Second)
	handler.imageMu.Unlock()

	if err := handler.Handle(context.Background(), &Record{LibraryTrackID: 1}); err != nil {
		t.Fatalf("Handle after TTL: %v", err)
	}
	if discCalls != 2 {
		t.Errorf("SearchArtists called %d times, want 2 after TTL expiry", discCalls)
	}
}
