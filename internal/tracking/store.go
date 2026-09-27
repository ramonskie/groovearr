// Package tracking provides persistence for artist-release monitoring:
// tracked artists, the discography discovered for each, and the acquisition
// status of every discovered album.
package tracking

import (
	"context"

	"github.com/ramonskie/groovearr/internal/domain"
)

// Store is the interface for artist-tracking persistence.
//
// The provider pair (ProviderName, ProviderArtistID) on an artist and
// (ProviderName, ProviderAlbumID) on an album is the external identity used
// for upserts; internal IDs are assigned by the store. Implementations live
// in a subpackage (internal/tracking/sqlite) and wrap failures with
// fmt.Errorf context per AGENTS §10, e.g. "get tracked artist %d: %w"; this
// package root only declares the contract.
type Store interface {
	// ─── Tracked artists ─────────────────────────────────────────────

	// CreateTrackedArtist inserts a new tracked artist and returns its ID.
	CreateTrackedArtist(ctx context.Context, a *domain.TrackedArtist) (int64, error)

	// GetTrackedArtist returns the tracked artist with the given ID, or
	// (nil, nil) when no such artist exists.
	GetTrackedArtist(ctx context.Context, id int64) (*domain.TrackedArtist, error)

	// GetTrackedArtistByProvider returns the tracked artist identified by
	// its provider pair, or (nil, nil) when no such artist exists.
	GetTrackedArtistByProvider(ctx context.Context, providerName, providerArtistID string) (*domain.TrackedArtist, error)

	// ListTrackedArtists returns all tracked artists.
	ListTrackedArtists(ctx context.Context) ([]domain.TrackedArtist, error)

	// UpdateArtistMonitor sets the monitored flag and monitor mode for the
	// tracked artist with the given ID.
	UpdateArtistMonitor(ctx context.Context, id int64, monitored bool, mode domain.MonitorMode) error

	// UpdateArtistLibraryLink records that the tracked artist now maps to a
	// local library artist by persisting library_artist_id.
	UpdateArtistLibraryLink(ctx context.Context, artistID int64, libraryArtistID int64) error

	// UpdateArtistName persists a corrected provider-supplied display name for
	// an existing tracked artist, refreshing updated_at. It touches only the
	// name: monitor settings, the library link, and auto_refresh survive.
	UpdateArtistName(ctx context.Context, artistID int64, name string) error

	// DeleteTrackedArtist removes a tracked artist and its discovered
	// albums (via ON DELETE CASCADE).
	DeleteTrackedArtist(ctx context.Context, id int64) error

	// ─── Tracked albums ──────────────────────────────────────────────

	// UpsertTrackedAlbum inserts or updates the album identified by its
	// owning artist plus provider album ID, returning its ID. The
	// LastSeenAt field is refreshed on every upsert; a non-nil incoming
	// LibraryAlbumID is persisted and a nil incoming link never clears an
	// existing one (COALESCE), so a transient match miss keeps the link.
	UpsertTrackedAlbum(ctx context.Context, a *domain.TrackedAlbum) (int64, error)

	// GetTrackedAlbumByProvider returns the album for the given tracked
	// artist and provider album ID, or (nil, nil) when no such album exists.
	GetTrackedAlbumByProvider(ctx context.Context, artistID int64, providerAlbumID string) (*domain.TrackedAlbum, error)

	// ListTrackedAlbums returns every album discovered for the given
	// tracked artist.
	ListTrackedAlbums(ctx context.Context, artistID int64) ([]domain.TrackedAlbum, error)

	// ListWantedAlbums returns the albums eligible for acquisition across
	// all artists: monitored albums whose status is wanted or downloading.
	// Unmonitored, ignored, and already-downloaded albums are excluded.
	ListWantedAlbums(ctx context.Context) ([]domain.TrackedAlbum, error)

	// UpdateAlbumMonitor sets the monitored flag for a single album.
	UpdateAlbumMonitor(ctx context.Context, albumID int64, monitored bool) error

	// MarkAlbumStatus advances an album's acquisition lifecycle status.
	MarkAlbumStatus(ctx context.Context, albumID int64, status domain.AlbumStatus) error

	// MarkAlbumSearched records that a SearchMissing pass processed the
	// album, regardless of outcome, by setting last_searched_at=now. It is
	// what makes a capped batch rotate instead of re-picking a stalled album.
	MarkAlbumSearched(ctx context.Context, albumID int64) error

	// GetTrackedAlbum returns the album with the given internal ID, or
	// (nil, nil) when no such album exists.
	GetTrackedAlbum(ctx context.Context, albumID int64) (*domain.TrackedAlbum, error)

	// TouchArtistRefreshed records that the artist's discography was just
	// reconciled (updates last_refreshed_at).
	TouchArtistRefreshed(ctx context.Context, artistID int64) error
}
