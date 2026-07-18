// Package soulseek implements a download plugin for slskd (Soulseek daemon REST API).
// It communicates with a local slskd instance over HTTP, not the raw Soulseek protocol.
package soulseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
)

const pluginName = "soulseek"
const displayName = "Soulseek"

// Client implements download.Plugin for Soulseek via slskd REST API.
type Client struct {
	cfg      config.SoulseekConfig
	baseURL  string
	apiKey   string
	client   *http.Client

	mu             sync.Mutex
	activeSearches map[string]context.CancelFunc // searchID → cancel
	downloadsMu       sync.RWMutex
	downloads         map[string]*domain.DownloadRecord // downloadID → record
	downloadUsernames map[string]string                 // downloadID → username
}

// New creates a Soulseek client with the given config.
func New(cfg config.SoulseekConfig) *Client {
	return &Client{
		cfg:            cfg,
		baseURL:        strings.TrimRight(cfg.SlskdURL, "/"),
		apiKey:         cfg.APIKey,
		client:         &http.Client{Timeout: 120 * time.Second},
		activeSearches: make(map[string]context.CancelFunc),
		downloads:         make(map[string]*domain.DownloadRecord),
		downloadUsernames: make(map[string]string),
	}
}

// Name returns the canonical plugin name.
func (c *Client) Name() string { return pluginName }

// DisplayName returns a human-readable label.
func (c *Client) DisplayName() string { return displayName }

// IsConfigured returns true if slskd URL and API key are both set.
func (c *Client) IsConfigured() bool {
	return c.baseURL != "" && c.apiKey != ""
}

// CheckConnection probes the slskd API for reachability.
func (c *Client) CheckConnection(ctx context.Context) error {
	if !c.IsConfigured() {
		return fmt.Errorf("soulseek: slskd URL not configured")
	}
	_, err := c.doRequest(ctx, http.MethodGet, "", nil)
	return err
}

// Search queries slskd and returns matching tracks and albums.
func (c *Client) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	return c.search(ctx, query, 60, nil)
}

// SearchWithProgress implements download.SearchPlugin.
func (c *Client) SearchWithProgress(ctx context.Context, query string, cb func(tracks []domain.TrackResult, albums []domain.AlbumResult, responseCount int)) ([]domain.TrackResult, []domain.AlbumResult, error) {
	return c.search(ctx, query, 60, cb)
}

func (c *Client) search(ctx context.Context, query string, timeoutSec int, cb func([]domain.TrackResult, []domain.AlbumResult, int)) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if !c.IsConfigured() {
		return nil, nil, fmt.Errorf("soulseek: not configured")
	}

	searchReq := map[string]any{
		"searchText":             query,
		"timeout":                timeoutSec * 1000, // slskd expects milliseconds
		"filterResponses":        true,
		"minimumResponseFileCount": 1,
		"minimumPeerUploadSpeed": c.cfg.MinUploadSpeed * 125000, // Mbps → bytes/sec
	}

	body, _ := json.Marshal(searchReq)
	resp, err := c.doRequest(ctx, http.MethodPost, "searches", bytes.NewReader(body))
	if err != nil {
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
		"path":     c.cfg.DownloadPath,
	}}

	body, _ := json.Marshal(downloadReq)
	endpoint := fmt.Sprintf("transfers/downloads/%s", url.PathEscape(username))
	resp, err := c.doRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("soulseek download: %w", err)
	}

	downloadID := extractDownloadID(resp, filename)
	if downloadID == "" {
		return "", fmt.Errorf("soulseek download: no download ID returned")
	}

	record := &domain.DownloadRecord{
		ID:         downloadID,
		SourceName: pluginName,
		Filename:   filename,
		State:      domain.DownloadInitializing,
	}
	c.downloadsMu.Lock()
	c.downloads[downloadID] = record
	c.downloadUsernames[downloadID] = username
	c.downloadsMu.Unlock()

	return downloadID, nil
}

// GetDownloads returns all tracked downloads for this source.
func (c *Client) GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error) {
	// Refresh from slskd API.
	resp, err := c.doRequest(ctx, http.MethodGet, "transfers/downloads", nil)
	if err != nil {
		// Return cached records if API fails.
		c.downloadsMu.RLock()
		defer c.downloadsMu.RUnlock()
		out := make([]domain.DownloadRecord, 0, len(c.downloads))
		for _, r := range c.downloads {
			out = append(out, *r)
		}
		return out, nil
	}

	records := parseDownloadStatus(resp, c.cfg.DownloadPath)
	c.downloadsMu.Lock()
	for _, r := range records {
		c.downloads[r.ID] = &r
	}
	c.downloadsMu.Unlock()

	return records, nil
}

// GetDownloadStatus returns a single download's status.
func (c *Client) GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "transfers/downloads/"+url.PathEscape(downloadID), nil)
	if err != nil {
		c.downloadsMu.RLock()
		defer c.downloadsMu.RUnlock()
		if r, ok := c.downloads[downloadID]; ok {
			return r, nil
		}
		return nil, fmt.Errorf("soulseek: download %s not found", downloadID)
	}

	records := parseDownloadStatus(resp, c.cfg.DownloadPath)
	if len(records) == 0 {
		return nil, fmt.Errorf("soulseek: download %s not found", downloadID)
	}
	record := records[0]

	c.downloadsMu.Lock()
	c.downloads[record.ID] = &record
	c.downloadsMu.Unlock()

	return &record, nil
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

