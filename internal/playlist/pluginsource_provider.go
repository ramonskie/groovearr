package playlist

import (
	"github.com/ramonskie/groovearr/internal/download"
)

// PlaylistSourceProvider is an optional interface for download plugins that can
// provide playlist functionality (e.g. Deezer user playlists, Spotify). Plugins
// implementing this will have their playlist source auto-registered.
//
// Moved to playlist package to avoid import cycle: playlist already depends on
// download (via service.go), so adding the interface here creates no new edges.
type PlaylistSourceProvider interface {
	download.Plugin

	// PlaylistSource returns a playlist.Source for this plugin.
	PlaylistSource() Source
}
