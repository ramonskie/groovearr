// Package deezer implements the Deezer metadata API client.
// This client uses the public API (no auth required for search).
// OAuth token support for user-level endpoints (favorites, playlists).
package deezer

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

	"github.com/ramonskie/groovearr/internal/domain"
)

const baseURL = "https://api.deezer.com"

// Track represents a Deezer track from the public API.
type Track struct {
	ID         int    `json:"id"`
	Title      string `json:"title"`
	Duration   int    `json:"duration"` // seconds
	Rank       int    `json:"rank"`
	Preview    string `json:"preview"`
	Link       string `json:"link"`
	TrackPos   int    `json:"track_position"`
	DiskNumber int    `json:"disk_number"`
	Explicit   bool   `json:"explicit_lyrics"`
	ReleaseDate string `json:"release_date"`
	Artist     struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		ID        int    `json:"id"`
		Title     string `json:"title"`
		CoverXL   string `json:"cover_xl"`
		CoverBig  string `json:"cover_big"`
		CoverMed  string `json:"cover_medium"`
		NbTracks  int    `json:"nb_tracks"`
	} `json:"album"`
	Contributors []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"contributors"`
}

// Album represents a Deezer album from the public API.
type Album struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	NbTracks    int    `json:"nb_tracks"`
	RecordType  string `json:"record_type"` // album, single, ep, compile
	Explicit    bool   `json:"explicit_lyrics"`
	Link        string `json:"link"`
	CoverXL     string `json:"cover_xl"`
	CoverBig    string `json:"cover_big"`
	CoverMed    string `json:"cover_medium"`
	Artist      struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
	Genres struct {
		Data []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	} `json:"genres"`
}

// Artist represents a Deezer artist from the public API.
type Artist struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	NbFan     int    `json:"nb_fan"`
	PictureXL string `json:"picture_xl"`
	PictureBig string `json:"picture_big"`
	PictureMed string `json:"picture_medium"`
	Link      string `json:"link"`
}

// Client provides access to Deezer's public metadata API.
type Client struct {
	cfg         DeezerConfig
	httpClient  *http.Client
	accessToken string
	log         *slog.Logger

	// Rate limiting.
	lastCall   time.Time
	minInterval time.Duration
}

// New creates a Deezer metadata API client.
func New(cfg DeezerConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		accessToken: cfg.AccessToken,
		log:         logger,
		minInterval: time.Second, // Deezer soft limit: ~50 req/5s
	}
}

// ─── Track search ───────────────────────────────────────────────────

// SearchTracks searches for tracks matching a query.
// Supports advanced syntax: pass track + artist for field-scoped search.
func (c *Client) SearchTracks(ctx context.Context, query string, limit int) ([]Track, error) {
	return c.searchTracks(ctx, query, "", "", limit)
}

// SearchTracksAdvanced uses field-scoped search (track:"X" artist:"Y").
func (c *Client) SearchTracksAdvanced(ctx context.Context, trackName, artistName, albumName string, limit int) ([]Track, error) {
	q := buildAdvancedQuery(trackName, artistName, albumName)
	return c.searchTracks(ctx, q, trackName, artistName, limit)
}

func (c *Client) searchTracks(ctx context.Context, query, track, artist string, limit int) ([]Track, error) {
	if query == "" && track != "" {
		query = fmt.Sprintf(`track:"%s" artist:"%s"`, track, artist)
	}

	data, err := c.apiGet(ctx, "search/track", map[string]string{
		"q":     query,
		"limit": strconv.Itoa(min(limit, 100)),
	})
	if err != nil {
		c.log.Error("search tracks failed", "error", err, "query", query, "component", "deezer_api")
		return nil, err
	}

	var result struct {
		Data []Track `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("unmarshal search tracks failed", "error", err, "component", "deezer_api")
		return nil, err
	}
	return result.Data, nil
}

// ─── Artist search ──────────────────────────────────────────────────

