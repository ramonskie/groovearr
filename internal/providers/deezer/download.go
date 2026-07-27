// Package deezer implements the download plugin for Deezer using ARL token authentication.
// Downloaded files are encrypted with Blowfish CBC — this client handles decryption on-the-fly.
package deezer

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/provider"
	"github.com/ramonskie/groovearr/internal/sanitize"

	"golang.org/x/crypto/blowfish"
)

const downloadPluginName = "deezer"
const downloadDisplayName = "Deezer"

// Outgoing rate limits — requests per second to Deezer APIs.
const (
	deezerGatewayRate  = 10 // auth, playlist, search metadata
	deezerDownloadRate = 30 // file transfers (higher throughput)
)

// Deezer internal API endpoints.
const (
	gwAPI    = "https://www.deezer.com/ajax/gw-light.php"
	mediaAPI = "https://media.deezer.com/v1/get_url"
)

// Blowfish secret (public knowledge, used by all Deezer clients).
var blowfishSecret = []byte("g4el58wc0zvf9na1")

// Quality format codes for media API.
var qualityFormats = map[string]map[string]string{
	"flac":    {"cipher": "BF_CBC_STRIPE", "format": "FLAC"},
	"mp3_320": {"cipher": "BF_CBC_STRIPE", "format": "MP3_320"},
	"mp3_128": {"cipher": "BF_CBC_STRIPE", "format": "MP3_128"},
}

var qualityOrder = []string{"flac", "mp3_320", "mp3_128"}

const chunkSize = 2048
const minFileSize = 100 * 1024 // 100KB

// DownloadClient implements download.Plugin for Deezer downloads.
type DownloadClient struct {
	cfg    DeezerConfig
	dlPath string
	client *http.Client // API calls (30s timeout)
	api    *Client      // public API client (discovery, metadata, no auth needed)
	log    *slog.Logger

	// downloadClient has no timeout — file downloads may take many minutes.
	// ReadIdleTimeout on the transport kills stalled connections after 30s of inactivity.
	downloadClient *http.Client

	authMu        sync.Mutex   // serializes authenticate calls
	tokenMu       sync.RWMutex // protects apiToken, licenseToken, userID, authenticated
	authenticated bool
	publicHealthy bool // set by CheckConnection when public API is reachable
	apiToken      string
	licenseToken  string
	userID        int

	// Per-download state.
	downloadsMu sync.RWMutex
	downloads   map[string]*download.Record // downloadID → record

	// Cancellation for active downloads.
	cancelMu    sync.Mutex
	cancelFuncs map[string]context.CancelFunc // downloadID → cancel
}

// NewDownloadClient creates a Deezer download client.
func NewDownloadClient(cfg DeezerConfig, downloadPath string, logger *slog.Logger) *DownloadClient {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://www.deezer.com")
	jar.SetCookies(u, []*http.Cookie{{
		Name:  "arl",
		Value: cfg.ARL,
		Path:  "/",
	}})

	downloadHeaders := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Accept-Language": "en-US,en;q=0.9",
		"Accept":          "application/json, text/plain, */*",
		"Referer":         "https://www.deezer.com/",
	}

	return &DownloadClient{
		cfg:    cfg,
		dlPath: downloadPath,
		api:    New(cfg, logger),
		log:    logger,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
			Transport: &headerTransport{
				headers:   downloadHeaders,
				transport: provider.NewRateLimitedTransport(http.DefaultTransport, deezerGatewayRate),
			},
		},
		downloadClient: &http.Client{
			Jar: jar,
			Transport: &headerTransport{
				headers: downloadHeaders,
				transport: provider.NewRateLimitedTransport(&http.Transport{
					Proxy:                 http.ProxyFromEnvironment,
					DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
					ForceAttemptHTTP2:     true,
					MaxIdleConns:          100,
					IdleConnTimeout:       90 * time.Second,
					TLSHandshakeTimeout:   10 * time.Second,
					ExpectContinueTimeout: 1 * time.Second,
				}, deezerDownloadRate),
			},
		},
		downloads:   make(map[string]*download.Record),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
}

// headerTransport adds default headers to every request.
type headerTransport struct {
	headers   map[string]string
	transport http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	return t.transport.RoundTrip(req)
}

// stallTimeoutReader wraps an io.Reader and returns an error if a Read
// call blocks longer than timeout. Used to detect stalled CDN connections.
type stallTimeoutReader struct {
	r       io.Reader
	timeout time.Duration
}

func (s *stallTimeoutReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := s.r.Read(p)
		ch <- result{n, err}
	}()
	select {
	case res := <-ch:
		return res.n, res.err
	case <-time.After(s.timeout):
		return 0, fmt.Errorf("read stalled after %v", s.timeout)
	}
}

// Name returns the canonical plugin name.
func (c *DownloadClient) Name() string { return downloadPluginName }

// DisplayName returns a human-readable label.
func (c *DownloadClient) DisplayName() string { return downloadDisplayName }

// IsConfigured returns true if the plugin has a valid ARL token for downloads.
func (c *DownloadClient) IsConfigured() bool { return c.cfg.ARL != "" }

// IsMetadataAvailable is always true — the public Deezer API works without auth.
func (c *DownloadClient) IsMetadataAvailable() bool { return true }

