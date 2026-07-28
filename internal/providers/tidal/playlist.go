package tidal

import (
	"context"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/playlist"
)

// playlistSourceAdapter adapts Client to playlist.Source.
// Lives in the providers/tidal package to avoid circular imports.
type playlistSourceAdapter struct {
	client *Client
}

func (a *playlistSourceAdapter) Name() string        { return pluginName }
func (a *playlistSourceAdapter) DisplayName() string { return displayName }
func (a *playlistSourceAdapter) IsConfigured() bool  { return a.client.IsConfigured() }

// GetUserPlaylists fetches the authenticated user's playlists via the Tidal v2 API.
func (a *playlistSourceAdapter) GetUserPlaylists(ctx context.Context) ([]playlist.PlaylistInfo, error) {
	if !a.client.IsConfigured() {
		return nil, nil
	}
	raw, err := a.client.api.GetUserPlaylists(ctx, 50, 0)
	if err != nil {
		a.client.log.Error("tidal get user playlists failed", "error", err, "component", "tidal")
		return nil, err
	}
	out := make([]playlist.PlaylistInfo, len(raw))
	for i, p := range raw {
		name := p.Title
		if name == "" {
			name = p.Name
		}
		out[i] = playlist.PlaylistInfo{
			SourceID:    p.UUID,
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(p.Description),
			TrackCount:  p.NumTracks,
		}
	}
	return out, nil
}

// GetPlaylistTracks fetches all tracks in a Tidal playlist, plus the playlist name.
func (a *playlistSourceAdapter) GetPlaylistTracks(ctx context.Context, sourceID string) ([]playlist.TrackInfo, string, error) {
	if !a.client.IsConfigured() {
		return nil, "", nil
	}
	raw, name, err := a.client.api.GetPlaylistTracks(ctx, sourceID)
	if err != nil {
		a.client.log.Error("tidal get playlist tracks failed", "error", err, "playlistID", sourceID, "component", "tidal")
		return nil, "", err
	}
	out := make([]playlist.TrackInfo, len(raw))
	for i, item := range raw {
		t := item.Item
		durMs := int64(t.Duration) * 1000
		artist := t.Artist.Name
		out[i] = playlist.TrackInfo{
			SourceTrackID: strconv.FormatInt(t.ID, 10),
			Title:         strings.TrimSpace(t.Title),
			Artist:        strings.TrimSpace(artist),
			Album:         strings.TrimSpace(t.Album.Title),
			DurationMs:    durMs,
			ISRC:          strings.TrimSpace(t.ISRC),
		}
	}
	return out, name, nil
}
