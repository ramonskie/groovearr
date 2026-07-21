// Package spotify implements the Spotify Web API client types.
// Structs mirror the JSON response shapes from the Spotify Web API
// (https://developer.spotify.com/documentation/web-api/).
package spotify

// ─── Shared / Primitives ─────────────────────────────────────────────

// ExternalURLs contains known external URLs for a Spotify entity.
type ExternalURLs struct {
	Spotify string `json:"spotify"`
}

// ExternalIDs contains known external IDs (ISRC, EAN, UPC) for a track or album.
type ExternalIDs struct {
	ISRC string `json:"isrc,omitempty"`
	EAN  string `json:"ean,omitempty"`
	UPC  string `json:"upc,omitempty"`
}

// Followers contains follower count information for an artist or playlist.
type Followers struct {
	Href  *string `json:"href"` // always null in Spotify responses
	Total int     `json:"total"`
}

// Image represents a cover-art or profile image.
type Image struct {
	URL    string `json:"url"`
	Height *int   `json:"height"` // nullable
	Width  *int   `json:"width"`  // nullable
}

// Restrictions describes why a track or album may not be available.
type Restrictions struct {
	Reason string `json:"reason"` // "market", "product", or "explicit"
}

// Copyright represents a copyright statement on an album.
type Copyright struct {
	Text string `json:"text"`
	Type string `json:"type"` // "C" or "P"
}

// ─── User ────────────────────────────────────────────────────────────

// User represents a Spotify user profile (playlist owner, added_by).
type User struct {
	ExternalURLs ExternalURLs `json:"external_urls"`
	Href         string       `json:"href"`
	ID           string       `json:"id"`
	Type         string       `json:"type"`
	URI          string       `json:"uri"`
	DisplayName  *string      `json:"display_name"` // nullable
}

// ─── Artist ──────────────────────────────────────────────────────────