// CapabilityStatus reports per-capability connection status from health checks.
func (c *DownloadClient) CapabilityStatus() map[string]string {
	dlStatus := "not_configured"
	if c.cfg.ARL != "" {
		dlStatus = "configured"
		if c.Connected() {
			dlStatus = "connected"
		}
	}
	pubStatus := "configured"
	if c.Connected() {
		pubStatus = "connected"
	}
	return map[string]string{
		"download":  dlStatus,
		"playlist":  dlStatus,
		"discovery": pubStatus,
		"metadata":  pubStatus,
	}
}

// UserID returns the authenticated Deezer user ID, or 0 if not authenticated.
func (c *DownloadClient) UserID() int {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.userID
}

// CheckConnection verifies the Deezer connection. When ARL is configured,
// authenticates with Deezer for downloads. When ARL is empty, only checks
// the public metadata API — metadata works without authentication.
func (c *DownloadClient) CheckConnection(ctx context.Context) error {
	if c.cfg.ARL == "" {
		// Metadata-only mode: verify public API is reachable.
		_, err := c.api.SearchTracks(ctx, "test", 1)
		c.tokenMu.Lock()
		c.publicHealthy = err == nil
		c.tokenMu.Unlock()
		if err != nil {
			c.log.Error("deezer metadata check failed", "error", err, "component", "deezer")
			return fmt.Errorf("deezer: public API unreachable: %w", err)
		}
		return nil
	}
	// Use a shorter timeout for the connection test, then restore original.
	origClient := c.client
	clientCopy := *origClient
	clientCopy.Timeout = 10 * time.Second
	c.client = &clientCopy
	defer func() { c.client = origClient }()

	return c.authenticate()
}

// Search queries Deezer's public API for tracks.
func (c *DownloadClient) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if err := c.ensureAuth(); err != nil {
		c.log.Error("search auth failed", "error", err, "component", "deezer")
		return nil, nil, fmt.Errorf("deezer auth: %w", err)
	}

	apiClient := New(c.cfg, c.log)
	tracks, err := apiClient.SearchTracks(ctx, query, 30)
	if err != nil {
		c.log.Error("search tracks failed", "error", err, "query", query, "component", "deezer")
		return nil, nil, err
	}

	quality := c.cfg.Quality
	if quality == "" {
		quality = "flac"
	}

	results := make([]domain.TrackResult, len(tracks))
	for i, t := range tracks {
		results[i] = t.ToTrackResult(quality)
	}

	// Also search for albums.
	albums, err := apiClient.SearchAlbums(ctx, query, 20)
	if err != nil {
		c.log.Warn("album search failed", "error", err, "component", "deezer")
		return results, nil, nil // album search is non-fatal
	}

	albumResults := make([]domain.AlbumResult, 0, len(albums))
	for _, a := range albums {
		year := ""
		if len(a.ReleaseDate) >= 4 {
			year = a.ReleaseDate[:4]
		}
		albumResults = append(albumResults, domain.AlbumResult{
			Username:        "deezer",
			AlbumTitle:      a.Title,
			Artist:          a.Artist.Name,
			TrackCount:      a.NbTracks,
			DominantQuality: quality,
			Year:            year,
			Tracks:          nil, // album search doesn't include track lists
		})
	}

	return results, albumResults, nil
}

// StartDownload initiates a non-blocking Deezer download. Returns a
// provider-managed download ID for subsequent status queries.
// Implements download.MonitoredProvider.
func (c *DownloadClient) StartDownload(ctx context.Context, meta download.Meta) (string, error) {
	if !c.IsConfigured() {
		c.log.Error("download failed: ARL not set", "component", "deezer")
		return "", fmt.Errorf("deezer: ARL token not set")
	}
	if err := c.ensureAuth(); err != nil {
		c.log.Error("download auth failed", "error", err, "component", "deezer")
		return "", fmt.Errorf("deezer auth: %w", err)
	}

	trackID := meta.TrackID
	displayName := meta.Artist + " - " + meta.Title

	// Fallback: parse track ID from filename for cross-provider retries
	// where the original download (e.g., Soulseek) didn't set TrackID.
	// Filename format: "trackID||displayName" from the queue path.
	if trackID == "" {
		if parts := strings.SplitN(meta.Filename, "||", 2); len(parts) >= 1 && parts[0] != "" {
			trackID = parts[0]
		}
	}
	if trackID == "" {
		c.log.Error("download failed: track ID not available", "component", "deezer")
		return "", fmt.Errorf("deezer: track ID not provided (set TrackID in DownloadMeta or use 'id||display' filename format)")
	}

	filename := trackID + "||" + displayName

	downloadID := fmt.Sprintf("deezer-%s-%d", trackID, time.Now().UnixNano())

	// Create record and start goroutine only after all checks pass.
	record := &download.Record{
		ID:          downloadID,
		SourceName:  downloadPluginName,
		Filename:    filename,
		DisplayName: displayName,
		TrackID:     trackID,
		State:       download.StateQueued,
	}

	c.downloadsMu.Lock()
	c.downloads[downloadID] = record
	c.downloadsMu.Unlock()

	c.log.Info("download queued", "downloadID", downloadID, "displayName", displayName, "component", "deezer")

	// Derive from the caller's context so cancellation propagates.
	dlCtx, cancel := context.WithCancel(ctx)
	c.cancelMu.Lock()
	c.cancelFuncs[downloadID] = cancel
	c.cancelMu.Unlock()

	go c.downloadSync(dlCtx, downloadID, trackID, displayName)
	return downloadID, nil
}

