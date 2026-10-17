package catalog

import (
	"context"
	"time"
)

// QueueStats is a cheap, read-only snapshot of the refresh queue.
type QueueStats struct {
	// Queued is the total number of open queue rows.
	Queued int
	// DueNow is how many rows are due at the snapshot time (due_at <= now).
	DueNow int
	// Scheduled is how many rows wait in the future (backoff or the stale TTL).
	Scheduled int
}

// UnmatchedAlbum is one parked album whose metadata could not be matched to a
// provider. It carries the joined library artist/title so a read-only listing
// does not need a second library query per row.
type UnmatchedAlbum struct {
	AlbumID   int64
	ArtistID  int64
	Artist    string
	Title     string
	Status    RefreshStatus
	Error     string
	UpdatedAt time.Time
}

// StatsStore is the read-only observability surface over the catalog: queue
// depth, materialized-entity counts, and the parked-unmatched listing used by
// the settings status card and its diagnostic endpoint.
//
// It is deliberately separate from Store so exposing metrics never widens the
// interfaces every catalog consumer embeds (AGENTS §3). The same
// *catalog/sqlite.Store implements both.
type StatsStore interface {
	// QueueStats returns queue depth as of now. A zero now means time.Now.
	QueueStats(ctx context.Context, now time.Time) (QueueStats, error)
	// AlbumStatusCounts returns how many album_metadata rows carry each refresh
	// status. A status with no rows is absent from the map.
	AlbumStatusCounts(ctx context.Context) (map[RefreshStatus]int, error)
	// CountArtistMetadata returns how many artist overviews are materialized.
	CountArtistMetadata(ctx context.Context) (int, error)
	// ListUnmatchedAlbums returns parked album metadata rows joined with their
	// library artist/title, ordered by title then album id, capped at limit.
	// limit <= 0 returns an empty slice.
	ListUnmatchedAlbums(ctx context.Context, limit int) ([]UnmatchedAlbum, error)
}
