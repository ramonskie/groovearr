// Package catalog provides the durable, materialized music metadata catalog and
// the persisted refresh work queue that keeps it fresh.
//
// Provider metadata (album tracklists, artist overview, discography) is stored
// as durable rows keyed by local library ids — not as a TTL cache — so warm
// reads are pure SQLite and never call a provider. Freshness is maintained by a
// paced, event-driven worker that drains the refresh queue; there is no periodic
// full-library sweep. See docs/flows/metadata-refresh.md.
//
// This package holds types and the Store interface only. The SQLite
// implementation lives in internal/catalog/sqlite. Core code depends on the
// interfaces here, never on a concrete store.
package catalog

import "time"

// TrackOrigin records where a materialized tracklist row came from.
type TrackOrigin string

const (
	// OriginLibrary marks a row derived from a file on disk (scan/import).
	OriginLibrary TrackOrigin = "library"
	// OriginProvider marks a row discovered from a metadata provider.
	OriginProvider TrackOrigin = "provider"
)

// Valid reports whether the origin is a known value.
func (o TrackOrigin) Valid() bool {
	switch o {
	case OriginLibrary, OriginProvider:
		return true
	default:
		return false
	}
}

// RefreshStatus is the lifecycle of a materialized entity's metadata.
type RefreshStatus string

const (
	// StatusPending means the entity is queued and has never been fetched.
	StatusPending RefreshStatus = "pending"
	// StatusFresh means materialized data exists and is within the soft TTL.
	StatusFresh RefreshStatus = "fresh"
	// StatusStale means materialized data exists but is older than the soft TTL.
	// Stale data is still served; a refresh is queued behind the response.
	StatusStale RefreshStatus = "stale"
	// StatusUnmatched means no provider matched the entity. It is retried on a
	// long interval so the queue drains instead of re-searching forever.
	StatusUnmatched RefreshStatus = "unmatched"
	// StatusFailed means the last refresh attempt exhausted its retries.
	StatusFailed RefreshStatus = "failed"
)

// Valid reports whether the status is a known value.
func (s RefreshStatus) Valid() bool {
	switch s {
	case StatusPending, StatusFresh, StatusStale, StatusUnmatched, StatusFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the status ends a refresh lifecycle without an
// immediate retry. Terminal statuses keep the queue from churning.
func (s RefreshStatus) IsTerminal() bool {
	return s == StatusUnmatched || s == StatusFailed
}

// EntityType identifies a refreshable entity kind.
type EntityType string

const (
	// EntityAlbum is a library album (domain.Album).
	EntityAlbum EntityType = "album"
	// EntityArtist is a library artist (domain.Artist).
	EntityArtist EntityType = "artist"
)

// Valid reports whether the entity type is a known value.
func (e EntityType) Valid() bool {
	switch e {
	case EntityAlbum, EntityArtist:
		return true
	default:
		return false
	}
}

// Enqueue priorities. Higher is drained first.
const (
	PriorityLow     = 10
	PriorityDefault = 50
	PriorityHigh    = 100
)

// Refresh reasons (free-form labels recorded on a queue row for observability).
const (
	ReasonCold   = "cold"   // first open of an unmaterialized entity
	ReasonStale  = "stale"  // lazy staleness on read
	ReasonEvent  = "event"  // import / scan / merge / tracked refresh
	ReasonBulk   = "bulk"   // opt-in "warm library" pass
	ReasonManual = "manual" // explicit user refresh
)

// AlbumTrack is one materialized expected-track row for an album. Rows from the
// library (files on disk) and from a provider coexist; Origin distinguishes
// them. The read path merges these with the live library tracks.
type AlbumTrack struct {
	AlbumID         int64
	DiscNumber      int
	TrackNumber     int
	Title           string
	ArtistName      string
	DurationMs      int64
	ISRC            string
	ProviderName    string
	ProviderTrackID string
	Origin          TrackOrigin
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AlbumMetadata is the durable provider link and refresh bookkeeping for one
// album. A stored ProviderAlbumID lets a refresh skip the fuzzy search and make
// a single lookup call.
type AlbumMetadata struct {
	AlbumID             int64
	ProviderName        string
	ProviderAlbumID     string
	RefreshStatus       RefreshStatus
	LastRefreshedAt     time.Time
	LastAccessedAt      time.Time
	ConsecutiveFailures int
	RefreshError        string
	UpdatedAt           time.Time
	// Partial marks a deliberately subsetted materialization: the provider link
	// points at a full release, but only the resolved rows (typically one track
	// from a 1-track local release) are materialized. The refresh engine never
	// expands a partial album, so a single stays a single.
	Partial bool
}

// ArtistMetadata is the durable artist overview (bio, image, genres) keyed by
// the local artist id so it survives merges and renames.
type ArtistMetadata struct {
	ArtistID            int64
	ProviderName        string
	ProviderArtistID    string
	ImageURL            string
	Genres              []string
	Summary             string
	RefreshStatus       RefreshStatus
	LastRefreshedAt     time.Time
	ConsecutiveFailures int
	RefreshError        string
	UpdatedAt           time.Time
}

// ArtistDiscographyRelease is one discovered release for an artist. It is
// display metadata only; it does not drive downloads (that is the tracking
// store's concern).
type ArtistDiscographyRelease struct {
	ArtistID        int64
	ProviderName    string
	ProviderAlbumID string
	Title           string
	Year            int
	Type            string
	CoverURL        string
	TrackCount      int
	LastSeenAt      time.Time
}

// RefreshItem is one row of the persisted refresh work queue.
type RefreshItem struct {
	ID         int64
	EntityType EntityType
	EntityID   int64
	Reason     string
	Priority   int
	DueAt      time.Time
	Attempts   int
	LastError  string
	CreatedAt  time.Time
}
