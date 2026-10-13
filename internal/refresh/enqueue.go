package refresh

import (
	"context"
	"errors"
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

// ReArmTerminalAlbums makes every terminal (unmatched/failed) album due for a
// refresh so the engine re-searches it on the next drain, bypassing the terminal
// parks — the 90-day UnmatchedRetry due_at (catalog.UnmatchedRetry) and the
// failed-album row removal. It is the explicit, user-triggered counterpart to
// those parks: the way to re-attempt the backlog after a matcher improvement
// without waiting out the window. limit caps how many albums are re-armed
// (0 = all). Returns the number of albums re-armed.
//
// Declared on the engine (not on the job) so provider-adjacent queue policy
// stays in this package: the job is a thin trigger, this is the rule.
func (s *Service) ReArmTerminalAlbums(ctx context.Context, limit int) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("refresh: no store")
	}
	return s.store.ReArmTerminalAlbums(ctx, s.now(), limit)
}
