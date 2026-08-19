package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var _ library.Store = (*enrichTestStore)(nil)

// enrichTestStore is a library.Store whose only behavior is returning the
// configured track list for ListTracksWithQuality.
type enrichTestStore struct {
	tracks []domain.Track
}

func (m *enrichTestStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return m.tracks, nil
}
func (m *enrichTestStore) UpsertArtist(ctx context.Context, a *domain.Artist) (int64, error) { return 0, nil }
func (m *enrichTestStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error)   { return nil, nil }
func (m *enrichTestStore) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	return nil, nil
}
func (m *enrichTestStore) ListArtists(ctx context.Context, o, l int) ([]domain.Artist, error) {
	return nil, nil
}
func (m *enrichTestStore) SearchArtists(ctx context.Context, q string, l int) ([]domain.Artist, error) {
	return nil, nil
}
func (m *enrichTestStore) SetArtistThumbURL(ctx context.Context, id int64, u string) error { return nil }
func (m *enrichTestStore) UpsertAlbum(ctx context.Context, a *domain.Album) (int64, error)  { return 0, nil }
func (m *enrichTestStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error)    { return nil, nil }
func (m *enrichTestStore) GetAlbumsByArtist(ctx context.Context, id int64) ([]domain.Album, error) {
	return nil, nil
}
func (m *enrichTestStore) SearchAlbums(ctx context.Context, q string, l int) ([]domain.Album, error) {
	return nil, nil
}
func (m *enrichTestStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) { return 0, nil }
func (m *enrichTestStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error)   { return nil, nil }
func (m *enrichTestStore) GetTracksByAlbum(ctx context.Context, id int64) ([]domain.Track, error) {
	return nil, nil
}
func (m *enrichTestStore) GetTracksByArtist(ctx context.Context, id int64) ([]domain.Track, error) {
	return nil, nil
}
func (m *enrichTestStore) SearchTracks(ctx context.Context, q string, l int) ([]domain.Track, error) {
	return nil, nil
}
func (m *enrichTestStore) GetTrackByFilePath(ctx context.Context, p string) (*domain.Track, error) {
	return nil, nil
}
func (m *enrichTestStore) GetTrackByISRC(ctx context.Context, i string) (*domain.Track, error) {
	return nil, nil
}
func (m *enrichTestStore) DeleteTrack(ctx context.Context, id int64) error                 { return nil }
func (m *enrichTestStore) ImportTrack(ctx context.Context, t *domain.Track, a, al string, y int, g []string) (int64, error) {
	return 0, nil
}
func (m *enrichTestStore) GetArtistByExternalID(ctx context.Context, s, e string) (*domain.Artist, error) {
	return nil, nil
}
func (m *enrichTestStore) GetAlbumByExternalID(ctx context.Context, s, e string) (*domain.Album, error) {
	return nil, nil
}
func (m *enrichTestStore) GetTrackByExternalID(ctx context.Context, s, e string) (*domain.Track, error) {
	return nil, nil
}
func (m *enrichTestStore) UpsertPlaylist(ctx context.Context, p *domain.Playlist) (int64, error) {
	return 0, nil
}
func (m *enrichTestStore) GetPlaylist(ctx context.Context, id int64) (*domain.Playlist, error) {
	return nil, nil
}
func (m *enrichTestStore) GetPlaylistBySourceID(ctx context.Context, s, id string) (*domain.Playlist, error) {
	return nil, nil
}
func (m *enrichTestStore) ListPlaylists(ctx context.Context) ([]domain.Playlist, error) { return nil, nil }
func (m *enrichTestStore) DeletePlaylist(ctx context.Context, id int64) error           { return nil }
func (m *enrichTestStore) UpsertPlaylistTrack(ctx context.Context, t *domain.PlaylistTrack) error {
	return nil
}
func (m *enrichTestStore) GetPlaylistTracks(ctx context.Context, id int64) ([]domain.PlaylistTrack, error) {
	return nil, nil
}
func (m *enrichTestStore) DeletePlaylistTracks(ctx context.Context, id int64) error { return nil }
func (m *enrichTestStore) Close() error                                             { return nil }

// trackingEnrichStore observes per-album concurrency from the enrichment
// handler's track lookups. Each GetTrack sleeps to widen the window in which
// concurrent workers would overlap.
type trackingEnrichStore struct {
	mu            sync.Mutex
	albumOf       map[int64]int64 // trackID → albumID
	active        map[int64]int   // albumID → currently active
	maxPerAlbum   map[int64]int
	maxTotal      int
}

