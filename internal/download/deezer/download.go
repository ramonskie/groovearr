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
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"

	"golang.org/x/crypto/blowfish"
)

const downloadPluginName = "deezer"
const downloadDisplayName = "Deezer"

// Deezer internal API endpoints.
const (
	gwAPI    = "https://www.deezer.com/ajax/gw-light.php"
	mediaAPI = "https://media.deezer.com/v1/get_url"
)

// Blowfish secret (public knowledge, used by all Deezer clients).
var blowfishSecret = []byte("g4el58wc0zvf9na1")

// Quality format codes for media API.
var qualityFormats = map[string]map[string]string{
	"flac":     {"cipher": "BF_CBC_STRIPE", "format": "FLAC"},
	"mp3_320":  {"cipher": "BF_CBC_STRIPE", "format": "MP3_320"},
	"mp3_128":  {"cipher": "BF_CBC_STRIPE", "format": "MP3_128"},
}

var qualityOrder = []string{"flac", "mp3_320", "mp3_128"}

const chunkSize = 2048
const minFileSize = 100 * 1024 // 100KB

// DownloadClient implements download.Plugin for Deezer downloads.
type DownloadClient struct {
	cfg     config.DeezerConfig
	dlPath  string
	client  *http.Client

	mu            sync.Mutex
	authenticated bool
	apiToken      string
	licenseToken  string
	userID        int

	// Per-download state.
	downloadsMu sync.RWMutex
	downloads   map[string]*domain.DownloadRecord // downloadID → record
}

// NewDownloadClient creates a Deezer download client.
func NewDownloadClient(cfg config.DeezerConfig, downloadPath string) *DownloadClient {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://www.deezer.com")
	jar.SetCookies(u, []*http.Cookie{{
		Name:  "arl",
		Value: cfg.ARL,
		Path:  "/",
	}})

	return &DownloadClient{
		cfg:       cfg,
		dlPath:    downloadPath,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
			Transport: &headerTransport{
				headers: map[string]string{
					"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
					"Accept-Language": "en-US,en;q=0.9",
					"Accept":          "application/json, text/plain, */*",
					"Referer":         "https://www.deezer.com/",
				},
			},
		},
		downloads: make(map[string]*domain.DownloadRecord),
	}
}

// headerTransport adds default headers to every request.
type headerTransport struct {
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

// Name returns the canonical plugin name.
func (c *DownloadClient) Name() string { return downloadPluginName }

// DisplayName returns a human-readable label.
func (c *DownloadClient) DisplayName() string { return downloadDisplayName }

// IsConfigured returns true if ARL token is set.
func (c *DownloadClient) IsConfigured() bool {
	return c.cfg.ARL != ""
}

// CheckConnection tries to authenticate with Deezer using the configured ARL.
func (c *DownloadClient) CheckConnection(ctx context.Context) error {
	if c.cfg.ARL == "" {
		return fmt.Errorf("deezer: ARL token not set")
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
		return nil, nil, fmt.Errorf("deezer auth: %w", err)
	}

	apiClient := New(c.cfg)
	tracks, err := apiClient.SearchTracks(ctx, query, 30)
	if err != nil {
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
		log.Printf("deezer album search: %v", err)
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

// Download initiates a Deezer download. filename is "track_id||display_name".
func (c *DownloadClient) Download(ctx context.Context, username, filename string, fileSize int64) (string, error) {
	if !c.IsConfigured() {
		return "", fmt.Errorf("deezer: ARL token not set")
	}
	if err := c.ensureAuth(); err != nil {
		return "", fmt.Errorf("deezer auth: %w", err)
	}

	parts := strings.SplitN(filename, "||", 2)
	if len(parts) < 1 {
		return "", fmt.Errorf("deezer: invalid filename format, expected 'track_id||display'")
	}
	trackID := parts[0]
	displayName := trackID
	if len(parts) > 1 {
		displayName = parts[1]
	}

	downloadID := fmt.Sprintf("deezer-%s-%d", trackID, time.Now().UnixNano())

	record := &domain.DownloadRecord{
		ID:          downloadID,
		SourceName:  downloadPluginName,
		Filename:    filename,
		DisplayName: displayName,
		TrackID:     trackID,
		State:       domain.DownloadInitializing,
	}

	c.downloadsMu.Lock()
	c.downloads[downloadID] = record
	c.downloadsMu.Unlock()

	// Start download in background.
	go c.downloadSync(downloadID, trackID, displayName)

	return downloadID, nil
}

// GetDownloads returns all tracked downloads.
func (c *DownloadClient) GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error) {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()

	out := make([]domain.DownloadRecord, 0, len(c.downloads))
	for _, r := range c.downloads {
		out = append(out, *r)
	}
	return out, nil
}

// GetDownloadStatus returns a single download's status.
func (c *DownloadClient) GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error) {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()

	r, ok := c.downloads[downloadID]
	if !ok {
		return nil, fmt.Errorf("deezer: download %s not found", downloadID)
	}
	return r, nil
}

// CancelDownload cancels an active download.
func (c *DownloadClient) CancelDownload(ctx context.Context, downloadID string, remove bool) error {
	c.downloadsMu.Lock()
	defer c.downloadsMu.Unlock()

	r, ok := c.downloads[downloadID]
	if !ok {
		return fmt.Errorf("deezer: download %s not found", downloadID)
	}
	r.State = domain.DownloadCancelled
	if remove {
		delete(c.downloads, downloadID)
	}
	return nil
}

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
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authenticated
}

