package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
)

// The observability surface is implemented by the same store. This assertion is
// the compile-time guard that a future change cannot silently drop a method.
var _ catalog.StatsStore = (*Store)(nil)

// QueueStats returns queue depth as of now: total open rows, how many are due,
// and how many wait in the future. Backoff and the stale TTL both live entirely
// in due_at, so "scheduled" is exactly the rows whose due_at is in the future.
func (s *Store) QueueStats(ctx context.Context, now time.Time) (catalog.QueueStats, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var st catalog.QueueStats
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN due_at <= ? THEN 1 ELSE 0 END), 0)
		FROM metadata_refresh_queue`, fmtTime(now.UTC())).
		Scan(&st.Queued, &st.DueNow)
	if err != nil {
		return catalog.QueueStats{}, fmt.Errorf("catalog sqlite: queue stats: %w", err)
	}
	st.Scheduled = st.Queued - st.DueNow
	return st, nil
}

// AlbumStatusCounts returns how many album_metadata rows carry each refresh
// status, keyed by status. A status with no rows is absent.
func (s *Store) AlbumStatusCounts(ctx context.Context) (map[catalog.RefreshStatus]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT refresh_status, COUNT(*) FROM album_metadata GROUP BY refresh_status`)
	if err != nil {
		return nil, fmt.Errorf("catalog sqlite: album status counts: %w", err)
	}
	defer rows.Close()

	out := map[catalog.RefreshStatus]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("catalog sqlite: scan album status count: %w", err)
		}
		out[catalog.RefreshStatus(status)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog sqlite: iterate album status counts: %w", err)
	}
	return out, nil
}

// CountArtistMetadata returns how many artist overviews are materialized.
func (s *Store) CountArtistMetadata(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM artist_metadata`).Scan(&n); err != nil {
		return 0, fmt.Errorf("catalog sqlite: count artist metadata: %w", err)
	}
	return n, nil
}