// SimplifiedArtist is a minimal artist representation used in track,
// album, and playlist responses.
type SimplifiedArtist struct {
	ExternalURLs ExternalURLs `json:"external_urls"`
	Href         string       `json:"href"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Type         string       `json:"type"`
	URI          string       `json:"uri"`
}

// Artist is the full artist object returned by /v1/artists/{id}.
type Artist struct {
	ExternalURLs ExternalURLs       `json:"external_urls"`
	Followers    Followers          `json:"followers"`
	Genres       []string           `json:"genres"`
	Href         string             `json:"href"`
	ID           string             `json:"id"`
	Images       []Image            `json:"images"`
	Name         string             `json:"name"`
	Popularity   int                `json:"popularity"`
	Type         string             `json:"type"`
	URI          string             `json:"uri"`
}

// ─── Album ───────────────────────────────────────────────────────────

// SimplifiedAlbum is a minimal album representation used in search
// results and as the album field on Track.
type SimplifiedAlbum struct {
	AlbumType            string             `json:"album_type"`
	TotalTracks          int                `json:"total_tracks"`
	ExternalURLs         ExternalURLs       `json:"external_urls"`
	Href                 string             `json:"href"`
	ID                   string             `json:"id"`
	Images               []Image            `json:"images"`
	Name                 string             `json:"name"`
	ReleaseDate          string             `json:"release_date"`
	ReleaseDatePrecision string             `json:"release_date_precision"`
	Restrictions         *Restrictions      `json:"restrictions,omitempty"`
	Type                 string             `json:"type"`
	URI                  string             `json:"uri"`
	Artists              []SimplifiedArtist `json:"artists"`
}

// Album is the full album object returned by /v1/albums/{id}.
type Album struct {
	AlbumType            string                 `json:"album_type"`
	TotalTracks          int                    `json:"total_tracks"`
	ExternalURLs         ExternalURLs           `json:"external_urls"`
	Href                 string                 `json:"href"`
	ID                   string                 `json:"id"`
	Images               []Image                `json:"images"`
	Name                 string                 `json:"name"`
	ReleaseDate          string                 `json:"release_date"`
	ReleaseDatePrecision string                 `json:"release_date_precision"`
	Restrictions         *Restrictions          `json:"restrictions,omitempty"`
	Type                 string                 `json:"type"`
	URI                  string                 `json:"uri"`
	Artists              []SimplifiedArtist     `json:"artists"`
	Tracks               Paging[SimplifiedTrack] `json:"tracks"`
	Copyrights           []Copyright            `json:"copyrights"`
	ExternalIDs          ExternalIDs            `json:"external_ids"`
	Genres               []string               `json:"genres"`
	Label                string                 `json:"label"`
	Popularity           int                    `json:"popularity"`
}

// ─── Track ───────────────────────────────────────────────────────────

// SimplifiedTrack is a minimal track representation used in album.tracks
// and other nested track lists.
type SimplifiedTrack struct {
	Artists      []SimplifiedArtist `json:"artists"`
	DiscNumber   int                `json:"disc_number"`
	DurationMs   int                `json:"duration_ms"`
	Explicit     bool               `json:"explicit"`
	ExternalURLs ExternalURLs       `json:"external_urls"`
	Href         string             `json:"href"`
	ID           string             `json:"id"`
	IsPlayable   *bool              `json:"is_playable,omitempty"`
	Restrictions *Restrictions      `json:"restrictions,omitempty"`
	Name         string             `json:"name"`
	PreviewURL   *string            `json:"preview_url"`
	TrackNumber  int                `json:"track_number"`
	Type         string             `json:"type"`
	URI          string             `json:"uri"`
	IsLocal      bool               `json:"is_local"`
}

// Track is the full track object returned by /v1/tracks/{id}.
type Track struct {
	Album        SimplifiedAlbum    `json:"album"`
	Artists      []SimplifiedArtist `json:"artists"`
	DiscNumber   int                `json:"disc_number"`
	DurationMs   int                `json:"duration_ms"`
	Explicit     bool               `json:"explicit"`
	ExternalIDs  ExternalIDs        `json:"external_ids"`
	ExternalURLs ExternalURLs       `json:"external_urls"`
	Href         string             `json:"href"`
	ID           string             `json:"id"`
	IsPlayable   *bool              `json:"is_playable,omitempty"`
	Restrictions *Restrictions      `json:"restrictions,omitempty"`
	Name         string             `json:"name"`
	Popularity   int                `json:"popularity"`
	PreviewURL   *string            `json:"preview_url"`
	TrackNumber  int                `json:"track_number"`
	Type         string             `json:"type"`
	URI          string             `json:"uri"`
	IsLocal      bool               `json:"is_local"`
}

// ─── Playlist ────────────────────────────────────────────────────────

// PlaylistTracksRef is a lightweight reference to a playlist's tracks
// (href + total). Used in SimplifiedPlaylist for listings where the
// full track list is not included.
type PlaylistTracksRef struct {
	Href  string `json:"href"`
	Total int    `json:"total"`
}

// PlaylistTrack is a single item in a playlist's tracks array.
type PlaylistTrack struct {
	AddedAt *string `json:"added_at"`  // nullable for old playlists
	AddedBy *User   `json:"added_by"`  // nullable for old playlists
	IsLocal bool    `json:"is_local"`
	Track   *Track  `json:"track"`
}

// SimplifiedPlaylist is a minimal playlist representation used in
// search results and user playlists list.
type SimplifiedPlaylist struct {
	Collaborative bool               `json:"collaborative"`
	Description   *string            `json:"description"` // nullable
	ExternalURLs  ExternalURLs       `json:"external_urls"`
	Href          string             `json:"href"`
	ID            string             `json:"id"`
	Images        []Image            `json:"images"`
	Name          string             `json:"name"`
	Owner         User               `json:"owner"`
	Public        *bool              `json:"public"` // nullable
	SnapshotID    string             `json:"snapshot_id"`
	Tracks        *PlaylistTracksRef `json:"tracks,omitempty"`
	Type          string             `json:"type"`
	URI           string             `json:"uri"`
}

// Playlist is the full playlist object returned by /v1/playlists/{id}.
type Playlist struct {
	Collaborative bool                  `json:"collaborative"`
	Description   *string               `json:"description"` // nullable
	ExternalURLs  ExternalURLs          `json:"external_urls"`
	Followers     Followers             `json:"followers"`
	Href          string                `json:"href"`
	ID            string                `json:"id"`
	Images        []Image               `json:"images"`
	Name          string                `json:"name"`
	Owner         User                  `json:"owner"`
	Public        *bool                 `json:"public"` // nullable
	SnapshotID    string                `json:"snapshot_id"`
	Tracks        Paging[PlaylistTrack] `json:"tracks"`
	Type          string                `json:"type"`
	URI           string                `json:"uri"`
}

// ─── Paging ──────────────────────────────────────────────────────────

// Paging is the generic paginated response wrapper used throughout the
// Spotify Web API. T is the item type (Track, Album, PlaylistTrack, etc.).
type Paging[T any] struct {
	Href     string `json:"href"`
	Limit    int    `json:"limit"`
	Next     string `json:"next"`
	Offset   int    `json:"offset"`
	Previous string `json:"previous"`
	Total    int    `json:"total"`
	Items    []T    `json:"items"`
}
