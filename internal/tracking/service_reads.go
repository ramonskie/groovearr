package tracking

import (
	"context"
	"fmt"

	"github.com/ramonskie/groovearr/internal/domain"
)

// GetTrackedArtist returns the tracked artist with the given ID, or (nil, nil)
// when no such artist exists. It delegates to the store so API handlers never
// touch persistence directly (AGENTS §4) and forms part of the
// jobs.TrackedRefresher contract: single-artist runners resolve an artist's
// provider through the service instead of reaching into the store (AGENTS §3).
// Store failures are wrapped with the same "get tracked artist %d" context the
// other service paths use (AGENTS §10).
func (s *Service) GetTrackedArtist(ctx context.Context, id int64) (*domain.TrackedArtist, error) {
	artist, err := s.store.GetTrackedArtist(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %d: %w", id, err)
	}
	return artist, nil
}

// ListAlbums returns every album discovered for a tracked artist. It delegates
// to the store and keeps the read path behind the service (AGENTS §4). An
// artist with no discovered albums yields an empty slice and a nil error;
// store failures are wrapped with "list tracked albums %d" context.
func (s *Service) ListAlbums(ctx context.Context, artistID int64) ([]domain.TrackedAlbum, error) {
	albums, err := s.store.ListTrackedAlbums(ctx, artistID)
	if err != nil {
		return nil, fmt.Errorf("list tracked albums %d: %w", artistID, err)
	}
	return albums, nil
}

// ListAllWanted returns the albums eligible for acquisition across every
// tracked artist (monitored, status wanted or downloading). It is the global
// counterpart of ListWanted and delegates to the store so the API handlers
// never touch persistence directly (AGENTS §4). An empty result is a nil
// slice; store failures are wrapped with "list wanted albums" context.
func (s *Service) ListAllWanted(ctx context.Context) ([]domain.TrackedAlbum, error) {
	albums, err := s.store.ListWantedAlbums(ctx)
	if err != nil {
		return nil, fmt.Errorf("list wanted albums: %w", err)
	}
	return albums, nil
}
