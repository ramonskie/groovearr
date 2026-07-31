// Package qbittorrent implements a DownloadClient plugin for qBittorrent WebUI API v2.
package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/plugin"
)

const (
	pluginName      = "qbittorrent"
	displayName     = "qBittorrent"
	defaultCategory = "music"
)

// Config holds the qBittorrent WebUI connection settings.
type Config struct {
	URL             string `json:"url"`              // e.g. http://localhost:8080
	APIKey          string `json:"api_key"`          // API key from WebUI settings
	Enabled         bool   `json:"enabled"`          // user-facing enable/disable toggle (default true)
	Category        string `json:"category"`         // torrent category (default: "music")
	DownloadPath    string `json:"download_path"`    // per-plugin download dir (falls back to library.download_path)
	RemoveCompleted bool   `json:"remove_completed"` // remove from client after import
}

// Plugin implements download.DownloadClient for qBittorrent.
type Plugin struct {
	cfg     Config
	http    *http.Client
	log     *slog.Logger
	baseURL string
	dlPath  string

	mu        sync.Mutex
	connected bool
}

var _ download.DownloadClient = (*Plugin)(nil)

func newPlugin(cfg Config, downloadPath string, logger *slog.Logger) (*Plugin, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("qbittorrent: url is required")
	}
	baseURL := strings.TrimRight(cfg.URL, "/")
	p := &Plugin{
		cfg:     cfg,
		log:     logger,
		baseURL: baseURL,
		dlPath:  downloadPath,
		http:    &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}},
	}
	return p, nil
}

// ─── plugin.BasePlugin ───────────────────────────────────────────────

func (p *Plugin) Name() string                    { return pluginName }
func (p *Plugin) DisplayName() string              { return displayName }
func (p *Plugin) IsConfigured() bool { return p.cfg.URL != "" && p.cfg.APIKey != "" }
func (p *Plugin) Connected() bool    { p.mu.Lock(); defer p.mu.Unlock(); return p.connected }

// IsEnabled returns false when the plugin has been explicitly disabled.
func (p *Plugin) IsEnabled() bool { return p.cfg.Enabled }
func (p *Plugin) CapabilityStatus() map[string]string { return map[string]string{"download_client": p.capStatus()} }

func (p *Plugin) capStatus() string {
	if !p.IsConfigured() {
		return "not_configured"
	}
	if p.Connected() {
		return "connected"
	}
	return "configured"
}