// GetDownloads returns all tracked downloads.
func (c *DownloadClient) GetDownloads(ctx context.Context) ([]download.Record, error) {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()

	out := make([]download.Record, 0, len(c.downloads))
	for _, r := range c.downloads {
		out = append(out, *r)
	}
	return out, nil
}

// GetDownloadStatus returns a single download's status.
func (c *DownloadClient) GetDownloadStatus(ctx context.Context, downloadID string) (*download.Record, error) {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()

	r, ok := c.downloads[downloadID]
	if !ok {
		return nil, fmt.Errorf("deezer: download %s not found", downloadID)
	}
	rec := *r // return a copy to avoid data race with download goroutine
	return &rec, nil
}

// CancelDownload cancels an active download by cancelling its context.
func (c *DownloadClient) CancelDownload(ctx context.Context, downloadID string, remove bool) error {
	c.downloadsMu.Lock()
	r, ok := c.downloads[downloadID]
	if ok {
		r.State = download.StateIgnored
		r.Error = ""
	}
	if remove {
		delete(c.downloads, downloadID)
	}
	c.downloadsMu.Unlock()

	if !ok {
		return fmt.Errorf("deezer: download %s not found", downloadID)
	}

	// Cancel the underlying context to stop the goroutine.
	c.cancelMu.Lock()
	if cancel, ok := c.cancelFuncs[downloadID]; ok {
		cancel()
		delete(c.cancelFuncs, downloadID)
	}
	c.cancelMu.Unlock()

	return nil
}

// GetStatus implements download.MonitoredProvider by wrapping GetDownloadStatus.
func (c *DownloadClient) GetStatus(ctx context.Context, providerID string) (*download.Record, error) {
	return c.GetDownloadStatus(ctx, providerID)
}

// Cancel implements download.MonitoredProvider by wrapping CancelDownload.
func (c *DownloadClient) Cancel(ctx context.Context, providerID string, remove bool) error {
	return c.CancelDownload(ctx, providerID, remove)
}

// ActiveDownloads returns the provider-managed IDs of all currently tracked downloads.
func (c *DownloadClient) ActiveDownloads() []string {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()
	ids := make([]string, 0, len(c.downloads))
	for id := range c.downloads {
		ids = append(ids, id)
	}
	return ids
}

// MaxConcurrent returns the maximum number of concurrent downloads for this provider.
func (c *DownloadClient) MaxConcurrent() int { return 2 }

// DownloadTimeout returns the per-provider timeout duration.
func (c *DownloadClient) DownloadTimeout() time.Duration { return 10 * time.Minute }

// ClearCompleted removes terminal-state downloads.
func (c *DownloadClient) ClearCompleted(ctx context.Context) error {
	c.downloadsMu.Lock()
	defer c.downloadsMu.Unlock()

	for id, r := range c.downloads {
		if r.State.Terminal() {
			delete(c.downloads, id)
		}
	}
	return nil
}

// Connected returns true if authentication has succeeded.
func (c *DownloadClient) Connected() bool {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.authenticated || c.publicHealthy
}

// GetProgress implements download.MonitoredProvider by retrieving the current
// transfer state from the in-memory download map.
func (c *DownloadClient) GetProgress(ctx context.Context, downloadID string) (*download.Progress, error) {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()
	r, ok := c.downloads[downloadID]
	if !ok {
		return nil, fmt.Errorf("deezer: download %s not found", downloadID)
	}
	return &download.Progress{
		DownloadID:  downloadID,
		Transferred: r.Transferred,
		Total:       r.Size,
		Speed:       r.Speed,
	}, nil
}

// isAuthenticated reads the auth flag under the token lock.
func (c *DownloadClient) isAuthenticated() bool {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.authenticated
}

// ensureAuth authenticates if not already done. Safe to call before any API operation.
func (c *DownloadClient) ensureAuth() error {
	if c.isAuthenticated() {
		return nil
	}
	return c.authenticate()
}

// ─── Authentication ─────────────────────────────────────────────────

func (c *DownloadClient) authenticate() error {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	// Double-check: might have been authenticated between lock acquisition.
	if c.isAuthenticated() {
		return nil
	}

	if c.cfg.ARL == "" {
		c.log.Error("authenticate failed: ARL not set", "component", "deezer")
		return fmt.Errorf("deezer: ARL token not set")
	}

	resp, err := c.gwCall(context.Background(), "deezer.getUserData", nil)
	if err != nil {
		c.log.Error("authenticate gwCall failed", "error", err, "component", "deezer")
		return fmt.Errorf("deezer auth: %w", err)
	}
	if resp == nil {
		c.log.Error("authenticate empty response", "component", "deezer")
		return fmt.Errorf("deezer auth: empty response")
	}

	user, _ := resp["USER"].(map[string]any)
	if user == nil {
		c.log.Error("authenticate no USER in response", "component", "deezer")
		return fmt.Errorf("deezer auth: no USER in response")
	}

	uid, _ := user["USER_ID"].(float64)
	if uid == 0 {
		c.log.Error("authenticate USER_ID is 0, ARL may be expired", "component", "deezer")
		return fmt.Errorf("deezer auth: USER_ID is 0 — ARL may be expired")
	}

	c.tokenMu.Lock()
	c.userID = int(uid)
	c.apiToken, _ = resp["checkForm"].(string)
	if opts, ok := user["OPTIONS"].(map[string]any); ok {
		c.licenseToken, _ = opts["license_token"].(string)
	}
	c.authenticated = true
	c.tokenMu.Unlock()

	return nil
}