// doRequest makes an HTTP request to slskd's /api/v0/ endpoint.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body io.Reader) (json.RawMessage, error) {
	u := c.baseURL + "/api/v0/" + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
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
	return fallback
}

func parseSearchResponses(raw json.RawMessage) []map[string]any {
	var responses []map[string]any
	json.Unmarshal(raw, &responses)
	return responses
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

			// Parse metadata from filename.
			trackNum, trackTitle := parseTrackFilename(filename)
			durationSec, _ := fm["length"].(float64)
			tr := domain.TrackResult{
				SearchResult: domain.SearchResult{
					Username:        username,
					Filename:        filename,
					Size:            int64(getFloat(fm, "size")),
					Bitrate:         int(getFloat(fm, "bitRate")),
					Duration:        int64(durationSec * 1000),
					Quality:         strings.ToLower(ext),
					FreeUploadSlots: int(getFloat(fm, "freeUploadSlots")),
					UploadSpeed:     int64(getFloat(fm, "uploadSpeed")),
					QueueLength:     int(getFloat(fm, "queueLength")),
				},
				Title:       trackTitle,
				TrackNumber: trackNum,
			}
			tracks = append(tracks, tr)

			// Group by album path.
			albumPath := extractAlbumPath(filename)
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
			qualityCounts[t.Quality]++
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
		albumArtist, albumTitle, albumYear := parseAlbumDir(key.path)

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

// parseTrackFilename extracts track number and title from a filename like "01 - Song Name.flac".
func parseTrackFilename(filename string) (num int, title string) {
	base := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	re := regexp.MustCompile(`^(\d{1,3})[\.\s\-]+(.+)$`)
	if m := re.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, strings.TrimSpace(m[2])
	}
	return 0, base
}

// parseAlbumDir extracts artist, album title, and year from a directory path segment.
// Patterns handled:
//
//	"Artist - Album (2024)"  → artist="Artist", album="Album", year="2024"
//	"Artist/Album"           → artist="Artist", album="Album"
//	"Artist - Album"         → artist="Artist", album="Album"
//	"Album (2024)"           → artist="", album="Album", year="2024"
func parseAlbumDir(dirPath string) (artist, album, year string) {
	// Use the last directory segment (the deepest album folder).
	seg := path.Base(dirPath)

	// Extract year: (YYYY) or [YYYY].
	yearRE := regexp.MustCompile(`[\[\(](\d{4})[\]\)]`)
	if m := yearRE.FindStringSubmatch(seg); m != nil {
		year = m[1]
		seg = yearRE.ReplaceAllString(seg, "")
	}

	// Try "Artist - Album" split.
	if idx := strings.Index(seg, " - "); idx > 0 {
		artist = strings.TrimSpace(seg[:idx])
		album = strings.TrimSpace(seg[idx+3:])
		return artist, album, year
	}

	// Fallback: just the segment as album title.
	album = strings.TrimSpace(seg)
	return "", album, year
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

func parseDownloadStatus(raw json.RawMessage, downloadPath string) []domain.DownloadRecord {
	var users []struct {
		Username    string `json:"username"`
		Directories []struct {
			Files []struct {
				ID               string  `json:"id"`
				Filename         string  `json:"filename"`
				State            string  `json:"state"`
				Size             int64   `json:"size"`
				BytesTransferred int64   `json:"bytesTransferred"`
				AverageSpeed     int64   `json:"averageSpeed"`
				PercentComplete  float64 `json:"percentComplete"`
			} `json:"files"`
		} `json:"directories"`
	}

	if err := json.Unmarshal(raw, &users); err != nil {
		return nil
	}

	var records []domain.DownloadRecord
	for _, user := range users {
		for _, dir := range user.Directories {
			for _, f := range dir.Files {
				filePath := ""
				if f.Filename != "" && downloadPath != "" {
					filePath = filepath.Join(downloadPath, f.Filename)
				}
				records = append(records, domain.DownloadRecord{
					ID:          f.ID,
					SourceName:  pluginName,
					Filename:    f.Filename,
					State:       parseState(f.State),
					Progress:    f.PercentComplete,
					Size:        f.Size,
					Transferred: f.BytesTransferred,
					Speed:       f.AverageSpeed,
					FilePath:    filePath,
				})
			}
		}
	}
	return records
}

func parseState(s string) domain.DownloadState {
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "completed") || strings.Contains(lower, "succeeded"):
		return domain.DownloadSucceeded
	case strings.Contains(lower, "errored") || strings.Contains(lower, "failed"):
		return domain.DownloadErrored
	case strings.Contains(lower, "cancelled"):
		return domain.DownloadCancelled
	case strings.Contains(lower, "aborted"):
		return domain.DownloadAborted
	case strings.Contains(lower, "initializing") || strings.Contains(lower, "queued"):
		return domain.DownloadInitializing
	default:
		return domain.DownloadDownloading
	}
}

func getFloat(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}
