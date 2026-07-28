// Package tidal implements the Tidal metadata API client.
// This client covers search, album, artist, and playlist endpoints NOT handled by go-tiddl.
// Auth (device code), track metadata (GetTrack), stream URL (GetTrackStream),
// and download (DownloadTrackStream) are handled by the go-tiddl library.
//
// API Base URLs:
//   - v1: https://api.tidal.com/v1/  (search, artists, albums, tracks, playlists)
//   - v2: https://api.tidal.com/v2/  (user playlists, collection)
//
// Auth: Bearer token in Authorization header. Country code as query param on all requests.
package tidal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/quality"
)

// apiClient is the low-level HTTP client for Tidal's REST API v1 and v2.
// It handles search, album, artist, and playlist endpoints.
// Auth (device code) and track/stream/download are handled by go-tiddl in client.go.

const (
	v1BaseURL = "https://api.tidal.com/v1"
	v2BaseURL = "https://api.tidal.com/v2"
	// defaultRateInterval is the minimum interval between requests to avoid 429s.
	defaultRateInterval = 1200 * time.Millisecond
)

// ─── apiClient ──────────────────────────────────────────────────────────

// apiClient provides access to Tidal's metadata API (search, albums, artists, playlists).
// Auth and track/stream endpoints are handled by the go-tiddl library.
type apiClient struct {
	httpClient  *http.Client
	v1BaseURL   string
	v2BaseURL   string
	accessToken string
	countryCode string
	mu          sync.RWMutex // protects accessToken and countryCode

	// Rate limiting with 429 Retry-After awareness.
	lastCall    time.Time
	minInterval time.Duration
}

// newAPI creates a Tidal metadata API client.
// baseURL and baseV2URL override the defaults when non-empty.
func newAPI(baseURL, baseV2URL string, timeout, rateInterval time.Duration) *apiClient {
	if baseURL == "" {
		baseURL = v1BaseURL
	}
	if baseV2URL == "" {
		baseV2URL = v2BaseURL
	}
	if rateInterval <= 0 {
		rateInterval = defaultRateInterval
	}
	return &apiClient{
		httpClient:  &http.Client{Timeout: timeout},
		v1BaseURL:   baseURL,
		v2BaseURL:   baseV2URL,
		accessToken: "",
		countryCode: "US",
		minInterval: rateInterval,
	}
}

// newAPIClient creates an apiClient from TidalConfig for use by the plugin client.
// This bridges the plugin-level constructor with the low-level API client.
// The download.Plugin and other interfaces are implemented in client.go.
func newAPIClient(cfg TidalConfig) *apiClient {
	c := newAPI("", "", 30*time.Second, 0)
	if cfg.AccessToken != "" {
		c.SetToken(cfg.AccessToken)
	}
	if cfg.CountryCode != "" {
		c.SetCountry(cfg.CountryCode)
	}
	return c
}

// SetToken updates the OAuth2 Bearer token at runtime.
func (c *apiClient) SetToken(token string) {
	c.mu.Lock()
	c.accessToken = token
	c.mu.Unlock()
}

// SetCountry updates the country code used for catalog availability.
func (c *apiClient) SetCountry(code string) {
	c.mu.Lock()
	c.countryCode = code
	c.mu.Unlock()
}

// ─── Response types ────────────────────────────────────────────────────

// TrackSearchResult is a track from /v1/search results (sparse).
type TrackSearchResult struct {
	ID          int64              `json:"id"`
	Title       string             `json:"title"`
	Duration    int                `json:"duration"`
	Explicit    bool               `json:"explicit"`
	TrackNumber int                `json:"trackNumber"`
	VolumeNumber int               `json:"volumeNumber"`
	ISRC        string             `json:"isrc"`
	Version     string             `json:"version"`
	AudioQuality string            `json:"audioQuality"`
	Popularity  int                `json:"popularity"`
	URL         string             `json:"url"`
	Artist      ArtistSearchResult `json:"artist"`
	Album       AlbumSearchResult  `json:"album"`
	Artists     []ArtistSearchResult `json:"artists"`
}

