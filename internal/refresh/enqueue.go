package refresh

import (
	"context"
	"fmt"

	"github.com/ramonskie/groovearr/internal/catalog"
)

// EnqueueAlbum queues one album for background metadata refresh.
//
// It is best-effort by contract: hot paths (album import, scan, merge) should
// log and continue on a returned error rather than fail their own operation.
// The enqueue is a single short queue-table write; it never blocks on a
// provider.
func (s *Service) EnqueueAlbum(ctx context.Context, albumID int64, reason string, priority int) error {
	if albumID <= 0 {
		return fmt.Errorf("refresh: enqueue album: invalid album id %d", albumID)
	}
	return s.enqueue(ctx, catalog.EntityAlbum, albumID, reason, priority)
}

// EnqueueArtist queues one artist for background metadata refresh. Best-effort,
// same contract as EnqueueAlbum.
func (s *Service) EnqueueArtist(ctx context.Context, artistID int64, reason string, priority int) error {
	if artistID <= 0 {
		return fmt.Errorf("refresh: enqueue artist: invalid artist id %d", artistID)
	}
	return s.enqueue(ctx, catalog.EntityArtist, artistID, reason, priority)
}

// enqueue writes one queue row. A nil service or store is a no-op so callers
// can depend on the helper without a nil guard.
func (s *Service) enqueue(ctx context.Context, entityType catalog.EntityType, entityID int64, reason string, priority int) error {
	if s == nil || s.store == nil {
		return nil
	}
	now := s.now()
	_, err := s.store.EnqueueRefresh(ctx, catalog.RefreshItem{
		EntityType: entityType,
		EntityID:   entityID,
		Reason:     reason,
		Priority:   priority,
		DueAt:      now,
		CreatedAt:  now,
	})
	if err != nil {
		if s.log != nil {
			s.log.Warn("refresh enqueue failed",
				"entity_type", entityType, "entity_id", entityID, "reason", reason,
				"error", err, "component", "refresh")
		}
		return fmt.Errorf("refresh: enqueue %s %d: %w", entityType, entityID, err)
	}
	return nil
}
