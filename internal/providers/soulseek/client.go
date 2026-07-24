// Package soulseek implements a download plugin for slskd (Soulseek daemon REST API).
// It communicates with a local slskd instance over HTTP, not the raw Soulseek protocol.
package soulseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/provider"
	"github.com/ramonskie/groovearr/internal/quality"
)

const pluginName = "soulseek"
const displayName = "Soulseek"

// soulseekRate limits outgoing requests to slskd to 10 req/s.
const soulseekRate = 10

// SoulseekConfig holds slskd connection and search parameters.
type SoulseekConfig struct {
	SlskdURL       string `json:"slskd_url"`
	APIKey         string `json:"api_key"`
	SearchTimeout  int    `json:"search_timeout"`
	MinUploadSpeed int    `json:"min_upload_speed"`
}

// Client implements download.Plugin for Soulseek via slskd REST API.
type Client struct {
	cfg      SoulseekConfig
	dlPath   string // download staging directory (not from Soulseek config)
	baseURL  string
	apiKey   string
	client   *http.Client
	log      *slog.Logger

	mu             sync.Mutex
	activeSearches map[string]context.CancelFunc // searchID → cancel
	downloadsMu       sync.RWMutex
	downloads         map[string]*domain.DownloadRecord // downloadID → record
	downloadUsernames map[string]string                 // downloadID → username
}

// New creates a Soulseek client from a raw JSON config blob, download path, and logger.
// The raw config is unmarshalled into a local SoulseekConfig.
func New(cfg json.RawMessage, downloadPath string, logger *slog.Logger) (*Client, error) {
	var sc SoulseekConfig
	if err := json.Unmarshal(cfg, &sc); err != nil {
		return nil, fmt.Errorf("soulseek: invalid config: %w", err)
	}
	return &Client{
		cfg:            sc,
		dlPath:         downloadPath,
		baseURL:        strings.TrimRight(sc.SlskdURL, "/"),
		apiKey:         sc.APIKey,
		client: &http.Client{Timeout: 120 * time.Second, Transport: provider.NewRateLimitedTransport(http.DefaultTransport, soulseekRate)},
		log:            logger,
		activeSearches: make(map[string]context.CancelFunc),
		downloads:         make(map[string]*domain.DownloadRecord),
		downloadUsernames: make(map[string]string),
	}, nil
}

// Name returns the canonical plugin name.
func (c *Client) Name() string { return pluginName }

// DisplayName returns a human-readable label.
func (c *Client) DisplayName() string { return displayName }

// IsConfigured returns true if slskd URL and API key are both set.
func (c *Client) IsConfigured() bool {
	return c.baseURL != "" && c.apiKey != ""
}

// CapabilityStatus returns download status derived from IsConfigured.
func (c *Client) CapabilityStatus() map[string]string {
	s := "not_configured"
	if c.IsConfigured() {
		s = "configured"
		if c.Connected() {
			s = "connected"
		}
	}
	return map[string]string{"download": s}
}

// CheckConnection probes the slskd API for reachability.
func (c *Client) CheckConnection(ctx context.Context) error {
	if !c.IsConfigured() {
		return fmt.Errorf("soulseek: slskd URL not configured")
	}
	_, err := c.doRequest(ctx, http.MethodGet, "application", nil)
	return err
}

// Search queries slskd and returns matching tracks and albums.
func (c *Client) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	return c.search(ctx, query, c.cfg.SearchTimeout, nil)
}

// SearchWithProgress implements download.SearchPlugin.
func (c *Client) SearchWithProgress(ctx context.Context, query string, cb func(tracks []domain.TrackResult, albums []domain.AlbumResult, responseCount int)) ([]domain.TrackResult, []domain.AlbumResult, error) {
	return c.search(ctx, query, c.cfg.SearchTimeout, cb)
}