// AlbumSearchResult is an album from /v1/search results (sparse).
type AlbumSearchResult struct {
	ID          int64                `json:"id"`
	Title       string               `json:"title"`
	Cover       string               `json:"cover"`
	Type        string               `json:"type"`
	Explicit    bool                 `json:"explicit"`
	NumTracks   int                  `json:"numTracks"`
	NumVolumes  int                  `json:"numVolumes"`
	Duration    int                  `json:"duration"`
	ReleaseDate string               `json:"releaseDate"`
	AudioQuality string              `json:"audioQuality"`
	Popularity  int                  `json:"popularity"`
	Artist      ArtistSearchResult   `json:"artist"`
	Artists     []ArtistSearchResult `json:"artists"`
	URL         string               `json:"url"`
}

// ArtistSearchResult is an artist from /v1/search results (sparse).
type ArtistSearchResult struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
	URL     string `json:"url"`
}

// AlbumDetail is the full album response from /v1/albums/{id}.
type AlbumDetail struct {
	ID                   int64                `json:"id"`
	Title                string               `json:"title"`
	Cover                string               `json:"cover"`
	VideoCover           string               `json:"videoCover"`
	Type                 string               `json:"type"`
	Duration             int                  `json:"duration"`
	Available            bool                 `json:"available"`
	AllowStreaming       bool                 `json:"allowStreaming"`
	PremiumStreamingOnly bool                 `json:"premiumStreamingOnly"`
	NumTracks            int                  `json:"numTracks"`
	NumVideos            int                  `json:"numVideos"`
	NumVolumes           int                  `json:"numVolumes"`
	TidalReleaseDate     string               `json:"tidalReleaseDate"`
	ReleaseDate          string               `json:"releaseDate"`
	Copyright            string               `json:"copyright"`
	UPC                  string               `json:"upc"`
	Version              string               `json:"version"`
	Explicit             bool                 `json:"explicit"`
	UniversalProductNumber int                `json:"universalProductNumber"`
	Popularity           int                  `json:"popularity"`
	AudioQuality         string               `json:"audioQuality"`
	AudioModes           []string             `json:"audioModes"`
	MediaMetadataTags    []string             `json:"mediaMetadataTags"`
	Artist               ArtistSearchResult   `json:"artist"`
	Artists              []ArtistSearchResult `json:"artists"`
	Tracks               []TrackSearchResult  `json:"tracks,omitempty"`
}

// TrackInfo is a full track from /v1/tracks/{id}, /v1/albums/{id}/tracks,
// /v1/artists/{id}/toptracks, and /v1/playlists/{uuid}/tracks.
type TrackInfo struct {
	ID                   int64                `json:"id"`
	Title                string               `json:"title"`
	Duration             int                  `json:"duration"`
	Explicit             bool                 `json:"explicit"`
	TrackNumber          int                  `json:"trackNumber"`
	VolumeNumber         int                  `json:"volumeNumber"`
	ISRC                 string               `json:"isrc"`
	Version              string               `json:"version"`
	Copyright            string               `json:"copyright"`
	Popularity           int                  `json:"popularity"`
	AudioQuality         string               `json:"audioQuality"`
	AudioModes           []string             `json:"audioModes"`
	MediaMetadataTags    []string             `json:"mediaMetadataTags"`
	AllowStreaming       bool                 `json:"allowStreaming"`
	StreamReady          bool                 `json:"streamReady"`
	PremiumStreamingOnly bool                 `json:"premiumStreamingOnly"`
	ReplayGain           float64              `json:"replayGain"`
	Peak                 float64              `json:"peak"`
	URL                  string               `json:"url"`
	Album                AlbumSearchResult    `json:"album"`
	Artist               ArtistSearchResult   `json:"artist"`
	Artists              []ArtistSearchResult `json:"artists"`
}

