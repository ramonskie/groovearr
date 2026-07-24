// Package lastfm implements a Last.fm discovery plugin.
package lastfm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ramonskie/groovearr/internal/provider"
)

const lastfmBaseURL = "https://ws.audioscrobbler.com/2.0/"

// Last.fm API rate limit: 3 req/s (conservative — no published limit).
const lastfmAPIRate = 3.0

// ArtistResult represents a Last.fm artist search result.
type ArtistResult struct {
	Name     string     `json:"name"`
	MBID     string     `json:"mbid"`
	Images   []LFMImage `json:"image"`
	ImageURL string     `json:"-"` // extracted from Images (size "large")
}

// LFMImage represents a Last.fm image element.
type LFMImage struct {
	URL  string `json:"#text"`
	Size string `json:"size"`
}

// AlbumResult represents a Last.fm album from an artist's top albums.
type AlbumResult struct {
	Name      string     `json:"name"`
	MBID      string     `json:"mbid"`
	PlayCount int        `json:"playcount"`
	Images    []LFMImage `json:"image"`
	ImageURL  string     `json:"-"` // extracted from Images (size "large")
}

// AlbumDetail is the full album info from album.getInfo.
type AlbumDetail struct {
	Name     string        `json:"name"`
	Artist   string        `json:"artist"`
	Tracks   []TrackResult `json:"-"` // extracted from Tracks.Track
	Images   []LFMImage    `json:"-"` // extracted from album.image
	ImageURL string        `json:"-"` // extracted from Images (size "large")
}

// TrackResult represents a track on a Last.fm album.
type TrackResult struct {
	Name     string `json:"name"`
	Duration string `json:"duration"` // seconds as string
}

// Client provides access to the Last.fm API.
type Client struct {
	httpClient *http.Client
	log        *slog.Logger
	apiKey     string
	baseURL    string // injectable for testing; defaults to lastfmBaseURL
}

// NewClient creates a Last.fm API client with rate-limited transport.
func NewClient(cfg LastFMConfig, logger *slog.Logger) *Client {
	return &Client{
		apiKey:  cfg.APIKey,
		baseURL: lastfmBaseURL,
		httpClient: &http.Client{
			Transport: provider.NewRateLimitedTransport(http.DefaultTransport, lastfmAPIRate),
			Timeout:   15 * time.Second,
		},
		log: logger,
	}
}