func (c *Client) search(ctx context.Context, query string, timeoutSec int, cb func([]domain.TrackResult, []domain.AlbumResult, int)) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if !c.IsConfigured() {
		return nil, nil, fmt.Errorf("soulseek: not configured")
	}
	if timeoutSec < 1 {
		timeoutSec = 60
	}

	searchReq := map[string]any{
		"searchText":              query,
		"timeout":                 timeoutSec * 1000, // slskd expects milliseconds
		"filterResponses":         false,
		"minimumResponseFileCount": 1,
		"minimumPeerUploadSpeed":  c.cfg.MinUploadSpeed * 125000, // Mbps → bytes/sec
	}

	body, _ := json.Marshal(searchReq)
	resp, err := c.doRequest(ctx, http.MethodPost, "searches", bytes.NewReader(body))
	if err != nil {
		c.log.Error("soulseek search request failed", "error", err, "query", query, "component", "soulseek")
		return nil, nil, fmt.Errorf("soulseek search: %w", err)
	}

	searchID := extractID(resp)
	if searchID == "" {
		return nil, nil, fmt.Errorf("soulseek search: no search ID returned")
	}

	// Register for cancellation.
	searchCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec+15)*time.Second)
	defer cancel()

	c.mu.Lock()
	c.activeSearches[searchID] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.activeSearches, searchID)
		c.mu.Unlock()
	}()

	// Poll for results.
	var allTracks []domain.TrackResult
	var allAlbums []domain.AlbumResult
	var responseCount int
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-searchCtx.Done():
			return allTracks, allAlbums, nil
		case <-ticker.C:
			respData, err := c.doRequest(searchCtx, http.MethodGet, "searches/"+searchID+"/responses", nil)
			if err != nil {
				continue // keep polling
			}

			responses := parseSearchResponses(respData)
			if len(responses) <= responseCount {
				if responseCount > 0 {
					return allTracks, allAlbums, nil // search finished
				}
				continue
			}

			newResponses := responses[responseCount:]
			responseCount = len(responses)
			tracks, albums := processResponses(newResponses)
			allTracks = append(allTracks, tracks...)
			allAlbums = append(allAlbums, albums...)

			if cb != nil {
				cb(allTracks, allAlbums, responseCount)
			}

			if responseCount >= 30 {
				return allTracks, allAlbums, nil // early termination
			}
		}
	}
}

// Download enqueues a file for download via slskd.
func (c *Client) Download(ctx context.Context, username, filename string, fileSize int64) (string, error) {
	if !c.IsConfigured() {
		return "", fmt.Errorf("soulseek: not configured")
	}

	downloadReq := []map[string]any{{
		"filename": filename,
		"size":     fileSize,
		"path":     c.dlPath,
	}}

	body, _ := json.Marshal(downloadReq)
	endpoint := fmt.Sprintf("transfers/downloads/%s", url.PathEscape(username))
	resp, err := c.doRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		c.log.Error("soulseek download request failed", "error", err, "username", username, "filename", filename, "component", "soulseek")
		return "", fmt.Errorf("soulseek download: %w", err)
	}

	downloadID := extractDownloadID(resp, filename)
	if downloadID == "" {
		return "", fmt.Errorf("soulseek download: no download ID returned")
	}

	// If slskd returned the filename as fallback (non-JSON response), resolve
	// the real UUID from the downloads list by matching the filename.
	if downloadID == filename {
		if realID := c.findDownloadIDByFilename(ctx, filename); realID != "" {
			downloadID = realID
		}
	}

	record := &domain.DownloadRecord{
		ID:         downloadID,
		SourceName: pluginName,
		Filename:   filename,
		State:      domain.DownloadQueued,
	}
	c.downloadsMu.Lock()
	c.downloads[downloadID] = record
	c.downloadUsernames[downloadID] = username
	c.downloadsMu.Unlock()

	return downloadID, nil
}

// findDownloadIDByFilename queries slskd's download list and returns the UUID
// of the first download matching the given filename.
func (c *Client) findDownloadIDByFilename(ctx context.Context, filename string) string {
	listResp, err := c.doRequest(ctx, http.MethodGet, "transfers/downloads", nil)
	if err != nil {
		c.log.Warn("soulseek findDownloadIDByFilename failed", "error", err, "filename", filename, "component", "soulseek")
		return ""
	}
	var users []struct {
		Directories []struct {
			Files []struct {
				ID       string `json:"id"`
				Filename string `json:"filename"`
			} `json:"files"`
		} `json:"directories"`
	}
	if json.Unmarshal(listResp, &users) != nil {
		return ""
	}
	for _, u := range users {
		for _, d := range u.Directories {
			for _, f := range d.Files {
				if f.Filename == filename {
					return f.ID
				}
			}
		}
	}
	return ""
}

