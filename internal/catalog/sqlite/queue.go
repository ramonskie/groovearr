package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
)

// Store now implements the full catalog.Store surface: the materialized album
// track / metadata / artist sub-stores (store.go) plus the persisted refresh
// queue below. This assertion is the compile-time guard that a future interface
// change cannot silently drop a method.
var _ catalog.Store = (*Store)(nil)

// ─── Refresh queue ───────────────────────────────────────────────────
//
// Debounce rule (EnqueueRefresh): a re-enqueue of an existing
// (entity_type, entity_id) row updates the reason, raises the priority to
// MAX(existing, new), and moves due_at to the new value. It deliberately does
// NOT touch attempts or last_error: a row that is mid-backoff keeps its failure
// count, so re-arming it from a lazy read or an event does not restart the
// exponential backoff from scratch. A new row is inserted with attempts=0.

// EnqueueRefresh adds or re-arms an entity in the queue and returns the row id.
// The upsert is debounced on the UNIQUE(entity_type, entity_id) guard so the
// same entity is never queued twice while an open row exists. See the debounce
// rule above.
func (s *Store) EnqueueRefresh(ctx context.Context, item catalog.RefreshItem) (int64, error) {
	if !item.EntityType.Valid() {
		return 0, fmt.Errorf("catalog sqlite: enqueue refresh: invalid entity type %q", item.EntityType)
	}
	if item.EntityID <= 0 {
		return 0, fmt.Errorf("catalog sqlite: enqueue refresh: invalid entity id %d", item.EntityID)
	}
	priority := item.Priority
	if priority == 0 {
		priority = catalog.PriorityDefault
	}

	now := time.Now().UTC()
	due := orNow(item.DueAt, now)
	created := orNow(item.CreatedAt, now)

	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO metadata_refresh_queue
			(entity_type, entity_id, reason, priority, due_at, attempts, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, 0, '', ?)
		ON CONFLICT(entity_type, entity_id) DO UPDATE SET
			reason   = excluded.reason,
			priority = MAX(priority, excluded.priority),
			due_at   = excluded.due_at
		RETURNING id`,
		string(item.EntityType), item.EntityID, item.Reason, priority,
		fmtTime(due), fmtTime(created)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("catalog sqlite: enqueue refresh %s/%d: %w", item.EntityType, item.EntityID, err)
	}
	return id, nil
}

// ClaimDueRefresh returns up to limit items whose due_at <= now, highest
// priority first and earliest due first within a priority. It is read-only:
// claiming does not mutate rows, so a crashed worker simply re-claims on the
// next tick. A non-positive limit yields an empty slice, not an error.
func (s *Store) ClaimDueRefresh(ctx context.Context, now time.Time, limit int) ([]catalog.RefreshItem, error) {
	if limit <= 0 {
		return []catalog.RefreshItem{}, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, entity_type, entity_id, reason, priority, due_at, attempts,
			last_error, created_at
		FROM metadata_refresh_queue
		WHERE due_at <= ?
		ORDER BY priority DESC, due_at ASC
		LIMIT ?`, fmtTime(now.UTC()), limit)
	if err != nil {
		return nil, fmt.Errorf("catalog sqlite: claim due refresh: %w", err)
	}
	defer rows.Close()

	out := []catalog.RefreshItem{}
	for rows.Next() {
		var it catalog.RefreshItem
		var entityType, dueAt, createdAt string
		if err := rows.Scan(&it.ID, &entityType, &it.EntityID, &it.Reason,
			&it.Priority, &dueAt, &it.Attempts, &it.LastError, &createdAt); err != nil {
			return nil, fmt.Errorf("catalog sqlite: scan refresh item: %w", err)
		}
		it.EntityType = catalog.EntityType(entityType)
		it.DueAt = parseTime(dueAt)
		it.CreatedAt = parseTime(createdAt)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog sqlite: iterate refresh queue: %w", err)
	}
	return out, nil
}

// FinishRefresh removes a claimed item. The terminal status is not stored here
// — the caller records it on the entity row (album_metadata/artist_metadata) —
// but it is logged so queue drainage is observable. Removing rather than
// flagging keeps the queue small and Claimed rows cannot accumulate.
func (s *Store) FinishRefresh(ctx context.Context, id int64, status catalog.RefreshStatus) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM metadata_refresh_queue WHERE id=?`, id); err != nil {
		return fmt.Errorf("catalog sqlite: finish refresh %d: %w", id, err)
	}
	if s.log != nil {
		s.log.Debug("catalog refresh finished", "queue_id", id, "status", string(status))
	}
	return nil
}

// RetryRefresh records a failed attempt and reschedules the item for
// nextAttempt. Callers compute nextAttempt with catalog.BackoffFor(attempts) so
// the schedule stays deterministic and survives a restart.
func (s *Store) RetryRefresh(ctx context.Context, id int64, attempts int, nextAttempt time.Time, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE metadata_refresh_queue
		SET attempts=?, due_at=?, last_error=?
		WHERE id=?`,
		attempts, fmtTime(orNow(nextAttempt, time.Now().UTC())), lastErr, id)
	if err != nil {
		return fmt.Errorf("catalog sqlite: retry refresh %d: %w", id, err)
	}
	return nil
}

// DeleteRefresh removes an item from the queue. Deleting a row that is absent
// is a no-op, not an error.
func (s *Store) DeleteRefresh(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM metadata_refresh_queue WHERE id=?`, id); err != nil {
		return fmt.Errorf("catalog sqlite: delete refresh %d: %w", id, err)
	}
	return nil
}

// BumpRefreshPriority raises an existing item's priority to at least priority
// (MAX(existing, priority)). It is a no-op when the entity is not queued; it
// does not touch due_at, so a bumped item still respects its schedule.
func (s *Store) BumpRefreshPriority(ctx context.Context, entityType catalog.EntityType, entityID int64, priority int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE metadata_refresh_queue
		SET priority = MAX(priority, ?)
		WHERE entity_type=? AND entity_id=?`,
		priority, string(entityType), entityID)
	if err != nil {
		return fmt.Errorf("catalog sqlite: bump refresh priority %s/%d: %w", entityType, entityID, err)
	}
	return nil
}

// CountQueuedRefresh returns how many items are currently queued.
func (s *Store) CountQueuedRefresh(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM metadata_refresh_queue`).Scan(&n); err != nil {
		return 0, fmt.Errorf("catalog sqlite: count refresh queue: %w", err)
	}
	return n, nil
}
