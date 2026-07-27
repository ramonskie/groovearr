package domain

// SyncMode controls how playlist folder sync handles removed tracks.
type SyncMode string

const (
	SyncModeMirror  SyncMode = "mirror"  // delete local files when removed from upstream
	SyncModeAppend  SyncMode = "append"  // keep local files even if removed from upstream
)

// Playlist represents an imported playlist from a music source.
type Playlist struct {
	ID               int64    `json:"id"`
	Source           string   `json:"source"`             // "deezer", "spotify", "tidal"
	SourcePlaylistID string   `json:"source_playlist_id"` // source-specific ID
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	TrackCount       int      `json:"track_count"`
	CoverURL         string   `json:"cover_url,omitempty"`
	OwnerName        string   `json:"owner_name,omitempty"`
	IsPublic         bool     `json:"is_public"`
	SyncedAt         string   `json:"synced_at,omitempty"`
	AutoSync         bool     `json:"auto_sync"`
	SyncMode         SyncMode `json:"sync_mode"` // "mirror" or "append" (default "mirror")
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

// PlaylistTrack is a single track within an imported playlist.
type PlaylistTrack struct {
	PlaylistID    int64  `json:"playlist_id"`
	Position      int    `json:"position"`
	TrackID       *int64 `json:"track_id,omitempty"` // NULL if not yet in library
	SourceTrackID string `json:"source_track_id"`    // source-specific track ID
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	Album         string `json:"album,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
	ISRC          string `json:"isrc,omitempty"`
}