func (p *Plugin) CheckConnection(ctx context.Context) error {
	if !p.IsConfigured() {
		return fmt.Errorf("qbittorrent: not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/v2/app/version", nil)
	if err != nil {
		p.mu.Lock()
		p.connected = false
		p.mu.Unlock()
		return fmt.Errorf("qbittorrent: connection check failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	resp, err := p.http.Do(req)
	if err != nil {
		p.mu.Lock()
		p.connected = false
		p.mu.Unlock()
		return fmt.Errorf("qbittorrent: connection check failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.mu.Lock()
		p.connected = false
		p.mu.Unlock()
		return fmt.Errorf("qbittorrent: unexpected status %d", resp.StatusCode)
	}
	p.mu.Lock()
	p.connected = true
	p.mu.Unlock()
	return nil
}

// ─── download.DownloadClient ─────────────────────────────────────────

func (p *Plugin) AddDownload(ctx context.Context, uri, category, savepath string) (string, error) {
	if category == "" {
		category = p.cfg.Category
	}
	if category == "" {
		category = defaultCategory
	}
	if savepath == "" {
		savepath = p.dlPath
	}

	hash, err := p.addTorrent(ctx, uri, category, savepath)
	if err != nil {
		return "", err
	}
	return hash, nil
}

func (p *Plugin) GetStatus(ctx context.Context, providerID string) (*download.Record, error) {
	info, err := p.getTorrentInfo(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		// Magnet still resolving metadata — return nil, nil so the monitor
		// polls again next tick instead of treating this as a failure.
		return nil, nil
	}
	return p.mapToRecord(info), nil
}

func (p *Plugin) GetProgress(ctx context.Context, providerID string) (*download.Progress, error) {
	info, err := p.getTorrentInfo(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, nil
	}
	return &download.Progress{
		DownloadID:  providerID,
		Transferred: info.TotalSize - info.AmountLeft,
		Total:       info.TotalSize,
		Speed:       info.DlSpeed,
	}, nil
}

func (p *Plugin) Cancel(ctx context.Context, providerID string, remove bool) error {
	return p.deleteTorrents(ctx, []string{providerID}, remove)
}

func (p *Plugin) MaxConcurrent() int     { return 5 }
func (p *Plugin) DownloadTimeout() time.Duration { return 2 * time.Hour }

// ─── HTTP methods ────────────────────────────────────────────────────



func (p *Plugin) doRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+"/api/v2"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", p.baseURL)
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	req.Header.Set("Connection", "close")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return p.http.Do(req)
}

func (p *Plugin) addTorrent(ctx context.Context, uri, category, savepath string) (string, error) {
	torrentData, err := p.downloadTorrentFile(ctx, uri)
	if err != nil {
		return "", fmt.Errorf("download torrent file: %w", err)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	tw, err := writer.CreateFormFile("torrents", "release.torrent")
	if err != nil {
		return "", err
	}
	if _, err := tw.Write(torrentData); err != nil {
		return "", err
	}
	writer.WriteField("category", category)
	writer.WriteField("savepath", savepath)
	writer.WriteField("paused", "false")
	writer.Close()

	resp, err := p.doRequest(ctx, http.MethodPost, "/torrents/add", &buf, writer.FormDataContentType())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("add torrent: %d: %s", resp.StatusCode, string(respBody))
	}

	// Resolve the torrent hash from qBittorrent's recently-added list, filtered
	// by category to reduce the chance of picking up a concurrently added torrent.
	return p.resolveRecentHash(ctx, category, savepath)
}

// downloadTorrentFile fetches a .torrent file from a URL.
func (p *Plugin) downloadTorrentFile(ctx context.Context, uri string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Groovearr/1.0")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch torrent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch torrent: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// resolveRecentHash returns the hash of the torrent we just added, filtered
// by category and save path to avoid picking up a concurrently added torrent.
func (p *Plugin) resolveRecentHash(ctx context.Context, category, savepath string) (string, error) {
	query := url.Values{}
	query.Set("sort", "added_on")
	query.Set("reverse", "true")
	query.Set("limit", "5")
	if category != "" {
		query.Set("category", category)
	}

	resp, err := p.doRequest(ctx, http.MethodGet, "/torrents/info?"+query.Encode(), nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve hash: %d", resp.StatusCode)
	}

	var infos []torrentInfo
	if err := json.NewDecoder(resp.Body).Decode(&infos); err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", fmt.Errorf("torrent added but not found in list")
	}

	// Pick the newest torrent whose save path matches. If none match, fall
	// back to the most recently added (it's very likely ours).
	savepathLower := strings.ToLower(savepath)
	for _, info := range infos {
		if strings.Contains(strings.ToLower(info.SavePath), savepathLower) {
			return info.Hash, nil
		}
	}
	return infos[0].Hash, nil
}

func (p *Plugin) getTorrentInfo(ctx context.Context, hash string) (*torrentInfo, error) {
	query := url.Values{}
	query.Set("hashes", hash)

	resp, err := p.doRequest(ctx, http.MethodGet, "/torrents/info?"+query.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrents/info: %d", resp.StatusCode)
	}

	var infos []torrentInfo
	if err := json.NewDecoder(resp.Body).Decode(&infos); err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		// Not found yet — magnet still resolving metadata.
		return nil, nil
	}
	result := infos[0]
	return &result, nil
}

func (p *Plugin) deleteTorrents(ctx context.Context, hashes []string, deleteFiles bool) error {
	data := url.Values{}
	data.Set("hashes", strings.Join(hashes, "|"))
	if deleteFiles {
		data.Set("deleteFiles", "true")
	} else {
		data.Set("deleteFiles", "false")
	}

	resp, err := p.doRequest(ctx, http.MethodPost, "/torrents/delete",
		strings.NewReader(data.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete torrents: %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ─── State mapping ───────────────────────────────────────────────────

func (p *Plugin) mapToRecord(info *torrentInfo) *download.Record {
	rec := &download.Record{
		ID:          info.Hash,
		SourceName:  pluginName,
		DisplayName: info.Name,
		FilePath:    info.ContentPath,
		Size:        info.TotalSize,
		Progress:    info.Progress * 100,
		Transferred: info.TotalSize - info.AmountLeft,
		Speed:       info.DlSpeed,
		State:       mapState(info),
	}
	if rec.FilePath == "" {
		rec.FilePath = info.SavePath + "/" + info.Name
	}
	return rec
}

func mapState(info *torrentInfo) download.State {
	if info.AmountLeft == 0 && info.CompletionOn > 0 {
		return download.StateImportPending
	}
	switch info.State {
	case "error", "missingFiles":
		return download.StateFailed
	case "metaDL", "allocating", "checkingResumeData", "moving":
		return download.StateDownloading
	default:
		// downloading, forcedDL, stalledDL, checkingDL, pausedDL, queuedDL
		return download.StateDownloading
	}
}

// ─── JSON types ──────────────────────────────────────────────────────

type torrentInfo struct {
	Hash         string  `json:"hash"`
	Name         string  `json:"name"`
	State        string  `json:"state"`
	Progress     float64 `json:"progress"`
	Size         int64   `json:"size"`
	TotalSize    int64   `json:"total_size"`
	SavePath     string  `json:"save_path"`
	ContentPath  string  `json:"content_path"`
	Category     string  `json:"category"`
	DlSpeed      int64   `json:"dlspeed"`
	AmountLeft   int64   `json:"amount_left"`
	CompletionOn int64   `json:"completion_on"`
	MagnetURI    string  `json:"magnet_uri"`
}

// Ensure plugin.PluginFactory compatibility.
var _ plugin.BasePlugin = (*Plugin)(nil)