// SearchArtists searches for artists by name.
func (c *Client) SearchArtists(ctx context.Context, query string, limit int) ([]Artist, error) {
	data, err := c.apiGet(ctx, "search/artist", map[string]string{
		"q":     query,
		"limit": strconv.Itoa(min(limit, 100)),
	})
	if err != nil {
		c.log.Error("search artists failed", "error", err, "query", query, "component", "deezer_api")
		return nil, err
	}
	var result struct {
		Data []Artist `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("unmarshal search artists failed", "error", err, "component", "deezer_api")
		return nil, err
	}
	return result.Data, nil
}

// ─── Album search ───────────────────────────────────────────────────

// SearchAlbums searches for albums by query (artist + album title).
func (c *Client) SearchAlbums(ctx context.Context, query string, limit int) ([]Album, error) {
	data, err := c.apiGet(ctx, "search/album", map[string]string{
		"q":     query,
		"limit": strconv.Itoa(min(limit, 100)),
	})
	if err != nil {
		c.log.Error("search albums failed", "error", err, "query", query, "component", "deezer_api")
		return nil, err
	}
	var result struct {
		Data []Album `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("unmarshal search albums failed", "error", err, "component", "deezer_api")
		return nil, err
	}
	return result.Data, nil
}

// ─── Entity detail ──────────────────────────────────────────────────

// GetTrack returns full track details by Deezer ID.
func (c *Client) GetTrack(ctx context.Context, trackID int) (*Track, error) {
	data, err := c.apiGet(ctx, fmt.Sprintf("track/%d", trackID), nil)
	if err != nil {
		c.log.Error("get track failed", "error", err, "trackID", trackID, "component", "deezer_api")
		return nil, err
	}
	var track Track
	if err := json.Unmarshal(data, &track); err != nil {
		c.log.Error("unmarshal track failed", "error", err, "trackID", trackID, "component", "deezer_api")
		return nil, err
	}
	return &track, nil
}

// GetAlbum returns full album details by Deezer ID.
func (c *Client) GetAlbum(ctx context.Context, albumID int) (*Album, error) {
	data, err := c.apiGet(ctx, fmt.Sprintf("album/%d", albumID), nil)
	if err != nil {
		c.log.Error("get album failed", "error", err, "albumID", albumID, "component", "deezer_api")
		return nil, err
	}
	var album Album
	if err := json.Unmarshal(data, &album); err != nil {
		c.log.Error("unmarshal album failed", "error", err, "albumID", albumID, "component", "deezer_api")
		return nil, err
	}
	return &album, nil
}

// GetAlbumTracks returns all tracks for an album.
func (c *Client) GetAlbumTracks(ctx context.Context, albumID int) ([]Track, error) {
	data, err := c.apiGet(ctx, fmt.Sprintf("album/%d/tracks", albumID), map[string]string{
		"limit": "500",
	})
	if err != nil {
		c.log.Error("get album tracks failed", "error", err, "albumID", albumID, "component", "deezer_api")
		return nil, err
	}
	var result struct {
		Data []Track `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("unmarshal album tracks failed", "error", err, "albumID", albumID, "component", "deezer_api")
		return nil, err
	}
	return result.Data, nil
}

// GetArtist returns full artist details by Deezer ID.
func (c *Client) GetArtist(ctx context.Context, artistID int) (*Artist, error) {
	data, err := c.apiGet(ctx, fmt.Sprintf("artist/%d", artistID), nil)
	if err != nil {
		c.log.Error("get artist failed", "error", err, "artistID", artistID, "component", "deezer_api")
		return nil, err
	}
	var artist Artist
	if err := json.Unmarshal(data, &artist); err != nil {
		c.log.Error("unmarshal artist failed", "error", err, "artistID", artistID, "component", "deezer_api")
		return nil, err
	}
	return &artist, nil
}

// GetArtistAlbums returns all albums for an artist, paginated.
func (c *Client) GetArtistAlbums(ctx context.Context, artistID, limit int) ([]Album, error) {
	var albums []Album
	offset := 0
	for offset < limit {
		fetchLimit := min(100, limit-offset)
		data, err := c.apiGet(ctx, fmt.Sprintf("artist/%d/albums", artistID), map[string]string{
			"limit": strconv.Itoa(fetchLimit),
			"index": strconv.Itoa(offset),
		})
		if err != nil {
			c.log.Error("get artist albums failed", "error", err, "artistID", artistID, "component", "deezer_api")
			return albums, err
		}
		var result struct {
			Data []Album `json:"data"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			c.log.Error("unmarshal artist albums failed", "error", err, "artistID", artistID, "component", "deezer_api")
			return albums, err
		}
		albums = append(albums, result.Data...)
		if len(result.Data) < fetchLimit {
			break
		}
		offset += len(result.Data)
	}
	return albums, nil
}