// GetDownloads returns all tracked downloads for this source.
func (c *Client) GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error) {
	// Refresh from slskd API.
	resp, err := c.doRequest(ctx, http.MethodGet, "transfers/downloads", nil)
	if err != nil {
		c.log.Error("soulseek get downloads request failed, returning cache", "error", err, "component", "soulseek")
		// Return cached records if API fails.
		c.downloadsMu.RLock()
		defer c.downloadsMu.RUnlock()
		out := make([]domain.DownloadRecord, 0, len(c.downloads))
		for _, r := range c.downloads {
			out = append(out, *r)
		}
		return out, nil
	}

	records := parseDownloadStatus(resp, c.dlPath, c.log)
	c.downloadsMu.Lock()
	for _, r := range records {
		c.downloads[r.ID] = &r
	}
	c.downloadsMu.Unlock()

	return records, nil
}

// GetDownloadStatus returns a single download's status.
// Tries username-based endpoint first (works for all states), falls back
// to ID-only endpoint, then list endpoint as last resort.
func (c *Client) GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error) {
	// Try username-based endpoint first — it returns the full record for all
	// download states unlike the ID-only endpoint which 404s for completed
	// transfers.
	c.downloadsMu.RLock()
	username := c.downloadUsernames[downloadID]
	c.downloadsMu.RUnlock()

	if username != "" {
		endpoint := fmt.Sprintf("transfers/downloads/%s/%s",
			url.PathEscape(username), url.PathEscape(downloadID))
		if rec, err := c.trySingleEndpoint(ctx, endpoint, downloadID); err == nil {
			return rec, nil
		}
	}

	// Fallback 1: ID-only endpoint.
	if rec, err := c.trySingleEndpoint(ctx, "transfers/downloads/"+url.PathEscape(downloadID), downloadID); err == nil {
		return rec, nil
	}

	// Fallback 2: list all downloads and search by ID.
	c.log.Warn("single-download endpoint failed, trying list fallback", "downloadID", downloadID, "component", "soulseek")
	if listResp, listErr := c.doRequest(ctx, http.MethodGet, "transfers/downloads", nil); listErr == nil {
		records := parseDownloadStatus(listResp, c.dlPath, c.log)
		for _, rec := range records {
			if rec.ID == downloadID {
				c.log.Info("found match in list", "downloadID", downloadID, "state", rec.State, "component", "soulseek")
				c.cacheRecord(&rec)
				return &rec, nil
			}
		}
		c.log.Warn("download not found in list", "downloadID", downloadID, "checked", len(records), "component", "soulseek")
	} else {
		c.log.Warn("list fallback failed", "error", listErr, "component", "soulseek")
	}

	// Last resort: cached record.
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()
	if r, ok := c.downloads[downloadID]; ok {
		c.log.Debug("returning cached record", "downloadID", downloadID, "state", r.State, "component", "soulseek")
		return r, nil
	}

	return nil, fmt.Errorf("soulseek: download %s not found", downloadID)
}

// trySingleEndpoint fetches a single download record from the given slskd API
// endpoint and parses the flat JSON object response.
func (c *Client) trySingleEndpoint(ctx context.Context, endpoint, downloadID string) (*domain.DownloadRecord, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	rec := parseSingleDownload(resp, c.dlPath, c.log)
	if rec == nil {
		return nil, fmt.Errorf("soulseek: empty response for %s", downloadID)
	}
	c.cacheRecord(rec)
	return rec, nil
}

// cacheRecord stores a download record in the in-memory cache.
func (c *Client) cacheRecord(r *domain.DownloadRecord) {
	c.downloadsMu.Lock()
	c.downloads[r.ID] = r
	c.downloadsMu.Unlock()
}

