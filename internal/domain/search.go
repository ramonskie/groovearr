package domain

import "github.com/ramonskie/groovearr/internal/quality"

// TrackMetadata holds parsed metadata for a track. Populated when the provider
// can extract metadata from the search result (e.g. filename parsing for Soulseek,
// structured API response for Deezer/Spotify). Nil when unavailable.
type TrackMetadata struct {
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Title    string `json:"title,omitempty"`
	Year     int    `json:"year,omitempty"`
	CoverURL string `json:"cover_url,omitempty"`
}

// SearchResult is the base type returned by download source searches.
type SearchResult struct {
	Username        string              `json:"username"`          // source name or slskd peer
	Filename        string              `json:"filename"`          // source-specific encoding
	Size            int64               `json:"size"`              // bytes
	Bitrate         int                 `json:"bitrate,omitempty"` // kbps (legacy — use AudioQuality.Bitrate)
	Duration        int64               `json:"duration,omitempty"` // milliseconds
	Quality         string              `json:"quality"`           // flac, mp3, ogg, aac, wma (legacy — use AudioQuality.Format)
	AudioQuality    quality.AudioQuality `json:"audio_quality"`
	FreeUploadSlots int                 `json:"free_upload_slots"`
	UploadSpeed     int64               `json:"upload_speed"` // bytes/sec
	QueueLength     int                 `json:"queue_length"`
}

// TrackResult is an individual track search hit.
type TrackResult struct {
	SearchResult
	Artist      string          `json:"artist,omitempty"`
	Title       string          `json:"title,omitempty"`
	Album       string          `json:"album,omitempty"`
	TrackNumber int             `json:"track_number,omitempty"`
	CoverURL    string          `json:"cover_url,omitempty"`   // album cover image URL (source-agnostic)
	Metadata    *TrackMetadata  `json:"metadata,omitempty"`    // structured metadata (nil for providers that don't support it)
}

// AlbumResult is an album-level search hit containing multiple tracks.
type AlbumResult struct {
	Username        string        `json:"username"`
	AlbumPath       string        `json:"album_path"`
	AlbumTitle      string        `json:"album_title"`
	Artist          string        `json:"artist,omitempty"`
	TrackCount      int           `json:"track_count"`
	TotalSize       int64         `json:"total_size"`
	Tracks          []TrackResult `json:"tracks"`
	DominantQuality string        `json:"dominant_quality"`
	Year            string        `json:"year,omitempty"`
	FreeUploadSlots int           `json:"free_upload_slots"`
	UploadSpeed     int64         `json:"upload_speed"`
	QueueLength     int           `json:"queue_length"`
}