// GetArtistTopTracks returns an artist's most popular tracks.
func (c *Client) GetArtistTopTracks(ctx context.Context, artistID, limit int) ([]Track, error) {
	data, err := c.apiGet(ctx, fmt.Sprintf("artist/%d/top", artistID), map[string]string{
		"limit": strconv.Itoa(limit),
	})
	if err != nil {
		c.log.Error("get artist top tracks failed", "error", err, "artistID", artistID, "component", "deezer_api")
		return nil, err
	}
	var result struct {
		Data []Track `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		c.log.Error("unmarshal artist top tracks failed", "error", err, "artistID", artistID, "component", "deezer_api")
		return nil, err
	}
	return result.Data, nil
}

// ─── Conversion helpers ─────────────────────────────────────────────

// ToTrackResult converts a Deezer Track to a domain.TrackResult for download search.
func (t Track) ToTrackResult(quality string) domain.TrackResult {
	durationSec := t.Duration
	artistName := t.Artist.Name
	if len(t.Contributors) > 1 {
		names := make([]string, len(t.Contributors))
		for i, c := range t.Contributors {
			names[i] = c.Name
		}
		artistName = strings.Join(names, ", ")
	}

	// Estimate size based on quality.
	var estSize int64
	var bitrate int
	switch quality {
	case "flac":
		estSize = int64(durationSec * 176400) // ~1411kbps
		bitrate = 1411
	case "mp3_320":
		estSize = int64(durationSec * 40000) // ~320kbps
		bitrate = 320
	default:
		estSize = int64(durationSec * 16000) // ~128kbps
		bitrate = 128
	}

	return domain.TrackResult{
		SearchResult: domain.SearchResult{
			Username:        "",
			Filename:        fmt.Sprintf("%d||%s - %s", t.ID, artistName, t.Title),
			Size:            estSize,
			Bitrate:         bitrate,
			Duration:        int64(t.Duration * 1000),
			Quality:         mapQuality(quality),
			FreeUploadSlots: 999,
			UploadSpeed:     999999,
		},
		Artist:      artistName,
		Title:       t.Title,
		Album:       t.Album.Title,
		TrackNumber: t.TrackPos,
		CoverURL:    t.Album.CoverXL,
	}
}

// ─── Internal ───────────────────────────────────────────────────────

func (c *Client) apiGet(ctx context.Context, endpoint string, params map[string]string) (json.RawMessage, error) {
	// Rate limit.
	elapsed := time.Since(c.lastCall)
	if elapsed < c.minInterval {
		select {
		case <-time.After(c.minInterval - elapsed):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c.lastCall = time.Now()

	u, err := url.Parse(baseURL + "/" + strings.TrimLeft(endpoint, "/"))
	if err != nil {
		c.log.Error("deezer api URL parse failed", "error", err, "endpoint", endpoint, "component", "deezer_api")
		return nil, err
	}

	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	if c.accessToken != "" {
		q.Set("access_token", c.accessToken)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		c.log.Error("deezer api create request failed", "error", err, "endpoint", endpoint, "component", "deezer_api")
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Error("deezer api HTTP request failed", "error", err, "endpoint", endpoint, "component", "deezer_api")
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Error("deezer api read response failed", "error", err, "endpoint", endpoint, "component", "deezer_api")
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		c.log.Error("deezer api non-OK status", "status", resp.StatusCode, "body", string(body)[:min(len(string(body)), 200)], "component", "deezer_api")
		return nil, fmt.Errorf("deezer API HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Check for API-level errors.
	var errResp struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		if errResp.Error.Type == "DataException" {
			return nil, fmt.Errorf("deezer: not found")
		}
		return nil, fmt.Errorf("deezer API error (%s): %s", errResp.Error.Type, errResp.Error.Message)
	}

	return json.RawMessage(body), nil
}

func buildAdvancedQuery(track, artist, album string) string {
	var parts []string
	if track != "" {
		parts = append(parts, fmt.Sprintf(`track:"%s"`, track))
	}
	if artist != "" {
		parts = append(parts, fmt.Sprintf(`artist:"%s"`, artist))
	}
	if album != "" {
		parts = append(parts, fmt.Sprintf(`album:"%s"`, album))
	}
	return strings.Join(parts, " ")
}

func mapQuality(q string) string {
	switch q {
	case "flac":
		return "flac"
	case "mp3_320":
		return "mp3"
	default:
		return "mp3"
	}
}