// CancelDownload cancels an active download. Tries multiple endpoint formats for slskd compatibility.
func (c *Client) CancelDownload(ctx context.Context, downloadID string, remove bool) error {
	// First, try to find the username from cached records.
	username := ""
	c.downloadsMu.RLock()
	if u, ok := c.downloadUsernames[downloadID]; ok {
		username = u
	}
	c.downloadsMu.RUnlock()

	// Try username-based endpoint (primary format).
	if username != "" {
		endpoint := fmt.Sprintf("transfers/downloads/%s/%s?remove=%t",
			url.PathEscape(username), url.PathEscape(downloadID), remove)
		if _, err := c.doRequest(ctx, http.MethodDelete, endpoint, nil); err == nil {
			if remove {
				c.downloadsMu.Lock()
				delete(c.downloads, downloadID)
				delete(c.downloadUsernames, downloadID)
				c.downloadsMu.Unlock()
			}
			return nil
		}
	}

	// Fallback: direct download ID (simpler slskd versions).
	endpoint := fmt.Sprintf("transfers/downloads/%s?remove=%t", url.PathEscape(downloadID), remove)
	_, err := c.doRequest(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		c.log.Error("soulseek cancel download failed (fallback)", "error", err, "downloadID", downloadID, "component", "soulseek")
		return fmt.Errorf("soulseek cancel: %w", err)
	}

	if remove {
		c.downloadsMu.Lock()
		delete(c.downloads, downloadID)
		delete(c.downloadUsernames, downloadID)
		c.downloadsMu.Unlock()
	}
	return nil
}

// ClearCompleted removes all terminal-state downloads from tracking.
func (c *Client) ClearCompleted(ctx context.Context) error {
	c.downloadsMu.Lock()
	defer c.downloadsMu.Unlock()
	for id, r := range c.downloads {
		if r.State.Terminal() {
			delete(c.downloads, id)
			delete(c.downloadUsernames, id)
		}
	}
	return nil
}

// Connected returns true if configured (Soulseek has no separate auth step).
func (c *Client) Connected() bool { return c.IsConfigured() }

// GetProgress implements download.DownloadProgressor by delegating to the
// slskd API for current transfer state.
func (c *Client) GetProgress(ctx context.Context, downloadID string) (*download.Progress, error) {
	status, err := c.GetDownloadStatus(ctx, downloadID)
	if err != nil {
		return nil, err
	}
	return &download.Progress{
		DownloadID:  downloadID,
		Transferred: status.Transferred,
		Total:       status.Size,
		Speed:       status.Speed,
	}, nil
}

// doRequest makes an HTTP request to slskd's /api/v0/ endpoint.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body io.Reader) (json.RawMessage, error) {
	u := c.baseURL + "/api/v0/" + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		c.log.Error("soulseek create request failed", "error", err, "method", method, "endpoint", endpoint, "component", "soulseek")
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.log.Error("soulseek HTTP request failed", "error", err, "method", method, "endpoint", endpoint, "component", "soulseek")
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Error("soulseek read response failed", "error", err, "method", method, "endpoint", endpoint, "component", "soulseek")
		return nil, err
	}

	if resp.StatusCode >= 400 {
		c.log.Error("soulseek non-OK status", "status", resp.StatusCode, "body", string(respBody)[:min(len(string(respBody)), 200)], "component", "soulseek")
		return nil, fmt.Errorf("slskd HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return json.RawMessage(respBody), nil
}

// ─── Response parsing helpers ───────────────────────────────────────

// audioExtensions are file extensions considered audio files.
var audioExtensions = map[string]bool{
	"mp3": true, "flac": true, "ogg": true, "aac": true,
	"wma": true, "wav": true, "m4a": true,
}

// slskdToAudioQuality maps slskd file metadata to a quality.AudioQuality descriptor.
// slskd provides bitrate and filename (which contains the extension).
// SampleRate and BitDepth are left zero for Soulseek FLAC files —
// TierScore uses a kbps heuristic to estimate hi-res quality from bitrate alone.
func slskdToAudioQuality(filename string, bitrate int) quality.AudioQuality {
	ext := strings.TrimPrefix(path.Ext(filename), ".")
	format := strings.ToLower(ext)

	return quality.AudioQuality{
		Format:  format,
		Bitrate: bitrate,
	}
}

func extractID(raw json.RawMessage) string {
	// Try dict.
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if id, ok := m["id"]; ok {
			return fmt.Sprint(id)
		}
	}
	// Try list.
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		if id, ok := arr[0]["id"]; ok {
			return fmt.Sprint(id)
		}
	}
	return ""
}

