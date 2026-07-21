package spotify

import (
	"context"
	"fmt"
	"strings"

	"github.com/ramonskie/groovearr/internal/playlist"
)

// ─── PlaylistSourceProvider ─────────────────────────────────+++++++++++

// PlaylistSource returns the playlist.Source for this plugin.
// Returns self whenever the plugin is configured — free mode supports playlist
// URL import via oEmbed, dev mode adds user playlist browsing via API.
func (p *Plugin) PlaylistSource() playlist.Source {
	if p.IsConfigured() {
		return p
	}
	return nil
}

// Ensure compile-time interface compliance.
var _ playlist.PlaylistSourceProvider = (*Plugin)(nil)

// ─── playlist.Source ──────────────────────────────────────────────────

// pageSize is the maximum number of items per Spotify Web API page.
const pageSize = 50

// GetUserPlaylists returns the authenticated user's playlists.
// Dev mode: calls the Spotify Web API with full pagination.
// Free mode: returns an error.
func (p *Plugin) GetUserPlaylists(ctx context.Context) ([]playlist.PlaylistInfo, error) {
	if p.cfg.Mode != "dev" || p.cfg.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("spotify: no access token — complete OAuth login first")
	}

	var result []playlist.PlaylistInfo
	offset := 0

	for {
		page, err := p.api.GetUserPlaylists(ctx, pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("spotify: get user playlists: %w", err)
		}

		for _, sp := range page.Items {
			result = append(result, simplifiedPlaylistToInfo(&sp))
		}

		// No more pages.
		if page.Next == "" {
			break
		}
		offset += pageSize
	}

	return result, nil
}

// GetPlaylistTracks returns all tracks in a Spotify playlist.
// Dev mode: calls the Spotify Web API with pagination for track listing,
// plus a separate call to get the playlist name.
// Free mode: uses the oEmbed endpoint to parse basic playlist info from
// a Spotify URL, returning limited track info (title only — no artist/album).
func (p *Plugin) GetPlaylistTracks(ctx context.Context, sourcePlaylistID string) ([]playlist.TrackInfo, string, error) {
	if p.cfg.Mode == "dev" && p.cfg.Tokens.AccessToken != "" {
		return p.getPlaylistTracksDev(ctx, sourcePlaylistID)
	}
	return p.getPlaylistTracksFree(ctx, sourcePlaylistID)
}

// getPlaylistTracksDev fetches playlist tracks via the Spotify Web API.
func (p *Plugin) getPlaylistTracksDev(ctx context.Context, playlistID string) ([]playlist.TrackInfo, string, error) {
	// Fetch playlist metadata for the name.
	pl, err := p.api.GetPlaylist(ctx, playlistID)
	if err != nil {
		return nil, "", fmt.Errorf("spotify: get playlist: %w", err)
	}
	playlistName := pl.Name

	var tracks []playlist.TrackInfo
	offset := 0

	for {
		page, err := p.api.GetPlaylistTracks(ctx, playlistID, pageSize, offset)
		if err != nil {
			return nil, "", fmt.Errorf("spotify: get playlist tracks: %w", err)
		}

		for _, pt := range page.Items {
			if pt.Track != nil {
				tracks = append(tracks, playlistTrackToTrackInfo(pt))
			}
		}

		if page.Next == "" {
			break
		}
		offset += pageSize
	}

	return tracks, playlistName, nil
}

// getPlaylistTracksFree scrapes the Spotify embed page to extract playlist
// metadata and track listing. No authentication required — parses the
// __NEXT_DATA__ JSON blob from the embed page to get track names, artists,
// and durations. Capped at what the embed page exposes (~100 tracks).
// sourcePlaylistID may be a full Spotify URL or a raw playlist ID.
func (p *Plugin) getPlaylistTracksFree(ctx context.Context, sourcePlaylistID string) ([]playlist.TrackInfo, string, error) {
	// Extract the raw playlist ID from the input (URL or raw ID).
	parsed, err := ParseSpotifyURL(sourcePlaylistID)
	if err != nil {
		if looksLikeID(sourcePlaylistID) {
			parsed = &SpotifyURL{Type: "playlist", ID: sourcePlaylistID}
		} else {
			return nil, "", fmt.Errorf("spotify: invalid playlist URL or ID: %w", err)
		}
	}
	if parsed.Type != "playlist" {
		return nil, "", fmt.Errorf("spotify: URL type is %q, expected playlist", parsed.Type)
	}

	ep, err := FetchEmbedPlaylist(ctx, p.oembedClient, parsed.ID)
	if err != nil {
		return nil, "", fmt.Errorf("spotify: embed playlist fetch: %w", err)
	}

	tracks := make([]playlist.TrackInfo, len(ep.Tracks))
	for i, t := range ep.Tracks {
		tracks[i] = playlist.TrackInfo{
			SourceTrackID: t.ID,
			Title:         t.Title,
			Artist:        t.Artist,
			DurationMs:    int64(t.Duration),
		}
	}

	return tracks, ep.Name, nil
}

// ─── Mapping helpers ──────────────────────────────────────────────────

// simplifiedPlaylistToInfo converts a Spotify SimplifiedPlaylist to a playlist.PlaylistInfo.
func simplifiedPlaylistToInfo(sp *SimplifiedPlaylist) playlist.PlaylistInfo {
	info := playlist.PlaylistInfo{
		SourceID: sp.ID,
		Name:     sp.Name,
	}

	if sp.Description != nil {
		info.Description = strings.TrimSpace(*sp.Description)
	}

	if sp.Tracks != nil {
		info.TrackCount = sp.Tracks.Total
	}

	if len(sp.Images) > 0 {
		info.CoverURL = sp.Images[0].URL
	}

	ownerName := sp.Owner.ID
	if sp.Owner.DisplayName != nil && *sp.Owner.DisplayName != "" {
		ownerName = *sp.Owner.DisplayName
	}
	info.OwnerName = ownerName

	return info
}

// playlistTrackToTrackInfo converts a Spotify PlaylistTrack (with embedded Track)
// to a playlist.TrackInfo.
func playlistTrackToTrackInfo(pt PlaylistTrack) playlist.TrackInfo {
	t := pt.Track
	info := playlist.TrackInfo{
		SourceTrackID: t.ID,
		Title:         t.Name,
		DurationMs:    int64(t.DurationMs),
		Album:         t.Album.Name,
		ISRC:          t.ExternalIDs.ISRC,
	}

	if len(t.Artists) > 0 {
		info.Artist = t.Artists[0].Name
	}

	return info
}
