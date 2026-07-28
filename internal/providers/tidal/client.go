// Package tidal implements the Tidal music streaming plugin.
// It provides download, metadata enrichment, discovery/browse, and playlist import
// via Tidal's REST API (apiClient) and the go-tiddl library for auth + streaming.
package tidal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/sanitize"

	"github.com/binozo/go-tiddl"
	"golang.org/x/oauth2"
)

// Compile-time interface checks.
var (
	_ plugin.BasePlugin             = (*Client)(nil)
	_ download.Plugin               = (*Client)(nil)
	_ download.MonitoredProvider    = (*Client)(nil)
	_ metadata.Provider             = (*Client)(nil)
	_ discovery.Provider            = (*Client)(nil)
	_ discovery.TopTrackProvider    = (*Client)(nil)
	_ playlist.PlaylistSourceProvider = (*Client)(nil)
)

// ─── Client ─────────────────────────────────────────────────────────────

// Client is the top-level Tidal plugin, satisfying all capability interfaces.
// It wraps a go-tiddl client for auth/streaming and an apiClient for search/metadata.
type Client struct {
	cfg    TidalConfig
	dlPath string
	log    *slog.Logger

	tiddlClient *tiddl.Client
	api         *apiClient

	connected bool
	mu        sync.RWMutex // protects connected and download state

	// tokenPersist is called after a token refresh to persist the new token to config.
	// Set by the OAuth handler after plugin initialization.
	tokenPersist func(accessToken, refreshToken string)

	// Pending device authorization flow — preserved between StartDeviceAuth and CompleteDeviceAuth.
	pendingAuth   *tiddl.AuthRequest
	pendingAuthMu sync.Mutex

	// Per-download tracking.
	downloadsMu sync.RWMutex
	downloads   map[string]*download.Record

	cancelMu    sync.Mutex
	cancelFuncs map[string]context.CancelFunc

	// Playlist adapter cached after construction.
	playlistAdapter *playlistSourceAdapter
}

// NewClient creates a Tidal plugin client from config, download path, and logger.
// Called by the plugin factory (factory.go) on initialization.
func NewClient(cfg TidalConfig, downloadPath string, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	// Build go-tiddl client with optional custom credentials.
	tiddlOpts := []tiddl.Option{
		tiddl.WithCountryCode(cfg.CountryCode),
		tiddl.WithLogger(logger),
	}
	// Note: we intentionally do NOT pass custom client_id/client_secret to go-tiddl.
	// go-tiddl uses Tidal's public web player credentials (hardcoded) which support
	// the device code OAuth flow. User-provided Developer Portal apps typically
	// don't support device code grant type and would fail during OAuth.
	tc, err := tiddl.NewClient(tiddlOpts...)
	if err != nil {
		return nil, fmt.Errorf("tidal: failed to create tiddl client: %w", err)
	}

	// If an access token is already configured, inject it into the tiddl client.
	if cfg.AccessToken != "" {
		tok := &oauth2.Token{
			AccessToken:  cfg.AccessToken,
			RefreshToken: cfg.RefreshToken,
			TokenType:    "Bearer",
		}
		// Use WithAuthToken to enable auto-refresh.
		if cfg.RefreshToken != "" {
			tc.SetToken(tok)
		} else {
			// Static token without refresh capability.
			tc2, err := tiddl.NewClient(
				tiddl.WithCountryCode(cfg.CountryCode),
				tiddl.WithLogger(logger),
				tiddl.WithAuth(cfg.AccessToken),
			)
			if err != nil {
				tc.Close()
				return nil, fmt.Errorf("tidal: failed to create tiddl client with auth: %w", err)
			}
			tc.Close()
			tc = tc2
		}
	}

	// Create the REST API client.
	api := newAPIClient(cfg)

	c := &Client{
		cfg:         cfg,
		dlPath:      downloadPath,
		log:         logger,
		tiddlClient: tc,
		api:         api,
		downloads:   make(map[string]*download.Record),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
	c.playlistAdapter = &playlistSourceAdapter{client: c}

	// Register token persistence callback so refreshed tokens update in-memory
	// config and the apiClient. Config file persistence is handled via tokenPersist
	// which is wired by main.go and the OAuth handler.
	tc.SetTokenChanged(func(tok *oauth2.Token) {
		c.mu.Lock()
		c.cfg.AccessToken = tok.AccessToken
		c.cfg.RefreshToken = tok.RefreshToken
		c.api.SetToken(tok.AccessToken)
		if c.tokenPersist != nil {
			c.tokenPersist(tok.AccessToken, tok.RefreshToken)
		}
		c.mu.Unlock()
	})

	return c, nil
}

// ─── plugin.BasePlugin ──────────────────────────────────────────────────

func (c *Client) Name() string        { return pluginName }
func (c *Client) DisplayName() string { return displayName }

// SetTokenPersistCallback registers a function called after token refresh
// to persist the new token to config. Called by OAuth handlers.
func (c *Client) SetTokenPersistCallback(fn func(accessToken, refreshToken string)) {
	c.mu.Lock()
	c.tokenPersist = fn
	c.mu.Unlock()
}

// IsConfigured returns true when a Tidal access token is configured.
func (c *Client) IsConfigured() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.AccessToken != ""
}