// PlaylistInfo represents a user playlist from the v2 my-collection endpoint.
type PlaylistInfo struct {
	UUID              string `json:"uuid"`
	ID                string `json:"id"`
	TRN               string `json:"trn"`
	Name              string `json:"name"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	NumTracks         int    `json:"numTracks"`
	NumVideos         int    `json:"numVideos"`
	Duration          int    `json:"duration"`
	Type              string `json:"type"`
	Public            bool   `json:"public"`
	Popularity        int    `json:"popularity"`
	Picture           string `json:"picture"`
	SquarePicture     string `json:"squarePicture"`
	LastItemAddedAt   string `json:"lastItemAddedAt"`
	LastUpdated       string `json:"lastUpdated"`
	Created           string `json:"created"`
	UserDateAdded     string `json:"userDateAdded"`
	ETag              string `json:"etag"`
}

// PlaylistTrackItem is a track from /v1/playlists/{uuid}/tracks.
type PlaylistTrackItem struct {
	Item   TrackInfo `json:"item"`
	Cut    string    `json:"cut"`
	DateAdded string `json:"dateAdded"`
	Index  int       `json:"index"`
}

// searchPayload is the combined /v1/search response wrapper.
type searchPayload struct {
	Artists struct {
		Items              []ArtistSearchResult `json:"items"`
		TotalNumberOfItems int                  `json:"totalNumberOfItems"`
	} `json:"artists"`
	Albums struct {
		Items              []AlbumSearchResult `json:"items"`
		TotalNumberOfItems int                 `json:"totalNumberOfItems"`
	} `json:"albums"`
	Tracks struct {
		Items              []TrackSearchResult `json:"items"`
		TotalNumberOfItems int                 `json:"totalNumberOfItems"`
	} `json:"tracks"`
}

// albumTracksPayload is the /v1/albums/{id}/tracks response wrapper.
type albumTracksPayload struct {
	Items              []TrackSearchResult `json:"items"`
	TotalNumberOfItems int                 `json:"totalNumberOfItems"`
}

// artistAlbumsPayload is the /v1/artists/{id}/albums response wrapper.
type artistAlbumsPayload struct {
	Items              []AlbumSearchResult `json:"items"`
	TotalNumberOfItems int                 `json:"totalNumberOfItems"`
}

// artistTopTracksPayload is the /v1/artists/{id}/toptracks response wrapper.
type artistTopTracksPayload struct {
	Items              []TrackInfo `json:"items"`
	TotalNumberOfItems int         `json:"totalNumberOfItems"`
}

// similarArtistsPayload is the /v1/artists/{id}/similar response wrapper.
type similarArtistsPayload struct {
	Items              []ArtistSearchResult `json:"items"`
	TotalNumberOfItems int                  `json:"totalNumberOfItems"`
}

// userPlaylistsPayload is the v2 /my-collection/playlists/folders response.
type userPlaylistsPayload struct {
	Items              []PlaylistInfo `json:"items"`
	TotalNumberOfItems int            `json:"totalNumberOfItems"`
	Cursor             string         `json:"cursor"`
}

// playlistTracksPayload is the /v1/playlists/{uuid}/tracks response wrapper.
type playlistTracksPayload struct {
	Items              []PlaylistTrackItem `json:"items"`
	TotalNumberOfItems int                 `json:"totalNumberOfItems"`
}

// tidalError is the Tidal API error response body.
type tidalError struct {
	Errors      []tidalErrorDetail `json:"errors"`
	UserMessage string             `json:"userMessage"`
}

type tidalErrorDetail struct {
	Detail string `json:"detail"`
}

func (e tidalError) Error() string {
	if len(e.Errors) > 0 && e.Errors[0].Detail != "" {
		return e.Errors[0].Detail
	}
	if e.UserMessage != "" {
		return e.UserMessage
	}
	return "unknown tidal API error"
}

// ─── Search methods ────────────────────────────────────────────────────

// SearchTracks searches for tracks matching a query.
// Returns up to 300 results (API maximum), paginated via offset.
func (c *apiClient) SearchTracks(ctx context.Context, query string, limit, offset int) ([]TrackSearchResult, error) {
	payload, err := c.search(ctx, query, limit, offset, "TRACKS")
	if err != nil {
		return nil, fmt.Errorf("tidal search tracks: %w", err)
	}
	return payload.Tracks.Items, nil
}

// SearchAlbums searches for albums matching a query.
func (c *apiClient) SearchAlbums(ctx context.Context, query string, limit, offset int) ([]AlbumSearchResult, error) {
	payload, err := c.search(ctx, query, limit, offset, "ALBUMS")
	if err != nil {
		return nil, fmt.Errorf("tidal search albums: %w", err)
	}
	return payload.Albums.Items, nil
}

// SearchArtists searches for artists by name.
func (c *apiClient) SearchArtists(ctx context.Context, query string, limit int) ([]ArtistSearchResult, error) {
	payload, err := c.search(ctx, query, limit, 0, "ARTISTS")
	if err != nil {
		return nil, fmt.Errorf("tidal search artists: %w", err)
	}
	return payload.Artists.Items, nil
}

// Search performs a combined search for tracks and albums.
func (c *apiClient) Search(ctx context.Context, query string, limit, offset int) ([]TrackSearchResult, []AlbumSearchResult, error) {
	payload, err := c.search(ctx, query, limit, offset, "TRACKS,ALBUMS")
	if err != nil {
		return nil, nil, fmt.Errorf("tidal search: %w", err)
	}
	return payload.Tracks.Items, payload.Albums.Items, nil
}

func (c *apiClient) search(ctx context.Context, query string, limit, offset int, types string) (*searchPayload, error) {
	params := map[string]string{
		"query":  query,
		"types":  types,
		"limit":  strconv.Itoa(clamp(limit, 1, 300)),
		"offset": strconv.Itoa(max(offset, 0)),
	}
	data, err := c.doRequest(ctx, http.MethodGet, c.v1BaseURL+"/search", params)
	if err != nil {
		return nil, err
	}
	var payload searchPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("tidal search unmarshal: %w", err)
	}
	return &payload, nil
}

// ─── Album methods ─────────────────────────────────────────────────────

// GetAlbum returns full album details by Tidal album ID, including track list.
func (c *apiClient) GetAlbum(ctx context.Context, albumID string) (*AlbumDetail, error) {
	data, err := c.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("%s/albums/%s", c.v1BaseURL, albumID), nil)
	if err != nil {
		return nil, fmt.Errorf("tidal get album %s: %w", albumID, err)
	}
	var album AlbumDetail
	if err := json.Unmarshal(data, &album); err != nil {
		return nil, fmt.Errorf("tidal get album %s unmarshal: %w", albumID, err)
	}
	return &album, nil
}

// GetAlbumTracks returns all tracks for an album.
func (c *apiClient) GetAlbumTracks(ctx context.Context, albumID string) ([]TrackSearchResult, error) {
	var allTracks []TrackSearchResult
	offset := 0
	for {
		params := map[string]string{
			"limit":  "100",
			"offset": strconv.Itoa(offset),
		}
		data, err := c.doRequest(ctx, http.MethodGet,
			fmt.Sprintf("%s/albums/%s/tracks", c.v1BaseURL, albumID), params)
		if err != nil {
			return nil, fmt.Errorf("tidal get album tracks %s: %w", albumID, err)
		}
		var payload albumTracksPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("tidal get album tracks %s unmarshal: %w", albumID, err)
		}
		allTracks = append(allTracks, payload.Items...)
		if len(payload.Items) < 100 || len(allTracks) >= payload.TotalNumberOfItems {
			break
		}
		offset += len(payload.Items)
	}
	return allTracks, nil
}

// ─── Artist methods ────────────────────────────────────────────────────

// GetArtistAlbums returns all albums/EPs/singles for an artist.
func (c *apiClient) GetArtistAlbums(ctx context.Context, artistID string, limit int) ([]AlbumSearchResult, error) {
	var allAlbums []AlbumSearchResult
	offset := 0
	fetchLimit := clamp(limit, 1, 300)
	for offset < fetchLimit {
		chunk := min(100, fetchLimit-offset)
		params := map[string]string{
			"limit":  strconv.Itoa(chunk),
			"offset": strconv.Itoa(offset),
		}
		data, err := c.doRequest(ctx, http.MethodGet,
			fmt.Sprintf("%s/artists/%s/albums", c.v1BaseURL, artistID), params)
		if err != nil {
			return nil, fmt.Errorf("tidal get artist albums %s: %w", artistID, err)
		}
		var payload artistAlbumsPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("tidal get artist albums %s unmarshal: %w", artistID, err)
		}
		allAlbums = append(allAlbums, payload.Items...)
		if len(payload.Items) < chunk {
			break
		}
		offset += len(payload.Items)
	}
	return allAlbums, nil
}

// GetArtistTopTracks returns an artist's most popular tracks.
func (c *apiClient) GetArtistTopTracks(ctx context.Context, artistID string, limit int) ([]TrackInfo, error) {
	data, err := c.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("%s/artists/%s/toptracks", c.v1BaseURL, artistID),
		map[string]string{
			"limit":  strconv.Itoa(clamp(limit, 1, 100)),
			"offset": "0",
		})
	if err != nil {
		return nil, fmt.Errorf("tidal get artist top tracks %s: %w", artistID, err)
	}
	var payload artistTopTracksPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("tidal get artist top tracks %s unmarshal: %w", artistID, err)
	}
	return payload.Items, nil
}

// GetSimilarArtists returns artists similar to the given artist.
func (c *apiClient) GetSimilarArtists(ctx context.Context, artistID string, limit int) ([]ArtistSearchResult, error) {
	data, err := c.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("%s/artists/%s/similar", c.v1BaseURL, artistID),
		map[string]string{
			"limit":  strconv.Itoa(clamp(limit, 1, 100)),
			"offset": "0",
		})
	if err != nil {
		return nil, fmt.Errorf("tidal get similar artists %s: %w", artistID, err)
	}
	var payload similarArtistsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("tidal get similar artists %s unmarshal: %w", artistID, err)
	}
	return payload.Items, nil
}

// ─── Playlist methods ──────────────────────────────────────────────────

// GetUserPlaylists returns the user's playlists from the v2 my-collection endpoint.
func (c *apiClient) GetUserPlaylists(ctx context.Context, limit, offset int) ([]PlaylistInfo, error) {
	params := map[string]string{
		"folderId":    "root",
		"includeOnly": "PLAYLIST",
		"limit":       strconv.Itoa(clamp(limit, 1, 50)),
		"offset":      strconv.Itoa(max(offset, 0)),
	}
	data, err := c.doRequest(ctx, http.MethodGet,
		c.v2BaseURL+"/my-collection/playlists/folders", params)
	if err != nil {
		return nil, fmt.Errorf("tidal get user playlists: %w", err)
	}
	var payload userPlaylistsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("tidal get user playlists unmarshal: %w", err)
	}
	return payload.Items, nil
}

// GetPlaylistTracks returns tracks from a playlist, plus the playlist name.
func (c *apiClient) GetPlaylistTracks(ctx context.Context, playlistUUID string) ([]PlaylistTrackItem, string, error) {
	// Get playlist metadata for the name.
	playlistName := ""
	metaData, err := c.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("%s/playlists/%s", c.v1BaseURL, playlistUUID), nil)
	if err != nil {
		return nil, "", fmt.Errorf("tidal get playlist %s metadata: %w", playlistUUID, err)
	}
	var meta struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(metaData, &meta) == nil {
		playlistName = meta.Title
	}

	// Get all tracks (paginated, max 100 per request).
	var allTracks []PlaylistTrackItem
	offset := 0
	for {
		params := map[string]string{
			"limit":  "100",
			"offset": strconv.Itoa(offset),
		}
		data, err := c.doRequest(ctx, http.MethodGet,
			fmt.Sprintf("%s/playlists/%s/tracks", c.v1BaseURL, playlistUUID), params)
		if err != nil {
			return allTracks, playlistName, fmt.Errorf("tidal get playlist tracks %s: %w", playlistUUID, err)
		}
		var payload playlistTracksPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			return allTracks, playlistName, fmt.Errorf("tidal get playlist tracks %s unmarshal: %w", playlistUUID, err)
		}
		allTracks = append(allTracks, payload.Items...)
		if len(payload.Items) < 100 {
			break
		}
		offset += len(payload.Items)
	}
	return allTracks, playlistName, nil
}

// ─── Helpers ───────────────────────────────────────────────────────────

// doRequest performs an HTTP request with auth header, country code, and rate limiting.
func (c *apiClient) doRequest(ctx context.Context, method, urlStr string, params map[string]string) (json.RawMessage, error) {
	// Snapshot mutable fields under lock to avoid data races.
	c.mu.RLock()
	token := c.accessToken
	country := c.countryCode
	last := c.lastCall
	c.mu.RUnlock()

	// Rate limiting: enforce minimum interval between requests.
	if elapsed := time.Since(last); elapsed < c.minInterval {
		timer := time.NewTimer(c.minInterval - elapsed)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}

	var resp *http.Response
	var lastErr error
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		c.mu.Lock()
		c.lastCall = time.Now()
		c.mu.Unlock()

		u, err := url.Parse(urlStr)
		if err != nil {
			return nil, fmt.Errorf("tidal URL parse: %w", err)
		}

		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		if country != "" {
			q.Set("countryCode", country)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("tidal create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("tidal HTTP request: %w", err)
		}

		// Handle 429 with Retry-After.
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			if attempt < maxRetries-1 && retryAfter > 0 {
				timer := time.NewTimer(retryAfter)
				select {
				case <-timer.C:
					continue
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				}
			}
			lastErr = fmt.Errorf("tidal rate limited (429) after %d retries", attempt+1)
			continue
		}

		lastErr = nil // successful response clears any prior rate-limit error
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}
	if resp == nil {
		return nil, fmt.Errorf("tidal: nil response after retries")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tidal read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Try to parse the Tidal error body for a more specific message.
		var apiErr tidalError
		if json.Unmarshal(body, &apiErr) == nil && (len(apiErr.Errors) > 0 || apiErr.UserMessage != "") {
			return nil, fmt.Errorf("tidal API HTTP %d: %w", resp.StatusCode, apiErr)
		}
		return nil, fmt.Errorf("tidal API HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}

// parseRetryAfter parses the Retry-After header value (seconds or HTTP-date).
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 5 * time.Second
	}
	if sec, err := strconv.Atoi(header); err == nil {
		return time.Duration(sec) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		return time.Until(t)
	}
	return 5 * time.Second
}

// ─── Quality mapping ───────────────────────────────────────────────────

// TidalQualityToAudioQuality maps a Tidal quality tier string to a quality.AudioQuality descriptor.
func TidalQualityToAudioQuality(tidalQuality string) quality.AudioQuality {
	switch tidalQuality {
	case "HI_RES_LOSSLESS":
		return quality.AudioQuality{
			Format:     "flac",
			Bitrate:    3000,
			SampleRate: 192000,
			BitDepth:   24,
		}
	case "LOSSLESS":
		return quality.AudioQuality{
			Format:     "flac",
			Bitrate:    900,
			SampleRate: 44100,
			BitDepth:   16,
		}
	case "HIGH":
		return quality.AudioQuality{
			Format:  "aac",
			Bitrate: 320,
		}
	case "LOW":
		return quality.AudioQuality{
			Format:  "aac",
			Bitrate: 96,
		}
	default:
		return quality.AudioQuality{
			Format:  "aac",
			Bitrate: 0,
		}
	}
}

// ─── Image CDN ─────────────────────────────────────────────────────────

// ImageURL builds a Tidal image CDN URL from a cover/picture UUID.
// Converts dashes to slashes as required by resources.tidal.com.
// Example: ImageURL("a1b2c3d4-e5f6-7890-abcd-ef1234567890", 640, 640)
func ImageURL(coverUUID string, width, height int) string {
	slug := strings.ReplaceAll(coverUUID, "-", "/")
	return fmt.Sprintf("https://resources.tidal.com/images/%s/%dx%d.jpg", slug, width, height)
}

// ─── Internal helpers ──────────────────────────────────────────────────

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
