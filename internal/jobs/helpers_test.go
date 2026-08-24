package jobs

import (
	"context"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// organizeRunnerStore is a minimal store for exercising the organize runner:
type organizeRunnerStore struct {
	library.Store
	artists   []domain.Artist
	albums    map[int64][]domain.Album
	tracks    map[int64][]domain.Track
	allTracks []domain.Track
}

func (s *organizeRunnerStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	if offset >= len(s.artists) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.artists) {
		end = len(s.artists)
	}
	return s.artists[offset:end], nil
}

func (s *organizeRunnerStore) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error) {
	return s.albums[artistID], nil
}

func (s *organizeRunnerStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return s.tracks[albumID], nil
}

func (s *organizeRunnerStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return s.allTracks, nil
}

func (s *organizeRunnerStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) {
	for i := range s.allTracks {
		if s.allTracks[i].ID == t.ID {
			s.allTracks[i] = *t
			return t.ID, nil
		}
	}
	s.allTracks = append(s.allTracks, *t)
	return t.ID, nil
}

func (s *organizeRunnerStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	for i := range s.artists {
		if s.artists[i].ID == id {
			return &s.artists[i], nil
		}
	}
	return nil, nil
}

func (s *organizeRunnerStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	var out []domain.Track
	for _, albums := range s.albums {
		for _, a := range albums {
			if a.ArtistID == artistID {
				out = append(out, s.tracks[a.ID]...)
			}
		}
	}
	return out, nil
}

func (s *organizeRunnerStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	for _, albums := range s.albums {
		for _, a := range albums {
			if a.ID == id {
				cp := a
				return &cp, nil
			}
		}
	}
	return nil, nil
}

// albumlessStore is organizeRunnerStore with GetAlbum stubbed to always miss,
// exercising the runner's album-lookup failure path.
type albumlessStore struct {
	*organizeRunnerStore
}

func (*albumlessStore) GetAlbum(context.Context, int64) (*domain.Album, error) {
	return nil, nil
}

func newRunners(cfg *config.Persistence, store library.Store) *Runners {
	return NewRunners(RunnerDeps{
		Log:    testLogger(),
		Store:  store,
		Config: func() config.Config { return cfg.Get() },
	})
}