// Connected returns whether the client has passed the last health check.
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// CheckConnection verifies the Tidal session by calling /v1/sessions.
func (c *Client) CheckConnection(ctx context.Context) error {
	c.mu.RLock()
	hasToken := c.cfg.AccessToken != ""
	c.mu.RUnlock()
	if !hasToken {
		return fmt.Errorf("tidal: access token not configured")
	}
	session, err := c.tiddlClient.GetSession(ctx)
	if err != nil {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		return fmt.Errorf("tidal: session check failed: %w", err)
	}
	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	c.log.Debug("tidal session validated", "userID", session.UserID, "countryCode", session.CountryCode, "component", "tidal")
	return nil
}

// CapabilityStatus returns per-capability status strings for the UI.
func (c *Client) CapabilityStatus() map[string]string {
	c.mu.RLock()
	hasToken := c.cfg.AccessToken != ""
	c.mu.RUnlock()
	dlStatus := "not_configured"
	if hasToken {
		dlStatus = "configured"
		if c.Connected() {
			dlStatus = "connected"
		}
	}
	metaStatus := "not_configured"
	if hasToken {
		metaStatus = "configured"
		// Metadata works as long as token is set.
	}
	return map[string]string{
		"download":  dlStatus,
		"playlist":  dlStatus,
		"discovery": metaStatus,
		"metadata":  metaStatus,
	}
}

// ─── download.Plugin: Search ────────────────────────────────────────────

// Search queries Tidal for tracks and albums matching the given query.
func (c *Client) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	tracks, albums, err := c.api.Search(ctx, query, 30, 0)
	if err != nil {
		c.log.Error("tidal search failed", "error", err, "query", query, "component", "tidal")
		return nil, nil, err
	}

	c.mu.RLock()
	quality := c.cfg.Quality
	c.mu.RUnlock()
	if quality == "" {
		quality = "LOSSLESS"
	}

	trackResults := make([]domain.TrackResult, len(tracks))
	for i, t := range tracks {
		artist := t.Artist.Name
		album := t.Album.Title
		cover := t.Album.Cover
		if cover != "" {
			cover = ImageURL(cover, 640, 640)
		}
		trackResults[i] = domain.TrackResult{
			SearchResult: domain.SearchResult{
				Username:     "",
				Filename:     strconv.FormatInt(t.ID, 10) + "||" + artist + " - " + t.Title,
				Size:         int64(t.Duration * 88200), // estimate for FLAC (~50% of PCM)
				Bitrate:      int(TidalQualityToAudioQuality(t.AudioQuality).Bitrate),
				Duration:     int64(t.Duration) * 1000, // seconds to ms
				Quality:      TidalQualityToAudioQuality(t.AudioQuality).Format,
				AudioQuality: TidalQualityToAudioQuality(t.AudioQuality),
			},
			Artist:      artist,
			Title:       t.Title,
			Album:       album,
			TrackNumber: t.TrackNumber,
			CoverURL:    cover,
		}
	}

	albumResults := make([]domain.AlbumResult, 0, len(albums))
	for _, a := range albums {
		year := ""
		if len(a.ReleaseDate) >= 4 {
			year = a.ReleaseDate[:4]
		}
		artistName := a.Artist.Name
		albumResults = append(albumResults, domain.AlbumResult{
			Username:        "tidal",
			AlbumTitle:      a.Title,
			Artist:          artistName,
			TrackCount:      a.NumTracks,
			DominantQuality: quality,
			Year:            year,
		})
	}

	return trackResults, albumResults, nil
}

