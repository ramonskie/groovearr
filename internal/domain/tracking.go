package domain

import "time"

// MonitorMode controls how a tracked artist is monitored for releases.
type MonitorMode string

const (
	MonitorModeAll    MonitorMode = "all"    // monitor the full discography plus future releases
	MonitorModeFuture MonitorMode = "future" // monitor future releases only
	MonitorModeNone   MonitorMode = "none"   // track the artist without monitoring
)

// AlbumStatus tracks where a discovered album sits in the acquisition lifecycle.
type AlbumStatus string

const (
	AlbumStatusWanted      AlbumStatus = "wanted"      // known, eligible for download
	AlbumStatusDownloading AlbumStatus = "downloading" // download in progress
	AlbumStatusDownloaded  AlbumStatus = "downloaded"  // present in the library
	AlbumStatusIgnored     AlbumStatus = "ignored"     // intentionally skipped
)

// TrackedArtist is an artist monitored for releases from a provider. The
// provider pair (ProviderName, ProviderArtistID) is the external identity;
// LibraryArtistID is nil until the artist is matched to a local library entry.
type TrackedArtist struct {
	ID               int64       `json:"id"`
	Name             string      `json:"name"`
	ProviderName     string      `json:"provider_name"`
	ProviderArtistID string      `json:"provider_artist_id"`
	Monitored        bool        `json:"monitored"`
	MonitorMode      MonitorMode `json:"monitor_mode"`
	LibraryArtistID  *int64      `json:"library_artist_id,omitempty"`
	AutoRefresh      bool        `json:"auto_refresh"`
	LastRefreshedAt  *time.Time  `json:"last_refreshed_at,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

// TrackedAlbum is an album discovered for a tracked artist. LibraryAlbumID is
// nil until the album exists in the local library — the same nullable-FK
// pattern used by PlaylistTrack.TrackID.
type TrackedAlbum struct {
	ID              int64       `json:"id"`
	TrackedArtistID int64       `json:"tracked_artist_id"`
	ProviderAlbumID string      `json:"provider_album_id"`
	ProviderName    string      `json:"provider_name"`
	Title           string      `json:"title"`
	Year            int         `json:"year,omitempty"`
	AlbumType       string      `json:"album_type,omitempty"`
	Monitored       bool        `json:"monitored"`
	Status          AlbumStatus `json:"status"`
	LibraryAlbumID  *int64      `json:"library_album_id,omitempty"`
	FirstSeenAt     time.Time   `json:"first_seen_at"`
	LastSeenAt      time.Time   `json:"last_seen_at"`
	// LastSearchedAt records the last SearchMissing pass that processed this
	// album, regardless of outcome. Nil means never searched. It drives the
	// rotating batch order so a capped run never re-picks a stalled album.
	LastSearchedAt *time.Time `json:"last_searched_at,omitempty"`
}