// ensureAuth authenticates if not already done. Safe to call before any API operation.
func (c *DownloadClient) ensureAuth() error {
	if c.Connected() {
		return nil
	}
	return c.authenticate()
}

// ─── Authentication ─────────────────────────────────────────────────

func (c *DownloadClient) authenticate() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfg.ARL == "" {
		return fmt.Errorf("deezer: ARL token not set")
	}

	resp, err := c.gwCall("deezer.getUserData", nil)
	if err != nil {
		return fmt.Errorf("deezer auth: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("deezer auth: empty response")
	}

	user, _ := resp["USER"].(map[string]any)
	if user == nil {
		return fmt.Errorf("deezer auth: no USER in response")
	}

	uid, _ := user["USER_ID"].(float64)
	if uid == 0 {
		return fmt.Errorf("deezer auth: USER_ID is 0 — ARL may be expired")
	}

	c.userID = int(uid)
	c.apiToken, _ = resp["checkForm"].(string)
	if opts, ok := user["OPTIONS"].(map[string]any); ok {
		c.licenseToken, _ = opts["license_token"].(string)
	}
	c.authenticated = true

	return nil
}

// ─── Download implementation ────────────────────────────────────────

func (c *DownloadClient) downloadSync(downloadID, trackID, displayName string) {
	// Get track data from private API.
	trackData, err := c.gwCall("song.getData", map[string]any{"sng_id": trackID})
	if err != nil {
		c.setError(downloadID, fmt.Sprintf("failed to get track data: %v", err))
		return
	}

	trackToken, _ := trackData["TRACK_TOKEN"].(string)
	if trackToken == "" {
		c.setError(downloadID, "no track token available")
		return
	}

	// Determine quality and get media URL with fallback.
	mediaURL, actualQuality := c.getMediaURL(trackToken)
	if mediaURL == "" {
		c.setError(downloadID, "no media URL available")
		return
	}

	ext := ".mp3"
	if actualQuality == "flac" {
		ext = ".flac"
	}

	safeName := sanitizeFilename(displayName)
	outPath := filepath.Join(c.dlPath, safeName+ext)

	// Download and decrypt.
	if err := c.downloadAndDecrypt(downloadID, trackID, mediaURL, outPath); err != nil {
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

	c.updateRecord(downloadID, func(r *domain.DownloadRecord) {
		r.State = domain.DownloadSucceeded
		r.Progress = 100.0
		r.FilePath = outPath
	})
}

func (c *DownloadClient) downloadAndDecrypt(downloadID, trackID, url, outPath string) error {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	key := deriveBlowfishKey(trackID)
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	var downloaded int64
	totalSize := resp.ContentLength
	startTime := time.Now()
	chunkIndex := 0
	buf := make([]byte, chunkSize)

	c.updateRecord(downloadID, func(r *domain.DownloadRecord) {
		r.State = domain.DownloadDownloading
		r.Size = totalSize
	})

	for {
		n, readErr := io.ReadFull(resp.Body, buf)
		if n > 0 {
			chunk := buf[:n]

			// Decrypt every 3rd chunk.
			if chunkIndex%3 == 0 && n == chunkSize {
				decrypted, err := blowfishDecrypt(chunk, key)
				if err == nil {
					chunk = decrypted
				}
			}

			if _, err := f.Write(chunk); err != nil {
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

			c.updateRecord(downloadID, func(r *domain.DownloadRecord) {
				r.Transferred = downloaded
				r.Progress = min(progress, 99.9)
				r.Speed = speed
			})
		}
		if readErr != nil {
			if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("read response: %w", readErr)
		}
	}
}

// ─── Media URL ──────────────────────────────────────────────────────

func (c *DownloadClient) getMediaURL(trackToken string) (string, string) {
	c.mu.Lock()
	licenseToken := c.licenseToken
	c.mu.Unlock()

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
	if !c.cfg.AllowFallback {
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

func (c *DownloadClient) gwCall(method string, params map[string]any) (map[string]any, error) {
	u, _ := url.Parse(gwAPI)
	q := u.Query()
	q.Set("method", method)
	q.Set("api_version", "1.0")
	apiToken := "null"
	if c.apiToken != "" {
		apiToken = c.apiToken
	}
	q.Set("api_token", apiToken)
	u.RawQuery = q.Encode()

	bodyData := params
	if bodyData == nil {
		bodyData = map[string]any{}
	}
	b, _ := json.Marshal(bodyData)

	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", "*/*")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deezer gw call: %w", err)
	}
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(resp.Body)
	var data struct {
		Error   any            `json:"error"`
		Results map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rawBody, &data); err != nil {
		return nil, fmt.Errorf("deezer parse error (http %d): %s", resp.StatusCode, string(rawBody))
	}

	if data.Error != nil {
		// Deezer returns "error":[] for no error (empty array).
		// Only treat as error if the error field is non-empty.
		if errList, ok := data.Error.([]any); ok && len(errList) == 0 {
			// Empty error array = success, fall through.
		} else {
			errJSON, _ := json.Marshal(data.Error)
			return nil, fmt.Errorf("deezer API error: %s", string(errJSON))
		}
	}
	return data.Results, nil
}

// ─── Blowfish decryption ────────────────────────────────────────────

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

	// CBC mode: XOR with previous ciphertext (or IV for first block), then decrypt.
	prev := iv
	blockSize := blowfish.BlockSize // 8 bytes
	for i := 0; i < len(data); i += blockSize {
		end := i + blockSize
		if end > len(data) {
			end = len(data)
		}

		// XOR with previous block.
		xored := make([]byte, blockSize)
		copy(xored, data[i:end])
		for j := 0; j < blockSize && j < len(xored); j++ {
			xored[j] ^= prev[j]
		}

		cipher.Decrypt(dst[i:end], xored)
		prev = data[i:end]
	}

	return dst, nil
}

// ─── Helpers ────────────────────────────────────────────────────────

func (c *DownloadClient) setError(downloadID, msg string) {
	c.updateRecord(downloadID, func(r *domain.DownloadRecord) {
		r.State = domain.DownloadErrored
		r.Error = msg
	})
}

func (c *DownloadClient) updateRecord(downloadID string, fn func(*domain.DownloadRecord)) {
	c.downloadsMu.Lock()
	defer c.downloadsMu.Unlock()
	if r, ok := c.downloads[downloadID]; ok {
		fn(r)
	}
}

func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		"<", "", ">", "", ":", "", "\"", "",
		"/", "", "\\", "", "|", "", "?", "", "*", "",
	)
	name = replacer.Replace(name)
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	if len(name) > 200 {
		name = name[:200]
	}
	if name == "" {
		name = "unknown"
	}
	return name
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}