// ─── download.MonitoredProvider ─────────────────────────────────────────

// StartDownload initiates a non-blocking Tidal download. Returns a
// provider-managed download ID for status queries.
func (c *Client) StartDownload(ctx context.Context, meta download.Meta) (string, error) {
	if !c.IsConfigured() {
		return "", fmt.Errorf("tidal: access token not set")
	}

	trackID := meta.TrackID
	displayName := meta.Artist + " - " + meta.Title

	// Fallback: parse track ID from filename.
	if trackID == "" {
		if parts := strings.SplitN(meta.Filename, "||", 2); len(parts) >= 1 && parts[0] != "" {
			trackID = parts[0]
		}
	}
	if trackID == "" {
		return "", fmt.Errorf("tidal: track ID not provided")
	}

	downloadID := fmt.Sprintf("tidal-%s-%d", trackID, time.Now().UnixNano())

	record := &download.Record{
		ID:          downloadID,
		SourceName:  pluginName,
		Filename:    trackID + "||" + displayName,
		DisplayName: displayName,
		TrackID:     trackID,
		State:       download.StateQueued,
	}

	c.downloadsMu.Lock()
	c.downloads[downloadID] = record
	c.downloadsMu.Unlock()

	c.log.Info("download queued", "downloadID", downloadID, "displayName", displayName, "component", "tidal")

	dlCtx, cancel := context.WithCancel(ctx)
	c.cancelMu.Lock()
	c.cancelFuncs[downloadID] = cancel
	c.cancelMu.Unlock()

	go c.downloadSync(dlCtx, downloadID, trackID, displayName)
	return downloadID, nil
}

// GetStatus returns the current state of a tracked download.
func (c *Client) GetStatus(ctx context.Context, providerID string) (*download.Record, error) {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()
	r, ok := c.downloads[providerID]
	if !ok {
		return nil, fmt.Errorf("tidal: download %s not found", providerID)
	}
	rec := *r
	return &rec, nil
}

// GetProgress returns byte-level progress for a download.
func (c *Client) GetProgress(ctx context.Context, providerID string) (*download.Progress, error) {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()
	r, ok := c.downloads[providerID]
	if !ok {
		return nil, fmt.Errorf("tidal: download %s not found", providerID)
	}
	return &download.Progress{
		DownloadID:  providerID,
		Transferred: r.Transferred,
		Total:       r.Size,
		Speed:       r.Speed,
	}, nil
}

// Cancel cancels an active download.
func (c *Client) Cancel(ctx context.Context, providerID string, remove bool) error {
	c.downloadsMu.Lock()
	r, ok := c.downloads[providerID]
	if ok {
		r.State = download.StateIgnored
		r.Error = ""
	}
	if remove {
		delete(c.downloads, providerID)
	}
	c.downloadsMu.Unlock()

	if !ok {
		return fmt.Errorf("tidal: download %s not found", providerID)
	}

	c.cancelMu.Lock()
	if cancel, exists := c.cancelFuncs[providerID]; exists {
		cancel()
		delete(c.cancelFuncs, providerID)
	}
	c.cancelMu.Unlock()

	return nil
}

// ActiveDownloads returns all tracked download IDs.
func (c *Client) ActiveDownloads() []string {
	c.downloadsMu.RLock()
	defer c.downloadsMu.RUnlock()
	ids := make([]string, 0, len(c.downloads))
	for id := range c.downloads {
		ids = append(ids, id)
	}
	return ids
}

// MaxConcurrent returns the maximum concurrent downloads for Tidal (2).
func (c *Client) MaxConcurrent() int { return 2 }