func extractDownloadID(raw json.RawMessage, fallback string) string {
	// slskd returns: {"enqueued":[{"id":"...","filename":"..."}],"failed":[]}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if enqueued, ok := m["enqueued"].([]any); ok && len(enqueued) > 0 {
			if entry, ok := enqueued[0].(map[string]any); ok {
				if id, ok := entry["id"]; ok {
					return fmt.Sprint(id)
				}
			}
		}
		// Flat id field (some slskd versions).
		if id, ok := m["id"]; ok {
			return fmt.Sprint(id)
		}
	}
	return fallback
}

func parseSearchResponses(raw json.RawMessage) []map[string]any {
	var responses []map[string]any
	json.Unmarshal(raw, &responses)
	return responses
}

// ─── Filename parsing helpers ────────────────────────────────────────
//
// NOTE: Soulseek provides no structured metadata — the filename IS the metadata.
// Artist matching for download selection is handled by the matching engine's
// word-boundary search on the full path (ScoreTrackMatchWithPath). The parsed
// artist/title fields here are display hints only.

// Tiered regex patterns for filename parsing, compiled once at init.
var (
	filenameTier1RE = regexp.MustCompile(`^(\d{1,3})[\.\s\-]+(.+?)\s+-\s+(.+)$`)
	filenameTier2RE = regexp.MustCompile(`^(.+?)\s+-\s+(.+)$`)
	filenameTier3RE = regexp.MustCompile(`^(\d{1,3})[\.\s\-]+(.+)$`)
	yearInPathRE    = regexp.MustCompile(`(19\d{2}|20\d{2})`)
)

// parseFilename parses a Soulseek file path using 3-tier regex patterns.
// Strips the file extension and applies patterns against the basename only.
// Returns artist, title, and track number; empty strings/zero when no pattern matches.
// Artist parsing is conservative — only "Artist - Title" basename pattern yields artist.
// Path-based artist matching is handled by the matching engine (ScoreTrackMatchWithPath).
func parseFilename(filename string) (artist, title string, trackNum int) {
	normalized := strings.ReplaceAll(filename, "\\", "/")
	base := strings.TrimSuffix(path.Base(normalized), path.Ext(normalized))

	if m := filenameTier1RE.FindStringSubmatch(base); m != nil {
		trackNum, _ = strconv.Atoi(m[1])
		artist = strings.TrimSpace(m[2])
		title = strings.TrimSpace(m[3])
		return
	}

	// Tier 3 before Tier 2: "01 - Title" must NOT be parsed as artist="01".
	if m := filenameTier3RE.FindStringSubmatch(base); m != nil {
		trackNum, _ = strconv.Atoi(m[1])
		title = strings.TrimSpace(m[2])
		return
	}

	if m := filenameTier2RE.FindStringSubmatch(base); m != nil {
		artist = strings.TrimSpace(m[1])
		title = strings.TrimSpace(m[2])
		return
	}

	return
}

// parseYearFromPath extracts a 4-digit year (19xx or 20xx) from a file path.
// Returns the first match or an empty string.
func parseYearFromPath(filename string) string {
	if m := yearInPathRE.FindStringSubmatch(filename); m != nil {
		return m[1]
	}
	return ""
}