// ─── Download implementation ────────────────────────────────────────

func (c *DownloadClient) downloadSync(ctx context.Context, downloadID, trackID, displayName string) {
	// Clean up cancel func when done.
	defer func() {
		c.cancelMu.Lock()
		delete(c.cancelFuncs, downloadID)
		c.cancelMu.Unlock()
	}()

	// Catch panics so they don't silently kill the goroutine.
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("download panic", "downloadID", downloadID, "panic", r, "component", "deezer")
			c.setError(downloadID, fmt.Sprintf("panic: %v", r))
		}
	}()

	// Check for early cancellation.
	if ctx.Err() != nil {
		return
	}

	// Get track data from private API.
	trackData, err := c.gwCall(ctx, "song.getData", map[string]any{"sng_id": trackID})
	if err != nil {
		c.log.Warn("get track data failed", "downloadID", downloadID, "error", err, "component", "deezer")
		c.setError(downloadID, fmt.Sprintf("failed to get track data: %v", err))
		return
	}
	if ctx.Err() != nil {
		return
	}

	// Fetch track metadata from public API (cover URL + renamer metadata).
	trackIDInt, convErr := strconv.Atoi(trackID)
	if convErr == nil {
		apiClient := New(c.cfg, c.log)
		if trk, err := apiClient.GetTrack(ctx, trackIDInt); err == nil && trk != nil {
			year := 0
			if len(trk.ReleaseDate) >= 4 {
				if y, parseErr := strconv.Atoi(trk.ReleaseDate[:4]); parseErr == nil {
					year = y
				}
			}
			c.updateRecord(downloadID, func(r *download.Record) {
				r.CoverURL = trk.Album.CoverXL
				r.Artist = trk.Artist.Name
				r.Album = trk.Album.Title
				r.Title = trk.Title
				r.TrackNumber = trk.TrackPos
				r.DiscNumber = trk.DiskNumber
				r.Year = year
			})
		}
	}

	trackToken, _ := trackData["TRACK_TOKEN"].(string)
	if trackToken == "" {
		c.log.Warn("no track token", "downloadID", downloadID, "component", "deezer")
		c.setError(downloadID, "no track token available")
		return
	}

	// Determine quality and get media URL with fallback.
	mediaURL, actualQuality := c.getMediaURL(trackToken)
	if mediaURL == "" {
		c.log.Warn("no media URL available", "downloadID", downloadID, "quality", actualQuality, "component", "deezer")
		c.setError(downloadID, "no media URL available")
		return
	}

	c.log.Info("download starting", "downloadID", downloadID, "quality", actualQuality, "displayName", displayName, "component", "deezer")

	ext := ".mp3"
	if actualQuality == "flac" {
		ext = ".flac"
	}

	safeName := sanitize.FileName(displayName)
	outPath := filepath.Join(c.dlPath, safeName+ext)

	// Download and decrypt.
	if err := c.downloadAndDecrypt(ctx, downloadID, trackID, mediaURL, outPath); err != nil {
		c.setError(downloadID, err.Error())
		os.Remove(outPath)
		return
	}

	// Validate file size.
	fi, err := os.Stat(outPath)
	if err != nil || fi.Size() < minFileSize {
		c.setError(downloadID, fmt.Sprintf("file too small (%d bytes)", fi.Size()))
		os.Remove(outPath)
		return
	}

	// Only mark succeeded if not already cancelled.
	c.updateRecord(downloadID, func(r *download.Record) {
		if r.State.Terminal() {
			return // cancelled or errored during download
		}
		r.State = download.StateImported
		r.Progress = 100.0
		r.FilePath = outPath
	})
	c.log.Info("download succeeded", "downloadID", downloadID, "path", outPath, "component", "deezer")
}