// DownloadTimeout returns the per-provider download timeout (10 minutes).
func (c *Client) DownloadTimeout() time.Duration { return 10 * time.Minute }

// ─── Download Pipeline ──────────────────────────────────────────────────

// downloadSync performs the actual download in a goroutine.
func (c *Client) downloadSync(ctx context.Context, downloadID, trackID, displayName string) {
	defer func() {
		c.cancelMu.Lock()
		delete(c.cancelFuncs, downloadID)
		c.cancelMu.Unlock()
	}()

	defer func() {
		if r := recover(); r != nil {
			c.log.Error("download panic", "downloadID", downloadID, "panic", r, "component", "tidal")
			c.setError(downloadID, fmt.Sprintf("panic: %v", r))
		}
	}()

	if ctx.Err() != nil {
		return
	}

	// Parse track ID as uint64 for go-tiddl.
	trackIDUint, err := strconv.ParseUint(trackID, 10, 64)
	if err != nil {
		c.setError(downloadID, fmt.Sprintf("invalid track ID: %s", trackID))
		return
	}

	// Fetch track metadata via go-tiddl.
	track, err := c.tiddlClient.GetTrack(ctx, trackIDUint)
	if err != nil {
		c.log.Warn("get track metadata failed", "downloadID", downloadID, "error", err, "component", "tidal")
		c.setError(downloadID, fmt.Sprintf("failed to get track metadata: %v", err))
		return
	}

	// Enrich download record with metadata from the track response.
	artistName := track.Artist.Name
	albumTitle := track.Album.Title
	c.updateRecord(downloadID, func(r *download.Record) {
		r.Artist = artistName
		r.Album = albumTitle
		r.Title = track.Title
		r.TrackNumber = track.TrackNumber
		r.CoverURL = ImageURL(track.Album.Cover, 1280, 1280)
	})

	if ctx.Err() != nil {
		return
	}

	// Determine quality: map cfg.Quality to tiddl.AudioQuality, fall back if not available.
	desiredQuality := cfgQualityToTiddl(c.cfg.Quality)
	actualQuality := selectQuality(desiredQuality, track.BestQuality())

	c.log.Info("download starting", "downloadID", downloadID, "quality", actualQuality, "displayName", displayName, "component", "tidal")

	// Get stream manifest and download URL.
	stream, err := c.tiddlClient.GetTrackStream(ctx, trackIDUint, actualQuality, false)
	if err != nil {
		c.log.Warn("get track stream failed", "downloadID", downloadID, "error", err, "component", "tidal")
		c.setError(downloadID, fmt.Sprintf("failed to get stream: %v", err))
		return
	}

	// Download audio data via go-tiddl's concurrent segment reader.
	reader, err := c.tiddlClient.DownloadTrackStream(ctx, stream)
	if err != nil {
		c.log.Warn("download track stream failed", "downloadID", downloadID, "error", err, "component", "tidal")
		c.setError(downloadID, fmt.Sprintf("failed to download stream: %v", err))
		return
	}
	defer reader.Close()

	// Determine file extension.
	ext := ".m4a"
	if actualQuality == tiddl.Lossless || actualQuality == tiddl.HiResLossless {
		ext = ".flac"
	}

	safeName := sanitize.FileName(displayName)
	outPath := filepath.Join(c.dlPath, safeName+ext)

	// Write stream to file.
	if err := c.writeStream(ctx, downloadID, reader, outPath); err != nil {
		c.setError(downloadID, err.Error())
		os.Remove(outPath)
		return
	}

	// Validate file size.
	fi, err := os.Stat(outPath)
	if err != nil {
		c.setError(downloadID, fmt.Sprintf("stat file: %v", err))
		os.Remove(outPath)
		return
	}
	if fi.Size() < 100*1024 {
		c.setError(downloadID, fmt.Sprintf("file too small (%d bytes)", fi.Size()))
		os.Remove(outPath)
		return
	}

	c.updateRecord(downloadID, func(r *download.Record) {
		if r.State.Terminal() {
			return
		}
		r.State = download.StateImported
		r.Progress = 100.0
		r.FilePath = outPath
	})
	c.log.Info("download succeeded", "downloadID", downloadID, "path", outPath, "component", "tidal")
}

