// Package discogs implements a Discogs discovery plugin.
package discogs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/provider"
)

const discogsBaseURL = "https://api.discogs.com"

// Discogs API rate limit: 0.4 req/s (24/min for unauthenticated requests).
// Authenticated requests may use higher rates, but we stay conservative.
const discogsAPIRate = 0.4

// ArtistResult represents a Discogs artist search result.
type ArtistResult struct {
	ID       int    `json:"id"`
	Name     string `json:"title"`
	ImageURL string `json:"cover_image"`
	Thumb    string `json:"thumb"`
}

// ArtistDetail represents a full Discogs artist resource.
type ArtistDetail struct {
	Name   string        `json:"name"`
	Images []ArtistImage `json:"images"`
}

// ArtistImage represents a Discogs artist image.
type ArtistImage struct {
	URI         string `json:"uri"`
	URI150      string `json:"uri150"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// ReleaseResult represents a minimal Discogs release from search or artist pages.
type ReleaseResult struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
	Type   string `json:"type"` // "release" or "master"
	Thumb  string `json:"thumb"`
	Artist string `json:"artist"` // not always present in search results
}

// ReleaseDetail is the full release object from GET /releases/{id}.
type ReleaseDetail struct {
	Title      string        `json:"title"`
	Year       int           `json:"year"`
	ArtistName string        `json:"-"` // extracted from artists array
	Tracklist  []TrackResult `json:"tracklist"`
	Artists    []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

// TrackResult represents a track on a Discogs release.
type TrackResult struct {
	Position string `json:"position"`
	Title    string `json:"title"`
	Duration string `json:"duration"`
}

// Client provides access to the Discogs REST API.
type Client struct {
	httpClient *http.Client
	log        *slog.Logger
	cfg        DiscogsConfig
	baseURL    string // injectable for testing; defaults to discogsBaseURL
}

// NewClient creates a Discogs API client with rate-limited transport.
func NewClient(cfg DiscogsConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg:        cfg,
		baseURL:    discogsBaseURL,
		httpClient: &http.Client{
			Transport: provider.NewRateLimitedTransport(http.DefaultTransport, discogsAPIRate),
			Timeout:   15 * time.Second,
		},
		log: logger,
	}
}

// SearchArtists searches for artists by name.
func (c *Client) SearchArtists(ctx context.Context, query string, limit int) ([]ArtistResult, error) {
	data, err := c.apiGet(ctx, "/database/search", map[string]string{
		"q":        query,
		"type":     "artist",
		"per_page": strconv.Itoa(limit),
	})
	if err != nil {
		c.log.Error("discogs search artists failed", "error", err, "query", query, "component", "discogs_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var result struct {
		Results []ArtistResult `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("discogs unmarshal search artists failed", "error", err, "component", "discogs_api")
		return nil, err
	}
	return result.Results, nil
}

// GetArtist fetches the full artist resource including images.
func (c *Client) GetArtist(ctx context.Context, artistID int) (*ArtistDetail, error) {
	data, err := c.apiGet(ctx, fmt.Sprintf("/artists/%d", artistID), nil)
	if err != nil {
		c.log.Error("discogs get artist failed", "error", err, "artistID", artistID, "component", "discogs_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var result ArtistDetail
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("discogs unmarshal artist failed", "error", err, "component", "discogs_api")
		return nil, err
	}
	return &result, nil
}

// GetArtistReleases fetches releases for a given artist.
func (c *Client) GetArtistReleases(ctx context.Context, artistID, limit int) ([]ReleaseResult, error) {
	perPage := "50"
	if limit > 0 && limit < 50 {
		perPage = strconv.Itoa(limit)
	}
	data, err := c.apiGet(ctx, fmt.Sprintf("/artists/%d/releases", artistID), map[string]string{
		"per_page": perPage,
		"sort":     "year",
	})
	if err != nil {
		c.log.Error("discogs get artist releases failed", "error", err, "artistID", artistID, "component", "discogs_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var result struct {
		Releases []ReleaseResult `json:"releases"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("discogs unmarshal artist releases failed", "error", err, "artistID", artistID, "component", "discogs_api")
		return nil, err
	}
	return result.Releases, nil
}

// GetRelease returns the full release details including tracklist.
func (c *Client) GetRelease(ctx context.Context, releaseID int) (*ReleaseDetail, error) {
	data, err := c.apiGet(ctx, fmt.Sprintf("/releases/%d", releaseID), nil)
	if err != nil {
		c.log.Error("discogs get release failed", "error", err, "releaseID", releaseID, "component", "discogs_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var detail ReleaseDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		c.log.Error("discogs unmarshal release failed", "error", err, "releaseID", releaseID, "component", "discogs_api")
		return nil, err
	}
	// Extract artist name from artists array.
	if len(detail.Artists) > 0 {
		detail.ArtistName = detail.Artists[0].Name
	}
	return &detail, nil
}

// SearchAlbums searches for releases by title or artist+album.
func (c *Client) SearchAlbums(ctx context.Context, query string, limit int) ([]ReleaseResult, error) {
	data, err := c.apiGet(ctx, "/database/search", map[string]string{
		"q":        query,
		"type":     "release",
		"per_page": strconv.Itoa(limit),
	})
	if err != nil {
		c.log.Error("discogs search releases failed", "error", err, "query", query, "component", "discogs_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var result struct {
		Results []ReleaseResult `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("discogs unmarshal search releases failed", "error", err, "component", "discogs_api")
		return nil, err
	}
	return result.Results, nil
}

// apiGet performs a GET request to the Discogs API and returns the raw JSON.
// Follows the Deezer apiGet pattern.
func (c *Client) apiGet(ctx context.Context, endpoint string, params map[string]string) (json.RawMessage, error) {
	u, err := url.Parse(c.baseURL + "/" + strings.TrimLeft(endpoint, "/"))
	if err != nil {
		c.log.Error("discogs api URL parse failed", "error", err, "endpoint", endpoint, "component", "discogs_api")
		return nil, err
	}

	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		c.log.Error("discogs api create request failed", "error", err, "endpoint", endpoint, "component", "discogs_api")
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Groovearr/1.0")

	// Add auth if consumer key is configured.
	if c.cfg.ConsumerKey != "" {
		req.Header.Set("Authorization", "Discogs key="+c.cfg.ConsumerKey+", secret="+c.cfg.ConsumerSecret)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Error("discogs api HTTP request failed", "error", err, "endpoint", endpoint, "component", "discogs_api")
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Error("discogs api read response failed", "error", err, "endpoint", endpoint, "component", "discogs_api")
		return nil, err
	}

	// Discogs returns 404 for not-found resources.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		// Try to extract error message.
		var errResp struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("discogs API HTTP %d: %s", resp.StatusCode, errResp.Message)
		}
		c.log.Error("discogs api non-OK status", "status", resp.StatusCode, "body", string(body)[:min(len(string(body)), 200)], "component", "discogs_api")
		return nil, fmt.Errorf("discogs API HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}
