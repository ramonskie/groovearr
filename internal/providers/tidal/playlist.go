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
	out := make([]playlist.PlaylistInfo, 0, len(raw))
	for _, p := range raw {
		// The v2 folders endpoint wraps each playlist under "data"; the
		// top-level fields are folder-item metadata (trn, name). Prefer the
		// nested playlist object for the authoritative id and title.
		uuid := p.UUID
		name := p.Name
		if name == "" {
			name = p.Title
		}
		description := p.Description
		trackCount := p.NumTracks
		if p.Data != nil {
			if p.Data.UUID != "" {
				uuid = p.Data.UUID
			}
			if p.Data.Title != "" {
				name = p.Data.Title
			}
			if description == "" {
				description = p.Data.Description
			}
			if trackCount == 0 {
				trackCount = p.Data.NumberOfTracks
			}
		}
		if uuid == "" {
			// Folder item without a resolvable playlist ID — skip it rather
			// than emitting a broken playlist that can't be browsed/imported.
			continue
		}
		out = append(out, playlist.PlaylistInfo{
			SourceID:    uuid,
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(description),
			TrackCount:  trackCount,
		})
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
		durMs := int64(item.Duration) * 1000
		artist := primaryArtistName(item.Artist, item.Artists)
		out[i] = playlist.TrackInfo{
			SourceTrackID: strconv.FormatInt(item.ID, 10),
			Title:         strings.TrimSpace(item.Title),
			Artist:        strings.TrimSpace(artist),
			Album:         strings.TrimSpace(item.Album.Title),
			DurationMs:    durMs,
			ISRC:          strings.TrimSpace(item.ISRC),
		}
	}
	return out, name, nil
}