// writeStream copies the reader to the output file, tracking progress.
func (c *Client) writeStream(ctx context.Context, downloadID string, reader io.Reader, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	c.updateRecord(downloadID, func(r *download.Record) { r.State = download.StateDownloading })

	buf := make([]byte, 32*1024) // 32KB buffer
	var written int64
	startTime := time.Now()

	for {
		if ctx.Err() != nil {
			os.Remove(outPath)
			return ctx.Err()
		}

		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return fmt.Errorf("write file: %w", err)
			}
			written += int64(n)

			elapsed := time.Since(startTime)
			speed := int64(0)
			if elapsed > 0 {
				speed = int64(float64(written) / elapsed.Seconds())
			}

			c.updateRecord(downloadID, func(r *download.Record) {
				r.Transferred = written
				r.Speed = speed
			})
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("read stream: %w", readErr)
		}
	}
}

// ─── Quality Selection ──────────────────────────────────────────────────

// cfgQualityToTiddl maps a TidalConfig quality string to a tiddl AudioQuality.
func cfgQualityToTiddl(q string) tiddl.AudioQuality {
	switch q {
	case "LOSSLESS":
		return tiddl.Lossless
	case "HIGH":
		return tiddl.High
	case "LOW":
		return tiddl.Low
	default:
		return tiddl.Lossless
	}
}

// selectQuality returns the best available quality not exceeding the desired one.
func selectQuality(desired, available tiddl.AudioQuality) tiddl.AudioQuality {
	if qualityLevel(desired) <= qualityLevel(available) {
		return desired
	}
	return available
}

// qualityLevel returns a numeric precedence for quality comparison.
func qualityLevel(q tiddl.AudioQuality) int {
	switch q {
	case tiddl.Low:
		return 0
	case tiddl.High:
		return 1
	case tiddl.Lossless:
		return 2
	case tiddl.HiResLossless:
		return 3
	default:
		return 0
	}
}

// ─── metadata.Provider ──────────────────────────────────────────────────

// IsMetadataAvailable returns true when access token is set (metadata needs auth).
func (c *Client) IsMetadataAvailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.AccessToken != ""
}

// SearchCover looks up album cover art via Tidal's search API.
func (c *Client) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	if artist == "" || album == "" {
		return nil, nil
	}
	results, err := c.api.SearchAlbums(ctx, artist+" "+album, 5, 0)
	if err != nil {
		c.log.Warn("tidal search cover failed", "error", err, "artist", artist, "album", album, "component", "tidal")
		return nil, err
	}
	for _, a := range results {
		if a.Cover == "" {
			continue
		}
		// Verify artist matches to avoid wrong cover for common album names.
		if !strings.EqualFold(a.Artist.Name, artist) {
			continue
		}
		coverURL := ImageURL(a.Cover, 1280, 1280)
		return &metadata.CoverResult{
			ImageURL: coverURL,
			Width:    1280,
			Height:   1280,
			Source:   "tidal",
			ThumbURL: ImageURL(a.Cover, 320, 320),
		}, nil
	}
	return nil, nil
}

// SearchArtistImage looks up an artist image via Tidal's search API.
func (c *Client) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	if artist == "" {
		return nil, nil
	}
	results, err := c.api.SearchArtists(ctx, artist, 3)
	if err != nil {
		c.log.Warn("tidal search artist image failed", "error", err, "artist", artist, "component", "tidal")
		return nil, err
	}
	for _, a := range results {
		if a.Picture == "" {
			continue
		}
		return &metadata.ArtistImageResult{
			ImageURL: ImageURL(a.Picture, 480, 480),
			Width:    480,
			Height:   480,
			Source:   "tidal",
			ThumbURL: ImageURL(a.Picture, 160, 160),
		}, nil
	}
	return nil, nil
}