func newTrackingEnrichStore(albumOf map[int64]int64) *trackingEnrichStore {
	return &trackingEnrichStore{
		albumOf:     albumOf,
		active:      make(map[int64]int),
		maxPerAlbum: make(map[int64]int),
	}
}

func (m *trackingEnrichStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error) {
	album := m.albumOf[id]
	m.mu.Lock()
	m.active[album]++
	if m.active[album] > m.maxPerAlbum[album] {
		m.maxPerAlbum[album] = m.active[album]
	}
	total := 0
	for _, v := range m.active {
		total += v
	}
	if total > m.maxTotal {
		m.maxTotal = total
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.active[album]--
		m.mu.Unlock()
	}()
	time.Sleep(40 * time.Millisecond)
	return &domain.Track{ID: id, AlbumID: album, ArtistID: 1, FilePath: "/music/album/track.flac"}, nil
}

func (m *trackingEnrichStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	return &domain.Artist{ID: id, Name: "Test Artist"}, nil
}
func (m *trackingEnrichStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	return &domain.Album{ID: id, Title: "Test Album"}, nil
}
func (m *trackingEnrichStore) SetArtistThumbURL(ctx context.Context, id int64, u string) error { return nil }
func (m *trackingEnrichStore) GetTracksByAlbum(ctx context.Context, id int64) ([]domain.Track, error) {
	return nil, nil
}
func (m *trackingEnrichStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) {
	return 0, nil
}
func (m *trackingEnrichStore) UpsertAlbum(ctx context.Context, a *domain.Album) (int64, error) {
	return 0, nil
}

// newEnrichServer builds a Server whose enrichment handler has no providers
// (so enrichTrack short-circuits after lookups) but whose store records
// per-album concurrency.
func newEnrichServer(tracks []domain.Track, albumOf map[int64]int64) (*Server, *trackingEnrichStore) {
	es := newTrackingEnrichStore(albumOf)
	h := download.NewMetadataEnrichmentHandler(
		metadata.NewRegistry(),
		discovery.NewRegistry(plugin.NewRegistry()),
		es,
		testLogger(),
	)
	return &Server{
		enrichmentHandler: h,
		store:             &enrichTestStore{tracks: tracks},
		log:               testLogger(),
	}, es
}

func TestEnrichRunnerAlbumSerialization(t *testing.T) {
	// 8 tracks alternating between two albums — different albums must enrich
	// concurrently, same-album tracks must never overlap.
	tracks := []domain.Track{
		{ID: 1, AlbumID: 10, Title: "A1"},
		{ID: 2, AlbumID: 20, Title: "B1"},
		{ID: 3, AlbumID: 10, Title: "A2"},
		{ID: 4, AlbumID: 20, Title: "B2"},
		{ID: 5, AlbumID: 10, Title: "A3"},
		{ID: 6, AlbumID: 20, Title: "B3"},
		{ID: 7, AlbumID: 10, Title: "A4"},
		{ID: 8, AlbumID: 20, Title: "B4"},
	}
	albumOf := map[int64]int64{1: 10, 3: 10, 5: 10, 7: 10, 2: 20, 4: 20, 6: 20, 8: 20}
	srv, es := newEnrichServer(tracks, albumOf)

	var last int64
	var mu sync.Mutex
	err := srv.enrichRunner(context.Background(), func(r jobs.Report) {
		mu.Lock()
		last = int64(r.Done)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	done := last
	mu.Unlock()
	if done != 8 {
		t.Fatalf("processed %d tracks, want 8", done)
	}
	for album, max := range es.maxPerAlbum {
		if max > 1 {
			t.Errorf("album %d had %d concurrent enrichments, want 1 (serialized per album)", album, max)
		}
	}
	if es.maxTotal < 2 {
		t.Errorf("max concurrent across albums = %d, want >= 2 (parallelism)", es.maxTotal)
	}
}

func TestEnrichRunnerCancel(t *testing.T) {
	tracks := []domain.Track{
		{ID: 1, AlbumID: 10, Title: "A1"},
		{ID: 2, AlbumID: 10, Title: "A2"},
		{ID: 3, AlbumID: 10, Title: "A3"},
		{ID: 4, AlbumID: 10, Title: "A4"},
		{ID: 5, AlbumID: 10, Title: "A5"},
		{ID: 6, AlbumID: 10, Title: "A6"},
	}
	albumOf := map[int64]int64{1: 10, 2: 10, 3: 10, 4: 10, 5: 10, 6: 10}
	srv, _ := newEnrichServer(tracks, albumOf)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()

	err := srv.enrichRunner(ctx, func(r jobs.Report) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