func processResponses(responses []map[string]any) ([]domain.TrackResult, []domain.AlbumResult) {
	type albumKey struct {
		username string
		path     string
	}

	var tracks []domain.TrackResult
	albumsByPath := make(map[albumKey][]domain.TrackResult)

	for _, resp := range responses {
		username, _ := resp["username"].(string)
		files, _ := resp["files"].([]any)

		for _, f := range files {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			filename, _ := fm["filename"].(string)
			ext := strings.TrimPrefix(path.Ext(filename), ".")
			if !audioExtensions[strings.ToLower(ext)] {
				continue
			}

			// Parse metadata from filename using 3-tier regex patterns.
			artist, trackTitle, trackNum := parseFilename(filename)

			durationSec, _ := fm["length"].(float64)
			tr := domain.TrackResult{
				SearchResult: domain.SearchResult{
					Username:        username,
					Filename:        filename,
					Size:            int64(getFloat(fm, "size")),
					Bitrate:         int(getFloat(fm, "bitRate")),
					Duration:        int64(durationSec * 1000),
					Quality:         strings.ToLower(ext),
					AudioQuality:    slskdToAudioQuality(filename, int(getFloat(fm, "bitRate"))),
					FreeUploadSlots: int(getFloat(fm, "freeUploadSlots")),
					UploadSpeed:     int64(getFloat(fm, "uploadSpeed")),
					QueueLength:     int(getFloat(fm, "queueLength")),
				},
				Title:       trackTitle,
				Artist:      artist,
				TrackNumber: trackNum,
			}

			// Populate structured metadata from filename parsing.
			// Soulseek has no structured metadata — everything comes from the filename.
			albumPath := extractAlbumPath(filename)
			yearStr := parseYearFromPath(filename)
			// Note: albumName/year intentionally NOT used — Soulseek directory names
			// are user-created junk, not reliable metadata.
			_ = yearStr
			_ = albumPath
			tr.Metadata = &domain.TrackMetadata{
				Artist: artist,
				Title:  trackTitle,
				// Album/Year intentionally NOT populated from Soulseek directory names —
				// they are user-created junk, not reliable metadata.
			}

			tracks = append(tracks, tr)

			// Group by album path.
			if albumPath != "" {
				key := albumKey{username: username, path: albumPath}
				albumsByPath[key] = append(albumsByPath[key], tr)
			}
		}
	}

	// Build albums from groups (2+ tracks).
	var albums []domain.AlbumResult
	for key, albumTracks := range albumsByPath {
		if len(albumTracks) < 2 {
			continue
		}
		var totalSize int64
		qualityCounts := map[string]int{}
		for _, t := range albumTracks {
			totalSize += t.Size
			qualityCounts[t.AudioQuality.Format]++
		}
		dominantQuality := ""
		maxCount := 0
		for q, c := range qualityCounts {
			if c > maxCount {
				maxCount = c
				dominantQuality = q
			}
		}

		// Parse artist, album title, and year from the album path.
		var albumArtist string
		if len(albumTracks) > 0 && albumTracks[0].Artist != "" {
			albumArtist = albumTracks[0].Artist
		}
		// Use the directory's last segment as the album title.
		albumPathSegments := strings.Split(strings.ReplaceAll(key.path, "\\", "/"), "/")
		albumTitle := albumPathSegments[len(albumPathSegments)-1]
		albumYear := parseYearFromPath(key.path)

		albums = append(albums, domain.AlbumResult{
			Username:        key.username,
			AlbumPath:       key.path,
			AlbumTitle:      albumTitle,
			Artist:          albumArtist,
			Year:            albumYear,
			TrackCount:      len(albumTracks),
			TotalSize:       totalSize,
			Tracks:          albumTracks,
			DominantQuality: dominantQuality,
			FreeUploadSlots: albumTracks[0].FreeUploadSlots,
			UploadSpeed:     albumTracks[0].UploadSpeed,
			QueueLength:     albumTracks[0].QueueLength,
		})
	}

	// Remove album tracks from individual results.
	albumTrackFiles := make(map[string]bool)
	for _, a := range albums {
		for _, t := range a.Tracks {
			albumTrackFiles[t.Filename] = true
		}
	}
	filtered := tracks[:0]
	for _, t := range tracks {
		if !albumTrackFiles[t.Filename] {
			filtered = append(filtered, t)
		}
	}

	return filtered, albums
}

func extractAlbumPath(filename string) string {
	// Normalize separators.
	normalized := strings.ReplaceAll(filename, "\\", "/")
	parts := strings.Split(normalized, "/")
	if len(parts) < 2 {
		return ""
	}
	// Return everything except the filename.
	return strings.Join(parts[:len(parts)-1], "/")
}