// SearchAlbum finds the album title for a track via Tidal's search API.
func (c *Client) SearchAlbum(ctx context.Context, artist, title string) string {
	if artist == "" || title == "" {
		return ""
	}
	tracks, err := c.api.SearchTracks(ctx, title+" "+artist, 3, 0)
	if err != nil {
		c.log.Warn("tidal search album failed", "error", err, "artist", artist, "title", title, "component", "tidal")
		return ""
	}
	for _, t := range tracks {
		if t.Album.Title != "" {
			return t.Album.Title
		}
	}
	return ""
}

// EnrichTrack fetches metadata (ISRC, release date) by searching Tidal's API
// by title and matching against the track's ISRC field.
func (c *Client) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	if track == nil {
		return nil, nil
	}
	// Search Tidal by title to find matching metadata (ISRC, release date).
	var isrc, releaseDate string
	tracks, err := c.api.SearchTracks(ctx, track.Title, 5, 0)
	if err != nil {
		c.log.Warn("tidal enrich track search failed", "error", err, "title", track.Title, "component", "tidal")
		return nil, err
	}
	for _, t := range tracks {
		if t.ISRC != "" {
			isrc = t.ISRC
		}
		if t.Album.ReleaseDate != "" {
			releaseDate = t.Album.ReleaseDate
		}
		if isrc != "" {
			break
		}
	}
	if isrc == "" && releaseDate == "" {
		return nil, nil
	}
	return &metadata.TrackMetadata{
		ISRC:        isrc,
		ReleaseDate: releaseDate,
	}, nil
}

// ─── discovery.Provider ─────────────────────────────────────────────────

// SearchArtists searches Tidal artists by name.
func (c *Client) SearchArtists(ctx context.Context, query string, limit int) ([]discovery.ArtistSummary, error) {
	artists, err := c.api.SearchArtists(ctx, query, limit)
	if err != nil {
		c.log.Error("tidal search artists failed", "error", err, "query", query, "component", "tidal")
		return nil, err
	}
	out := make([]discovery.ArtistSummary, len(artists))
	for i, a := range artists {
		imageURL := ""
		if a.Picture != "" {
			imageURL = ImageURL(a.Picture, 480, 480)
		}
		out[i] = discovery.ArtistSummary{
			ProviderID:   strconv.FormatInt(a.ID, 10),
			ProviderName: "tidal",
			Name:         a.Name,
			ImageURL:     imageURL,
		}
	}
	return out, nil
}

// GetArtistAlbums returns an artist's albums via Tidal's API.
func (c *Client) GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]discovery.AlbumResult, error) {
	albums, err := c.api.GetArtistAlbums(ctx, providerArtistID, limit)
	if err != nil {
		c.log.Error("tidal get artist albums failed", "error", err, "artistID", providerArtistID, "component", "tidal")
		return nil, err
	}
	out := make([]discovery.AlbumResult, 0, len(albums))
	for _, a := range albums {
		coverURL := ""
		if a.Cover != "" {
			coverURL = ImageURL(a.Cover, 640, 640)
		}
		year := 0
		if len(a.ReleaseDate) >= 4 {
			if y, err := strconv.Atoi(a.ReleaseDate[:4]); err == nil {
				year = y
			}
		}
		out = append(out, discovery.AlbumResult{
			ProviderID:   strconv.FormatInt(a.ID, 10),
			ProviderName: "tidal",
			ArtistName:   a.Artist.Name,
			Title:        a.Title,
			CoverURL:     coverURL,
			TrackCount:   a.NumTracks,
			Type:         a.Type,
			Year:         year,
		})
	}
	return out, nil
}

// GetAlbumTracks returns all tracks for a Tidal album.
func (c *Client) GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]discovery.TrackInfo, error) {
	album, err := c.api.GetAlbum(ctx, providerAlbumID)
	if err != nil {
		c.log.Error("tidal get album failed", "error", err, "albumID", providerAlbumID, "component", "tidal")
		return nil, err
	}
	tracks, err := c.api.GetAlbumTracks(ctx, providerAlbumID)
	if err != nil {
		c.log.Error("tidal get album tracks failed", "error", err, "albumID", providerAlbumID, "component", "tidal")
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
			ProviderID:  strconv.FormatInt(t.ID, 10),
			ArtistName:  trackArtist,
			AlbumTitle:  album.Title,
			Title:       t.Title,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.VolumeNumber,
			DurationMs:  int64(t.Duration) * 1000,
		}
	}
	return out, nil
}

