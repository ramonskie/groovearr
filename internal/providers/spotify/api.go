package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// API provides typed methods for accessing the Spotify Web API.
// All methods require dev mode (valid access token). Call requireDevMode
// before any request to produce a clear error in free mode.
type API struct {
	client *SpotifyClient
}

// NewAPI creates a Spotify API wrapper around an authenticated SpotifyClient.
func NewAPI(client *SpotifyClient) *API {
	return &API{client: client}
}

// ─── Dev-mode guard ───────────────────────────────────────────────────

func (a *API) requireDevMode() error {
	if a.client.cfg.Mode != "dev" || a.client.cfg.Tokens.AccessToken == "" {
		return fmt.Errorf("spotify: API methods require dev mode with a valid access token")
	}
	return nil
}

// ─── Search ───────────────────────────────────────────────────────────

// SearchTracks searches for tracks matching a query string.
// query supports Spotify field filters (e.g. "track:Do I artist:Luke Bryan").
func (a *API) SearchTracks(ctx context.Context, query string, limit, offset int) (*Paging[Track], error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("type", "track")
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}

	var result struct {
		Tracks Paging[Track] `json:"tracks"`
	}
	if err := a.get(ctx, "/search", params, &result); err != nil {
		return nil, err
	}
	return &result.Tracks, nil
}

// SearchAlbums searches for albums matching a query string.
// query supports Spotify field filters (e.g. "artist:Miles Davis").
func (a *API) SearchAlbums(ctx context.Context, query string, limit, offset int) (*Paging[SimplifiedAlbum], error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("type", "album")
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}

	var result struct {
		Albums Paging[SimplifiedAlbum] `json:"albums"`
	}
	if err := a.get(ctx, "/search", params, &result); err != nil {
		return nil, err
	}
	return &result.Albums, nil
}

// ─── Tracks ───────────────────────────────────────────────────────────

// GetTrack returns full track details by Spotify track ID.
// Optional market parameter specifies an ISO 3166-1 alpha-2 country code.
func (a *API) GetTrack(ctx context.Context, id string, market ...string) (*Track, error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	params := url.Values{}
	if len(market) > 0 && market[0] != "" {
		params.Set("market", market[0])
	}

	var track Track
	if err := a.get(ctx, "/tracks/"+id, params, &track); err != nil {
		return nil, err
	}
	return &track, nil
}

// ─── Albums ───────────────────────────────────────────────────────────

// GetAlbum returns full album details by Spotify album ID.
// Optional market parameter specifies an ISO 3166-1 alpha-2 country code.
func (a *API) GetAlbum(ctx context.Context, id string, market ...string) (*Album, error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	params := url.Values{}
	if len(market) > 0 && market[0] != "" {
		params.Set("market", market[0])
	}

	var album Album
	if err := a.get(ctx, "/albums/"+id, params, &album); err != nil {
		return nil, err
	}
	return &album, nil
}

// ─── Artists ──────────────────────────────────────────────────────────

// GetArtist returns full artist details by Spotify artist ID.
func (a *API) GetArtist(ctx context.Context, id string) (*Artist, error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	var artist Artist
	if err := a.get(ctx, "/artists/"+id, nil, &artist); err != nil {
		return nil, err
	}
	return &artist, nil
}

// ─── Playlists ────────────────────────────────────────────────────────

// GetPlaylist returns a playlist by Spotify playlist ID.
// Optional fields parameter filters response fields (e.g. "name,owner").
func (a *API) GetPlaylist(ctx context.Context, id string, fields ...string) (*Playlist, error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	params := url.Values{}
	if len(fields) > 0 && fields[0] != "" {
		params.Set("fields", fields[0])
	}

	var playlist Playlist
	if err := a.get(ctx, "/playlists/"+id, params, &playlist); err != nil {
		return nil, err
	}
	return &playlist, nil
}

// GetPlaylistTracks returns paginated tracks from a playlist.
// Optional fields parameter filters response fields (e.g. "items(track(name,id))").
func (a *API) GetPlaylistTracks(ctx context.Context, id string, limit, offset int, fields ...string) (*Paging[PlaylistTrack], error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	params := url.Values{}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	if len(fields) > 0 && fields[0] != "" {
		params.Set("fields", fields[0])
	}

	var result Paging[PlaylistTrack]
	if err := a.get(ctx, "/playlists/"+id+"/tracks", params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ─── User Playlists ───────────────────────────────────────────────────

// GetUserPlaylists returns the current user's playlists.
func (a *API) GetUserPlaylists(ctx context.Context, limit, offset int) (*Paging[SimplifiedPlaylist], error) {
	if err := a.requireDevMode(); err != nil {
		return nil, err
	}

	params := url.Values{}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}

	var result Paging[SimplifiedPlaylist]
	if err := a.get(ctx, "/me/playlists", params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ─── Internal helpers ─────────────────────────────────────────────────

// get sends a GET request to the Spotify Web API and decodes the JSON
// response into target. Non-200 responses are returned as descriptive errors.
func (a *API) get(ctx context.Context, path string, params url.Values, target interface{}) error {
	u, err := url.Parse(SpotifyWebAPI + path)
	if err != nil {
		return fmt.Errorf("spotify: invalid URL: %w", err)
	}
	if len(params) > 0 {
		u.RawQuery = params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("spotify: request creation failed: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("spotify: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if decodeErr := json.NewDecoder(resp.Body).Decode(&apiErr); decodeErr == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("spotify: API error %d: %s", apiErr.Error.Status, apiErr.Error.Message)
		}
		return fmt.Errorf("spotify: HTTP %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("spotify: decode failed: %w", err)
	}
	return nil
}
