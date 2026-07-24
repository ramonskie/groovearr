package download

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockMetadataProvider implements metadata.Provider for testing.
type mockMetadataProvider struct {
	name        string
	cover       *metadata.CoverResult
	trackMeta   *metadata.TrackMetadata
	configured  bool
	connected   bool
}

func (m *mockMetadataProvider) Name() string                    { return m.name }
func (m *mockMetadataProvider) DisplayName() string             { return m.name }
func (m *mockMetadataProvider) IsConfigured() bool              { return m.configured }
func (m *mockMetadataProvider) IsMetadataAvailable() bool       { return m.configured }
func (m *mockMetadataProvider) CapabilityStatus() map[string]string {
	s := "not_configured"
	if m.configured { s = "connected" }
	return map[string]string{"metadata": s}
}
func (m *mockMetadataProvider) CheckConnection(ctx context.Context) error { return nil }
func (m *mockMetadataProvider) Connected() bool                 { return m.connected }

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
	track     *domain.Track
	artist    *domain.Artist
	album     *domain.Album
	tracks    []domain.Track
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

func TestMetadataEnrichmentHandler_SkipNoLibraryTrack(t *testing.T) {
	reg := metadata.NewRegistry()
	store := &mockLibraryStore{}
	handler := NewMetadataEnrichmentHandler(reg, store, testLogger())

	err := handler.Handle(context.Background(), &domain.DownloadRecord{
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
	handler := NewMetadataEnrichmentHandler(reg, store, testLogger())

	err := handler.Handle(context.Background(), &domain.DownloadRecord{
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
	handler := NewMetadataEnrichmentHandler(reg, store, testLogger())

	err := handler.Handle(context.Background(), &domain.DownloadRecord{
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
	handler := NewMetadataEnrichmentHandler(reg, store, testLogger())

	err := handler.Handle(context.Background(), &domain.DownloadRecord{
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
	handler := NewMetadataEnrichmentHandler(reg, store, testLogger())

	err := handler.Handle(context.Background(), &domain.DownloadRecord{
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
	handler := NewMetadataEnrichmentHandler(reg, store, testLogger())

	err := handler.Handle(context.Background(), &domain.DownloadRecord{
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
	handler := NewMetadataEnrichmentHandler(reg, store, testLogger())

	err := handler.Handle(context.Background(), &domain.DownloadRecord{
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