// SearchAlbums searches Tidal for albums by name.
func (c *Client) SearchAlbums(ctx context.Context, query string, limit int) ([]discovery.AlbumResult, error) {
	return c.searchAlbumsInternal(ctx, query, limit)
}

// ─── discovery.TopTrackProvider ─────────────────────────────────────────

// GetArtistTopTracks returns an artist's top tracks via Tidal's API.
func (c *Client) GetArtistTopTracks(ctx context.Context, providerArtistID string, limit int) ([]discovery.TrackInfo, error) {
	tracks, err := c.api.GetArtistTopTracks(ctx, providerArtistID, limit)
	if err != nil {
		c.log.Error("tidal get artist top tracks failed", "error", err, "artistID", providerArtistID, "component", "tidal")
		return nil, err
	}
	out := make([]discovery.TrackInfo, len(tracks))
	for i, t := range tracks {
		out[i] = discovery.TrackInfo{
			ProviderID:  strconv.FormatInt(t.ID, 10),
			ArtistName:  t.Artist.Name,
			AlbumTitle:  t.Album.Title,
			Title:       t.Title,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.VolumeNumber,
			DurationMs:  int64(t.Duration) * 1000,
			ISRC:        t.ISRC,
		}
	}
	return out, nil
}

// ─── playlist.PlaylistSourceProvider ─────────────────────────────────────

// PlaylistSource returns a playlist.Source adapter wrapping this client.
func (c *Client) PlaylistSource() playlist.Source {
	return c.playlistAdapter
}

// ─── OAuth Device Flow ──────────────────────────────────────────────────

// tidalDefaultClientID is Tidal's public web player client ID, used for the
// device code OAuth flow. Extracted from go-tiddl's built-in credentials.
// User-provided Developer Portal apps typically don't support the device code
// grant type, so we always use the web player's well-known client.
const tidalDefaultClientID = "4N3n6Q1x95LL5K7p"
const tidalDefaultClientSecret = "oKOXfJW371cX6xaZ0PyhgGNBdNLlBZd4AKKYougMjik="

// StartDeviceAuth initiates the Tidal device authorization flow.
// Returns the user code and verification URL for the user to open in a browser.
// The device code is stored on the Client for a subsequent CompleteDeviceAuth call.
//
// Uses Tidal's public web player client credentials (not the user's Developer Portal
// credentials) because the device code grant type is only available to limited-input
// device clients.
func (c *Client) StartDeviceAuth(ctx context.Context) (userCode string, verificationURL string, deviceCode string, expiresAt time.Time, err error) {
	form := url.Values{}
	form.Set("client_id", tidalDefaultClientID)
	form.Set("scope", "r_usr w_usr w_sub")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://auth.tidal.com/v1/oauth2/device_authorization",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("tidal: create device auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("tidal: device auth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", "", time.Time{}, fmt.Errorf("tidal: device auth HTTP %d: %s", resp.StatusCode, string(body))
	}

	var authReq tiddl.AuthRequest
	if err := json.NewDecoder(resp.Body).Decode(&authReq); err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("tidal: decode device auth response: %w", err)
	}

	c.pendingAuthMu.Lock()
	c.pendingAuth = &authReq
	c.pendingAuthMu.Unlock()

	return authReq.UserCode, authReq.VerificationUriComplete.String(), authReq.DeviceCode, authReq.Expires, nil
}