// parseSingleDownload parses a single download record from the slskd
// single-download endpoint (flat JSON object, not wrapped in user/directory).
func parseSingleDownload(raw json.RawMessage, downloadPath string, log *slog.Logger) *domain.DownloadRecord {
	var f struct {
		ID               string  `json:"id"`
		Filename         string  `json:"filename"`
		State            string  `json:"state"`
		Size             int64   `json:"size"`
		BytesTransferred int64   `json:"bytesTransferred"`
		AverageSpeed     float64 `json:"averageSpeed"`
		PercentComplete  float64 `json:"percentComplete"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Error("parseSingleDownload unmarshal error", "error", err, "component", "soulseek")
		return nil
	}
	if f.ID == "" {
		return nil
	}

	normalizedFilename := strings.ReplaceAll(f.Filename, "\\", "/")
	filePath := ""
	if normalizedFilename != "" && downloadPath != "" {
		filePath = filepath.Join(downloadPath, slskdOutputPath(normalizedFilename))
	}

	return &domain.DownloadRecord{
		ID:          f.ID,
		SourceName:  pluginName,
		Filename:    normalizedFilename,
		State:       parseState(f.State),
		Progress:    f.PercentComplete,
		Size:        f.Size,
		Transferred: f.BytesTransferred,
		Speed:       int64(f.AverageSpeed),
		FilePath:    filePath,
	}
}

// slskdOutputPath computes the actual output path slskd uses when moving a
// completed download from the incomplete directory. slskd keeps only the last
// two path segments (parent directory + filename) from the original remote path.
//
// Example: "Music/Artist/Album/track.flac" → "Album/track.flac"
func slskdOutputPath(filename string) string {
	parts := strings.Split(filename, "/")
	if len(parts) >= 2 {
		return filepath.Join(parts[len(parts)-2], parts[len(parts)-1])
	}
	return filename
}

func parseDownloadStatus(raw json.RawMessage, downloadPath string, log *slog.Logger) []domain.DownloadRecord {
	var users []struct {
		Username    string `json:"username"`
		Directories []struct {
			Files []struct {
				ID               string  `json:"id"`
				Filename         string  `json:"filename"`
				State            string  `json:"state"`
				Size             int64   `json:"size"`
				BytesTransferred int64   `json:"bytesTransferred"`
				AverageSpeed     float64 `json:"averageSpeed"`
				PercentComplete  float64 `json:"percentComplete"`
			} `json:"files"`
		} `json:"directories"`
	}

	if err := json.Unmarshal(raw, &users); err != nil {
		log.Error("parseDownloadStatus unmarshal error", "error", err, "component", "soulseek")
		return nil
	}

	var records []domain.DownloadRecord
	for _, user := range users {
		for _, dir := range user.Directories {
			for _, f := range dir.Files {
				// Normalize Windows backslashes to forward slashes.
				normalizedFilename := strings.ReplaceAll(f.Filename, "\\", "/")
				filePath := ""
				if normalizedFilename != "" && downloadPath != "" {
					filePath = filepath.Join(downloadPath, slskdOutputPath(normalizedFilename))
				}
				records = append(records, domain.DownloadRecord{
					ID:          f.ID,
					SourceName:  pluginName,
					Filename:    normalizedFilename,
					State:       parseState(f.State),
					Progress:    f.PercentComplete,
					Size:        f.Size,
					Transferred: f.BytesTransferred,
					Speed:       int64(f.AverageSpeed),
					FilePath:    filePath,
				})
			}
		}
	}
	return records
}

// parseState maps slskd's download state strings to canonical domain states.
func parseState(s string) domain.DownloadState {
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "errored") || strings.Contains(lower, "failed") || strings.Contains(lower, "rejected"):
		return domain.DownloadFailed
	case strings.Contains(lower, "cancelled") || strings.Contains(lower, "canceled") || strings.Contains(lower, "aborted"):
		return domain.DownloadIgnored
	case strings.Contains(lower, "completed") || strings.Contains(lower, "succeeded"):
		return domain.DownloadImported
	case strings.Contains(lower, "initializing") || strings.Contains(lower, "queued"):
		return domain.DownloadQueued
	default:
		return domain.DownloadDownloading
	}
}

func getFloat(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}