func (c *DownloadClient) downloadAndDecrypt(ctx context.Context, downloadID, trackID, url, outPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.log.Error("download create request failed", "error", err, "downloadID", downloadID, "component", "deezer")
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := c.downloadClient.Do(req)
	if err != nil {
		c.log.Error("download HTTP request failed", "error", err, "downloadID", downloadID, "component", "deezer")
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.log.Error("download non-OK status", "status", resp.StatusCode, "downloadID", downloadID, "component", "deezer")
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	key := deriveBlowfishKey(trackID)
	f, err := os.Create(outPath)
	if err != nil {
		c.log.Error("download create file failed", "error", err, "downloadID", downloadID, "path", outPath, "component", "deezer")
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	// Wrap the body so stalled reads time out after 30s.
	body := &stallTimeoutReader{r: resp.Body, timeout: 30 * time.Second}

	var downloaded int64
	totalSize := resp.ContentLength
	startTime := time.Now()
	chunkIndex := 0
	buf := make([]byte, chunkSize)

	c.updateRecord(downloadID, func(r *download.Record) {
		r.State = download.StateDownloading
		r.Size = totalSize
	})

	// Read and process chunks. Every 3rd chunk is encrypted with Blowfish CBC.
	// Deezer always uses BF_CBC_STRIPE for all quality formats.
	firstN, firstReadErr := io.ReadFull(body, buf)
	if firstReadErr != nil && firstReadErr != io.ErrUnexpectedEOF && firstReadErr != io.EOF {
		c.log.Error("download read first chunk failed", "error", firstReadErr, "downloadID", downloadID, "component", "deezer")
		return fmt.Errorf("read response: %w", firstReadErr)
	}
	if firstN > 0 {
		chunk := buf[:firstN]
		if chunkIndex%3 == 0 && firstN == chunkSize {
			if decrypted, err := blowfishDecrypt(chunk, key); err == nil {
				chunk = decrypted
			}
		}
		if _, err := f.Write(chunk); err != nil {
			c.log.Error("download write first chunk failed", "error", err, "downloadID", downloadID, "component", "deezer")
			return fmt.Errorf("write file: %w", err)
		}
		downloaded += int64(firstN)
		chunkIndex++
	}
	if firstReadErr != nil {
		// Short file (single chunk). Validate size check below.
	}

	for {
		// Check for cancellation between chunks.
		if ctx.Err() != nil {
			os.Remove(outPath)
			return ctx.Err()
		}

		n, readErr := io.ReadFull(body, buf)
		if n > 0 {
			chunk := buf[:n]

			// Decrypt every 3rd chunk (BF_CBC_STRIPE pattern).
			if chunkIndex%3 == 0 && n == chunkSize {
				decrypted, err := blowfishDecrypt(chunk, key)
				if err == nil {
					chunk = decrypted
				}
			}

			if _, err := f.Write(chunk); err != nil {
				c.log.Error("download write chunk failed", "error", err, "downloadID", downloadID, "component", "deezer")
				return fmt.Errorf("write file: %w", err)
			}

			downloaded += int64(n)
			chunkIndex++

			// Update progress.
			elapsed := time.Since(startTime)
			speed := int64(0)
			if elapsed > 0 {
				speed = int64(float64(downloaded) / elapsed.Seconds())
			}
			progress := 0.0
			if totalSize > 0 {
				progress = float64(downloaded) / float64(totalSize) * 100
			}

			c.updateRecord(downloadID, func(r *download.Record) {
				r.Transferred = downloaded
				r.Progress = min(progress, 99.9)
				r.Speed = speed
			})
		}
		if readErr != nil {
			if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
				return nil
			}
			c.log.Error("download read chunk failed", "error", readErr, "downloadID", downloadID, "component", "deezer")
			return fmt.Errorf("read response: %w", readErr)
		}
	}
}

// ─── Media URL ──────────────────────────────────────────────────────

func (c *DownloadClient) getMediaURL(trackToken string) (string, string) {
	c.tokenMu.RLock()
	licenseToken := c.licenseToken
	c.tokenMu.RUnlock()

	if licenseToken == "" {
		return "", ""
	}

	// Build quality order with preferred first.
	order := make([]string, len(qualityOrder))
	copy(order, qualityOrder)
	prefIdx := indexOf(order, c.cfg.Quality)
	if prefIdx >= 0 {
		order = append(order[prefIdx:], order[:prefIdx]...)
	}
	// Allow fallback unless explicitly disabled.
	if c.cfg.AllowFallback != nil && !*c.cfg.AllowFallback {
		order = order[:1]
	}

	for _, q := range order {
		fmt, ok := qualityFormats[q]
		if !ok {
			continue
		}

		payload := map[string]any{
			"license_token": licenseToken,
			"media": []map[string]any{{
				"type":    "FULL",
				"formats": []map[string]string{fmt},
			}},
			"track_tokens": []string{trackToken},
		}

		body, _ := json.Marshal(payload)
		resp, err := c.client.Post(mediaAPI, "application/json", strings.NewReader(string(body)))
		if err != nil {
			continue
		}

		var data struct {
			Data []struct {
				Media []struct {
					Sources []struct {
						URL string `json:"url"`
					} `json:"sources"`
				} `json:"media"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if len(data.Data) > 0 && len(data.Data[0].Media) > 0 && len(data.Data[0].Media[0].Sources) > 0 {
			return data.Data[0].Media[0].Sources[0].URL, q
		}
	}

	return "", ""
}

// ─── Gateway API ────────────────────────────────────────────────────

func (c *DownloadClient) gwCall(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	u, _ := url.Parse(gwAPI)
	q := u.Query()
	q.Set("method", method)
	q.Set("api_version", "1.0")

	c.tokenMu.RLock()
	apiToken := c.apiToken
	c.tokenMu.RUnlock()

	if apiToken == "" {
		apiToken = "null"
	}
	q.Set("api_token", apiToken)
	u.RawQuery = q.Encode()

	bodyData := params
	if bodyData == nil {
		bodyData = map[string]any{}
	}
	b, _ := json.Marshal(bodyData)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(b)))
	if err != nil {
		c.log.Error("gwCall create request failed", "error", err, "method", method, "component", "deezer")
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", "*/*")

	resp, err := c.client.Do(req)
	if err != nil {
		c.log.Error("gwCall HTTP request failed", "error", err, "method", method, "component", "deezer")
		return nil, fmt.Errorf("deezer gw call: %w", err)
	}
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(resp.Body)
	var data struct {
		Error   any            `json:"error"`
		Results map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rawBody, &data); err != nil {
		c.log.Error("gwCall parse error", "error", err, "method", method, "status", resp.StatusCode, "component", "deezer")
		return nil, fmt.Errorf("deezer parse error (http %d): %s", resp.StatusCode, string(rawBody))
	}

	if data.Error != nil {
		// Deezer returns "error":[] for no error (empty array).
		// Only treat as error if the error field is non-empty.
		if errList, ok := data.Error.([]any); ok && len(errList) == 0 {
			// Empty error array = success, fall through.
		} else {
			errJSON, _ := json.Marshal(data.Error)
			c.log.Error("gwCall API error", "method", method, "apiError", string(errJSON), "component", "deezer")
			return nil, fmt.Errorf("deezer API error: %s", string(errJSON))
		}
	}
	return data.Results, nil
}

// ─── Blowfish decryption ────────────────────────────────────────────

// isAudioHeader checks whether the first bytes look like a known audio format.
// Returns true for valid FLAC (fLaC magic) or MP3 (0xFFEx/0xFFFx sync).
// Encrypted (BF_CBC_STRIPE) streams have random first bytes → returns false.
func isAudioHeader(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	// FLAC: "fLaC" magic bytes.
	if string(data[:4]) == "fLaC" {
		return true
	}
	// MP3: frame sync — first byte 0xFF, second byte top 3 bits set.
	if data[0] == 0xFF && len(data) >= 2 && (data[1]&0xE0) == 0xE0 {
		return true
	}
	return false
}

// deriveBlowfishKey derives a 16-byte Blowfish key from a Deezer track ID.
func deriveBlowfishKey(trackID string) []byte {
	hash := md5.Sum([]byte(trackID))
	hexStr := hex.EncodeToString(hash[:])
	key := make([]byte, 16)
	for i := 0; i < 16; i++ {
		key[i] = hexStr[i] ^ hexStr[i+16] ^ blowfishSecret[i]
	}
	return key
}

func blowfishDecrypt(data, key []byte) ([]byte, error) {
	cipher, err := blowfish.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	dst := make([]byte, len(data))

	// CBC mode decryption: Decrypt(ciphertext) XOR previous_ciphertext (or IV).
	prev := iv
	blockSize := blowfish.BlockSize // 8 bytes
	for i := 0; i < len(data); i += blockSize {
		end := i + blockSize
		if end > len(data) {
			end = len(data)
		}

		// Decrypt the current ciphertext block.
		var decrypted [8]byte
		cipher.Decrypt(decrypted[:], data[i:end])

		// XOR decrypted block with previous ciphertext (CBC mode).
		for j := 0; j < blockSize && j < len(decrypted); j++ {
			dst[i+j] = decrypted[j] ^ prev[j]
		}
		prev = data[i:end]
	}

	return dst, nil
}

// ─── Playlist Adapter ─────────────────────────────────────────────────

// playlistSourceAdapter adapts DownloadClient to playlist.Source.
// Lives in the providers/deezer package alongside the download client to avoid
// circular imports between download and playlist packages.
type playlistSourceAdapter struct {
	client *DownloadClient
}

func (a *playlistSourceAdapter) Name() string        { return downloadPluginName }
func (a *playlistSourceAdapter) DisplayName() string { return downloadDisplayName }
func (a *playlistSourceAdapter) IsConfigured() bool  { return a.client.IsConfigured() }

func (a *playlistSourceAdapter) GetUserPlaylists(ctx context.Context) ([]playlist.PlaylistInfo, error) {
	raw, err := a.client.GetUserPlaylists(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]playlist.PlaylistInfo, len(raw))
	for i, p := range raw {
		out[i] = playlist.PlaylistInfo{
			SourceID:    p.ID,
			Name:        strings.TrimSpace(p.Title),
			Description: strings.TrimSpace(p.Description),
			TrackCount:  p.TrackCount,
		}
	}
	return out, nil
}

func (a *playlistSourceAdapter) GetPlaylistTracks(ctx context.Context, sourceID string) ([]playlist.TrackInfo, string, error) {
	raw, name, err := a.client.GetPlaylistTracks(ctx, sourceID)
	if err != nil {
		return nil, "", err
	}
	out := make([]playlist.TrackInfo, len(raw))
	for i, t := range raw {
		durMs, _ := strconv.ParseInt(t.Duration, 10, 64)
		out[i] = playlist.TrackInfo{
			SourceTrackID: t.ID,
			Title:         strings.TrimSpace(t.Title),
			Artist:        strings.TrimSpace(t.Artist),
			Album:         strings.TrimSpace(t.Album),
			DurationMs:    durMs * 1000,
			ISRC:          strings.TrimSpace(t.ISRC),
		}
	}
	return out, name, nil
}

// PlaylistSource returns a playlist.Source wrapping this download client.
// Satisfies playlist.PlaylistSourceProvider.
func (c *DownloadClient) PlaylistSource() playlist.Source {
	return &playlistSourceAdapter{client: c}
}

// ─── Helpers ────────────────────────────────────────────────────────

func (c *DownloadClient) setError(downloadID, msg string) {
	c.updateRecord(downloadID, func(r *download.Record) {
		if r.State.Terminal() {
			return // already cancelled — don't overwrite
		}
		r.State = download.StateFailed
		r.Error = msg
	})
}

func (c *DownloadClient) updateRecord(downloadID string, fn func(*download.Record)) {
	c.downloadsMu.Lock()
	defer c.downloadsMu.Unlock()
	if r, ok := c.downloads[downloadID]; ok {
		fn(r)
	}
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}

// ─── Playlist API ─────────────────────────────────────────────────────

// DeezerTrackInfo holds track-level data from Deezer playlist/song responses.
type DeezerTrackInfo struct {
	ID          string `json:"SNG_ID"`
	Title       string `json:"SNG_TITLE"`
	Artist      string `json:"ART_NAME"`
	Album       string `json:"ALB_TITLE"`
	Duration    string `json:"DURATION"`
	TrackNumber string `json:"TRACK_NUMBER"`
	DiskNumber  string `json:"DISK_NUMBER"`
	ISRC        string `json:"ISRC"`
}

// DeezerPlaylist holds playlist data from Deezer's internal API.
type DeezerPlaylist struct {
	ID          string `json:"PLAYLIST_ID"`
	Title       string `json:"TITLE"`
	Description string `json:"DESCRIPTION"`
	TrackCount  int    `json:"NB_SONG"`
}

// GetUserPlaylists fetches the authenticated user's playlists via the Deezer gateway.
func (c *DownloadClient) GetUserPlaylists(ctx context.Context) ([]DeezerPlaylist, error) {
	if err := c.ensureAuth(); err != nil {
		c.log.Error("get user playlists auth failed", "error", err, "component", "deezer")
		return nil, fmt.Errorf("deezer auth: %w", err)
	}

	results, err := c.gwCall(ctx, "deezer.pageProfile", map[string]any{
		"USER_ID": c.UserID(),
		"tab":     "playlists",
	})
	if err != nil {
		c.log.Error("get user playlists gwCall failed", "error", err, "component", "deezer")
		return nil, fmt.Errorf("pageProfile: %w", err)
	}

	// TAB.playlists.data contains the user's playlists.
	if tab, ok := results["TAB"].(map[string]any); ok {
		if pl, ok := tab["playlists"].(map[string]any); ok {
			rawList, _ := pl["data"].([]any)
			return parsePlaylistItems(rawList), nil
		}
	}
	return nil, nil
}

func parsePlaylistItems(rawList []any) []DeezerPlaylist {
	var out []DeezerPlaylist
	for _, item := range rawList {
		raw, _ := json.Marshal(item)
		var p DeezerPlaylist
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if p.ID == "" || p.Title == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// GetPlaylistTracks fetches all tracks in a Deezer playlist via the gateway.
func (c *DownloadClient) GetPlaylistTracks(ctx context.Context, playlistID string) ([]DeezerTrackInfo, string, error) {
	if err := c.ensureAuth(); err != nil {
		c.log.Error("get playlist tracks auth failed", "error", err, "playlistID", playlistID, "component", "deezer")
		return nil, "", fmt.Errorf("deezer auth: %w", err)
	}

	results, err := c.gwCall(ctx, "deezer.pagePlaylist", map[string]any{
		"playlist_id": playlistID,
		"nb":          500,
	})
	if err != nil {
		c.log.Error("get playlist tracks gwCall failed", "error", err, "playlistID", playlistID, "component", "deezer")
		return nil, "", fmt.Errorf("pagePlaylist: %w", err)
	}

	// Extract playlist name from DATA.
	var playlistName string
	if data, ok := results["DATA"].(map[string]any); ok {
		if name, ok := data["TITLE"].(string); ok {
			playlistName = name
		}
	}

	// SONGS is at results level (not inside DATA).
	songs, _ := results["SONGS"].(map[string]any)
	if songs == nil {
		return nil, playlistName, nil
	}

	rawList, _ := songs["data"].([]any)
	if rawList == nil {
		return nil, playlistName, nil
	}

	var out []DeezerTrackInfo
	for _, item := range rawList {
		raw, _ := json.Marshal(item)
		var t DeezerTrackInfo
		if err := json.Unmarshal(raw, &t); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, playlistName, nil
}

// ─── discovery.Provider ────────────────────────────────────────────

func (d *DownloadClient) SearchArtists(ctx context.Context, query string, limit int) ([]discovery.ArtistSummary, error) {
	artists, err := d.api.SearchArtists(ctx, query, limit)
	if err != nil {
		d.log.Error("search artists failed", "error", err, "query", query, "component", "deezer")
		return nil, err
	}
	out := make([]discovery.ArtistSummary, len(artists))
	for i, a := range artists {
		out[i] = discovery.ArtistSummary{
			ProviderID:   strconv.Itoa(a.ID),
			ProviderName: "deezer",
			Name:         a.Name,
			ImageURL:     a.PictureMed,
		}
	}
	return out, nil
}

func (d *DownloadClient) GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]discovery.AlbumResult, error) {
	id, err := strconv.Atoi(providerArtistID)
	if err != nil {
		d.log.Error("get artist albums invalid id", "error", err, "providerArtistID", providerArtistID, "component", "deezer")
		return nil, fmt.Errorf("deezer: invalid artist id: %w", err)
	}
	albums, err := d.api.GetArtistAlbums(ctx, id, limit)
	if err != nil {
		d.log.Error("get artist albums api failed", "error", err, "artistID", id, "component", "deezer")
		return nil, err
	}
	var out []discovery.AlbumResult
	for _, a := range albums {
		coverURL := a.CoverXL
		if coverURL == "" {
			coverURL = a.CoverBig
		}
		artistName := ""
		if a.Artist.Name != "" {
			artistName = a.Artist.Name
		}
		out = append(out, discovery.AlbumResult{
			ProviderID:   strconv.Itoa(a.ID),
			ProviderName: "deezer",
			ArtistName:   artistName,
			Title:        a.Title,
			CoverURL:     coverURL,
			TrackCount:   a.NbTracks,
			Type:         a.RecordType,
		})
	}
	return out, nil
}

func (d *DownloadClient) GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]discovery.TrackInfo, error) {
	id, err := strconv.Atoi(providerAlbumID)
	if err != nil {
		d.log.Error("get album tracks invalid id", "error", err, "providerAlbumID", providerAlbumID, "component", "deezer")
		return nil, fmt.Errorf("deezer: invalid album id: %w", err)
	}
	album, err := d.api.GetAlbum(ctx, id)
	if err != nil {
		d.log.Error("get album tracks get album failed", "error", err, "albumID", id, "component", "deezer")
		return nil, err
	}
	tracks, err := d.api.GetAlbumTracks(ctx, id)
	if err != nil {
		d.log.Error("get album tracks api failed", "error", err, "albumID", id, "component", "deezer")
		return nil, err
	}
	artistName := album.Artist.Name
	out := make([]discovery.TrackInfo, len(tracks))
	for i, t := range tracks {
		trackArtist := artistName
		if t.Artist.Name != "" {
			trackArtist = t.Artist.Name
		}
		out[i] = discovery.TrackInfo{
			ProviderID:  strconv.Itoa(t.ID),
			ArtistName:  trackArtist,
			AlbumTitle:  album.Title,
			Title:       t.Title,
			TrackNumber: t.TrackPos,
			DiscNumber:  t.DiskNumber,
			DurationMs:  int64(t.Duration) * 1000,
		}
	}
	return out, nil
}

func (d *DownloadClient) SearchAlbums(ctx context.Context, query string, limit int) ([]discovery.AlbumResult, error) {
	albums, err := d.api.SearchAlbums(ctx, query, limit)
	if err != nil {
		d.log.Error("search albums failed", "error", err, "query", query, "component", "deezer")
		return nil, err
	}
	var out []discovery.AlbumResult
	for _, a := range albums {
		coverURL := a.CoverXL
		if coverURL == "" {
			coverURL = a.CoverBig
		}
		artistName := ""
		if a.Artist.Name != "" {
			artistName = a.Artist.Name
		}
		out = append(out, discovery.AlbumResult{
			ProviderID:   strconv.Itoa(a.ID),
			ProviderName: "deezer",
			ArtistName:   artistName,
			Title:        a.Title,
			CoverURL:     coverURL,
			TrackCount:   a.NbTracks,
			Type:         a.RecordType,
		})
	}
	return out, nil
}

// ─── metadata.Provider (public API, no ARL needed) ─────────────────────

// Compile-time checks.
var _ metadata.Provider = (*DownloadClient)(nil)
var _ download.MonitoredProvider = (*DownloadClient)(nil)

// SearchAlbum finds the album title for a track via Deezer's public search API.
func (c *DownloadClient) SearchAlbum(ctx context.Context, artist, title string) string {
	if artist == "" || title == "" {
		return ""
	}
	tracks, err := c.api.SearchTracksAdvanced(ctx, title, artist, "", 3)
	if err != nil {
		c.log.Warn("deezer search album failed", "error", err, "artist", artist, "title", title, "component", "deezer")
		return ""
	}
	for _, t := range tracks {
		if t.Album.Title != "" {
			return t.Album.Title
		}
	}
	return ""
}

// SearchCover looks up album cover art via Deezer's public search API.
func (c *DownloadClient) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	if artist == "" || album == "" {
		return nil, nil
	}
	tracks, err := c.api.SearchTracksAdvanced(ctx, "", artist, album, 1)
	if err != nil {
		c.log.Warn("deezer search cover failed", "error", err, "artist", artist, "album", album, "component", "deezer")
		return nil, err
	}
	if len(tracks) == 0 || tracks[0].Album.Title == "" {
		return nil, nil
	}
	a := tracks[0].Album
	imageURL := a.CoverXL
	if imageURL == "" {
		imageURL = a.CoverBig
	}
	if imageURL == "" {
		return nil, nil
	}
	return &metadata.CoverResult{
		ImageURL: imageURL,
		Width:    500,
		Height:   500,
		Source:   "deezer",
		ThumbURL: a.CoverMed,
	}, nil
}

// SearchArtistImage is unsupported by the Deezer public search API.
func (c *DownloadClient) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}

// EnrichTrack is unsupported by the Deezer public search API.
func (c *DownloadClient) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}
