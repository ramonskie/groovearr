package catalog

import (
	"context"
	"time"
)

// Store is the durable metadata catalog: materialized album tracklists, album
// and artist provider links/bookkeeping, artist discography, and the persisted
// refresh work queue.
//
// Implementations live in internal/catalog/sqlite and share the library's
// single SQLite connection. "Not found" lookups return (nil, nil), never an
// error. All mutations use short per-entity transactions so readers are never
// starved by the single-writer connection.
type Store interface {
	AlbumTrackStore
	AlbumMetadataStore
	ArtistMetadataStore
	ArtistDiscographyStore
	RefreshQueue
}

// AlbumTrackStore stores materialized expected-track rows per album.
type AlbumTrackStore interface {
	// ReplaceAlbumTracks atomically replaces every track row for an album in a
	// single transaction (delete-then-insert).
	ReplaceAlbumTracks(ctx context.Context, albumID int64, tracks []AlbumTrack) error
	// GetAlbumTracks returns the materialized rows in provider order
	// (disc_number, track_number). Returns an empty slice, not an error, when
	// the album has no materialized rows.
	GetAlbumTracks(ctx context.Context, albumID int64) ([]AlbumTrack, error)
	// DeleteAlbumTracks removes all materialized rows for an album.
	DeleteAlbumTracks(ctx context.Context, albumID int64) error
	// CountAlbumTracks returns how many materialized rows an album has.
	CountAlbumTracks(ctx context.Context, albumID int64) (int, error)
}

// AlbumMetadataStore stores the provider link and refresh bookkeeping per album.
type AlbumMetadataStore interface {
	// UpsertAlbumMetadata inserts or updates the metadata row for one album.
	UpsertAlbumMetadata(ctx context.Context, m AlbumMetadata) error
	// GetAlbumMetadata returns the row, or (nil, nil) when absent.
	GetAlbumMetadata(ctx context.Context, albumID int64) (*AlbumMetadata, error)
	// ListAlbumsDueForRefresh returns album ids whose materialized metadata is
	// older than olderThan (or never materialized), ordered by last access
	// (most recent first), capped at limit.
	ListAlbumsDueForRefresh(ctx context.Context, olderThan time.Time, limit int) ([]int64, error)
	// TouchAlbumAccessed records a read so the refresh order favors hot albums.
	TouchAlbumAccessed(ctx context.Context, albumID int64, at time.Time) error
}

// ArtistMetadataStore stores the durable artist overview per artist.
type ArtistMetadataStore interface {
	// UpsertArtistMetadata inserts or updates the overview row for one artist.
	UpsertArtistMetadata(ctx context.Context, m ArtistMetadata) error
	// GetArtistMetadata returns the row, or (nil, nil) when absent.
	GetArtistMetadata(ctx context.Context, artistID int64) (*ArtistMetadata, error)
}

// ArtistDiscographyStore stores discovered releases per artist.
type ArtistDiscographyStore interface {
	// ReplaceArtistDiscography atomically replaces an artist's discovered
	// releases in a single transaction.
	ReplaceArtistDiscography(ctx context.Context, artistID int64, releases []ArtistDiscographyRelease) error
	// GetArtistDiscography returns an artist's discovered releases ordered by
	// year then title. Returns an empty slice, not an error, when absent.
	GetArtistDiscography(ctx context.Context, artistID int64) ([]ArtistDiscographyRelease, error)
}

// RefreshQueue is the persisted, resumable refresh work queue. It makes refresh
// survive restarts, supports priority (user-open bumps), exponential backoff,
// and a terminal state so the queue drains instead of churning.
type RefreshQueue interface {
	// EnqueueRefresh adds or re-arms an entity. Re-enqueuing the same
	// (entity_type, entity_id) updates priority/due_at rather than duplicating.
	// Returns the queue row id.
	EnqueueRefresh(ctx context.Context, item RefreshItem) (int64, error)
	// ClaimDueRefresh returns up to limit items whose due_at <= now, ordered by
	// priority (desc) then due_at (asc). Claiming does not mutate rows; the
	// caller reports outcome via FinishRefresh/RetryRefresh.
	ClaimDueRefresh(ctx context.Context, now time.Time, limit int) ([]RefreshItem, error)
	// FinishRefresh marks a claimed item done with a terminal outcome and
	// removes it from the queue.
	FinishRefresh(ctx context.Context, id int64, status RefreshStatus) error
	// RetryRefresh records a failed attempt and reschedules it for nextAttempt.
	RetryRefresh(ctx context.Context, id int64, attempts int, nextAttempt time.Time, lastErr string) error
	// DeleteRefresh removes an item from the queue.
	DeleteRefresh(ctx context.Context, id int64) error
	// BumpRefreshPriority raises an existing item's priority (a no-op if the
	// entity is not queued).
	BumpRefreshPriority(ctx context.Context, entityType EntityType, entityID int64, priority int) error
	// CountQueuedRefresh returns how many items are currently queued.
	CountQueuedRefresh(ctx context.Context) (int, error)
	// ReArmTerminalAlbums makes every terminal (unmatched/failed) album due for a
	// refresh so the engine re-searches it, bypassing the terminal parks (the
	// 90-day UnmatchedRetry due_at and the failed-album row removal). It is the
	// explicit, user-triggered counterpart to those parks: a parked unmatched row
	// is moved to due-now, and a failed album whose row was removed gets a fresh
	// one. Non-terminal albums are never touched. limit caps the rows (limit <= 0
	// means no cap). Returns the number of albums re-armed.
	ReArmTerminalAlbums(ctx context.Context, now time.Time, limit int) (int, error)
}