// CompleteDeviceAuth completes the device authorization after the user has approved.
// It exchanges the device code for tokens directly (bypassing go-tiddl's oauth2 HTTP client)
// then calls SetToken on the tiddl client so subsequent API calls are authenticated.
func (c *Client) CompleteDeviceAuth(ctx context.Context) error {
	c.pendingAuthMu.Lock()
	req := c.pendingAuth
	c.pendingAuthMu.Unlock()

	if req == nil {
		return fmt.Errorf("tidal: no pending device authorization — call StartDeviceAuth first")
	}

	// Exchange device code for tokens.
	form := url.Values{}
	form.Set("client_id", tidalDefaultClientID)
	form.Set("device_code", req.DeviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("scope", "r_usr w_usr w_sub")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://auth.tidal.com/v1/oauth2/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("tidal: create token request: %w", err)
	}
	httpReq.SetBasicAuth(tidalDefaultClientID, tidalDefaultClientSecret)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("tidal: token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Check for pending/expired errors.
		var devErr struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if json.Unmarshal(body, &devErr) == nil && devErr.Error != "" {
			if devErr.Error == "authorization_pending" {
				return fmt.Errorf("tidal: %s (%s)", devErr.Error, devErr.ErrorDescription)
			}
			return fmt.Errorf("tidal: %s (%s)", devErr.Error, devErr.ErrorDescription)
		}
		return fmt.Errorf("tidal: token endpoint HTTP %d: %s", resp.StatusCode, string(body))
	}

	var authResult tiddl.AuthResult
	if err := json.Unmarshal(body, &authResult); err != nil {
		return fmt.Errorf("tidal: decode token response: %w", err)
	}

	// Feed the token into go-tiddl for subsequent authenticated API calls.
	c.tiddlClient.SetToken(authResult.Token)

	// Persist token callback so future refreshes update the config.
	c.tiddlClient.SetTokenChanged(func(tok *oauth2.Token) {
		c.mu.Lock()
		c.cfg.AccessToken = tok.AccessToken
		c.cfg.RefreshToken = tok.RefreshToken
		c.api.SetToken(tok.AccessToken)
		if c.tokenPersist != nil {
			c.tokenPersist(tok.AccessToken, tok.RefreshToken)
		}
		c.mu.Unlock()
	})

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()

	// Clear pending auth — token exchange succeeded.
	c.pendingAuthMu.Lock()
	c.pendingAuth = nil
	c.pendingAuthMu.Unlock()

	c.log.Info("tidal OAuth completed", "userID", authResult.UserID, "component", "tidal")
	return nil
}

// ─── Session Token ──────────────────────────────────────────────────────

// GetTokenJSON returns the current OAuth2 token as JSON for config persistence.
func (c *Client) GetTokenJSON() (json.RawMessage, error) {
	tok, err := c.tiddlClient.Token()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(tok)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// ─── Internal Helpers ───────────────────────────────────────────────────

// searchAlbumsInternal is the shared implementation used by SearchAlbums and SearchCover fallback.
func (c *Client) searchAlbumsInternal(ctx context.Context, query string, limit int) ([]discovery.AlbumResult, error) {
	albums, err := c.api.SearchAlbums(ctx, query, limit, 0)
	if err != nil {
		c.log.Error("tidal search albums failed", "error", err, "query", query, "component", "tidal")
		return nil, err
	}
	out := make([]discovery.AlbumResult, 0, len(albums))
	for _, a := range albums {
		coverURL := ""
		if a.Cover != "" {
			coverURL = ImageURL(a.Cover, 640, 640)
		}
		year := 0
		if len(a.ReleaseDate) >= 4 {
			if y, err := strconv.Atoi(a.ReleaseDate[:4]); err == nil {
				year = y
			}
		}
		out = append(out, discovery.AlbumResult{
			ProviderID:   strconv.FormatInt(a.ID, 10),
			ProviderName: "tidal",
			ArtistName:   a.Artist.Name,
			Title:        a.Title,
			CoverURL:     coverURL,
			TrackCount:   a.NumTracks,
			Type:         a.Type,
			Year:         year,
		})
	}
	return out, nil
}

func (c *Client) setError(downloadID, msg string) {
	c.updateRecord(downloadID, func(r *download.Record) {
		if r.State.Terminal() {
			return
		}
		r.State = download.StateFailed
		r.Error = msg
	})
}

func (c *Client) updateRecord(downloadID string, fn func(*download.Record)) {
	c.downloadsMu.Lock()
	defer c.downloadsMu.Unlock()
	if r, ok := c.downloads[downloadID]; ok {
		fn(r)
	}
}