// SearchArtists searches for artists by name.
func (c *Client) SearchArtists(ctx context.Context, query string, limit int) ([]ArtistResult, error) {
	data, err := c.apiGet(ctx, map[string]string{
		"method": "artist.search",
		"artist": query,
		"limit":  strconv.Itoa(limit),
	})
	if err != nil {
		c.log.Error("lastfm search artists failed", "error", err, "query", query, "component", "lastfm_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	// Response: {"results": {"artistmatches": {"artist": [...]}}}
	var result struct {
		Results struct {
			ArtistMatches struct {
				Artist json.RawMessage `json:"artist"`
			} `json:"artistmatches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("lastfm unmarshal search artists failed", "error", err, "component", "lastfm_api")
		return nil, err
	}
	// If no matches, artist field is empty string "".
	if len(result.Results.ArtistMatches.Artist) == 0 || string(result.Results.ArtistMatches.Artist) == `""` {
		return nil, nil
	}
	// artist might be an array or a single object.
	var artists []ArtistResult
	if result.Results.ArtistMatches.Artist[0] == '[' {
		if err := json.Unmarshal(result.Results.ArtistMatches.Artist, &artists); err != nil {
			c.log.Error("lastfm unmarshal artist array failed", "error", err, "component", "lastfm_api")
			return nil, err
		}
	} else {
		var single ArtistResult
		if err := json.Unmarshal(result.Results.ArtistMatches.Artist, &single); err != nil {
			c.log.Error("lastfm unmarshal artist single failed", "error", err, "component", "lastfm_api")
			return nil, err
		}
		artists = []ArtistResult{single}
	}
	// Extract image URLs.
	for i := range artists {
		artists[i].ImageURL = extractLargeImage(artists[i].Images)
	}
	return artists, nil
}

// GetArtistTopAlbums fetches an artist's top albums.
func (c *Client) GetArtistTopAlbums(ctx context.Context, artistName string, limit int) ([]AlbumResult, error) {
	data, err := c.apiGet(ctx, map[string]string{
		"method": "artist.gettopalbums",
		"artist": artistName,
		"limit":  strconv.Itoa(limit),
	})
	if err != nil {
		c.log.Error("lastfm get artist top albums failed", "error", err, "artist", artistName, "component", "lastfm_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var result struct {
		TopAlbums struct {
			Album json.RawMessage `json:"album"`
		} `json:"topalbums"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("lastfm unmarshal top albums failed", "error", err, "component", "lastfm_api")
		return nil, err
	}
	var albums []AlbumResult
	if len(result.TopAlbums.Album) == 0 || string(result.TopAlbums.Album) == `""` {
		return nil, nil
	}
	if result.TopAlbums.Album[0] == '[' {
		if err := json.Unmarshal(result.TopAlbums.Album, &albums); err != nil {
			c.log.Error("lastfm unmarshal album array failed", "error", err, "component", "lastfm_api")
			return nil, err
		}
	} else {
		var single AlbumResult
		if err := json.Unmarshal(result.TopAlbums.Album, &single); err != nil {
			c.log.Error("lastfm unmarshal album single failed", "error", err, "component", "lastfm_api")
			return nil, err
		}
		albums = []AlbumResult{single}
	}
	for i := range albums {
		albums[i].ImageURL = extractLargeImage(albums[i].Images)
	}
	return albums, nil
}

// GetAlbumInfo fetches full album details including track list.
func (c *Client) GetAlbumInfo(ctx context.Context, artistName, albumName string) (*AlbumDetail, error) {
	data, err := c.apiGet(ctx, map[string]string{
		"method": "album.getinfo",
		"artist": artistName,
		"album":  albumName,
	})
	if err != nil {
		c.log.Error("lastfm get album info failed", "error", err, "artist", artistName, "album", albumName, "component", "lastfm_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	// Response: {"album": {"name":..., "artist":..., "tracks": {"track": ...}, "image": [...]}}
	var wrapper struct {
		Album struct {
			Name   string `json:"name"`
			Artist string `json:"artist"`
			Tracks struct {
				Track json.RawMessage `json:"track"`
			} `json:"tracks"`
			Images []LFMImage `json:"image"`
		} `json:"album"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		c.log.Error("lastfm unmarshal album info failed", "error", err, "component", "lastfm_api")
		return nil, err
	}
	detail := &AlbumDetail{
		Name:   wrapper.Album.Name,
		Artist: wrapper.Album.Artist,
		Images: wrapper.Album.Images,
	}
	detail.ImageURL = extractLargeImage(wrapper.Album.Images)
	// Handle single track (object) vs multiple tracks (array).
	if len(wrapper.Album.Tracks.Track) > 0 && string(wrapper.Album.Tracks.Track) != `""` {
		if wrapper.Album.Tracks.Track[0] == '[' {
			if err := json.Unmarshal(wrapper.Album.Tracks.Track, &detail.Tracks); err != nil {
				c.log.Error("lastfm unmarshal track array failed", "error", err, "component", "lastfm_api")
				return nil, err
			}
		} else {
			var single TrackResult
			if err := json.Unmarshal(wrapper.Album.Tracks.Track, &single); err != nil {
				c.log.Error("lastfm unmarshal track single failed", "error", err, "component", "lastfm_api")
				return nil, err
			}
			detail.Tracks = []TrackResult{single}
		}
	}
	return detail, nil
}

// apiGet performs a GET request to the Last.fm API and returns the raw JSON.
// Follows the Deezer apiGet pattern.
func (c *Client) apiGet(ctx context.Context, params map[string]string) (json.RawMessage, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		c.log.Error("lastfm api URL parse failed", "error", err, "component", "lastfm_api")
		return nil, err
	}

	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	q.Set("api_key", c.apiKey)
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		c.log.Error("lastfm api create request failed", "error", err, "component", "lastfm_api")
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Error("lastfm api HTTP request failed", "error", err, "component", "lastfm_api")
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Error("lastfm api read response failed", "error", err, "component", "lastfm_api")
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		c.log.Error("lastfm api non-OK status", "status", resp.StatusCode, "body", string(body)[:min(len(string(body)), 200)], "component", "lastfm_api")
		return nil, fmt.Errorf("lastfm API HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Check for API-level error.
	var errResp struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error > 0 {
		return nil, fmt.Errorf("lastfm API error %d: %s", errResp.Error, errResp.Message)
	}

	return json.RawMessage(body), nil
}

// extractLargeImage finds the "large" size image URL from Last.fm's image array.
func extractLargeImage(images []LFMImage) string {
	for _, img := range images {
		if img.Size == "large" {
			return img.URL
		}
	}
	// Fallback: try "extralarge".
	for _, img := range images {
		if img.Size == "extralarge" {
			return img.URL
		}
	}
	return ""
}
