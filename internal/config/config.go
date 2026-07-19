// Package config provides application configuration loading and validation.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Config holds all application settings.
type Config struct {
	Soulseek SoulseekConfig `json:"soulseek"`
	Deezer   DeezerConfig   `json:"deezer"`
	Library  LibraryConfig  `json:"library"`
	Quality  QualityConfig  `json:"quality"`
}

// SoulseekConfig holds slskd connection settings.
type SoulseekConfig struct {
	SlskdURL       string `json:"slskd_url"`       // e.g. "http://localhost:5030"
	APIKey         string `json:"api_key"`          // X-API-Key for slskd
	SearchTimeout  int    `json:"search_timeout"`   // seconds, default: 60
	MinUploadSpeed int    `json:"min_upload_speed"` // Mbps, default: 0
}

// DeezerConfig holds Deezer API and download settings.
type DeezerConfig struct {
	ARL           string `json:"arl"`            // browser cookie token
	Quality       string `json:"quality"`        // flac, mp3_320, mp3_128
	AllowFallback *bool  `json:"allow_fallback"` // try lower quality if preferred unavailable (nil = true)
	AccessToken   string `json:"access_token"`   // OAuth token for user data (optional)
}

// LibraryConfig holds music library paths.
type LibraryConfig struct {
	DownloadPath     string `json:"download_path"`     // download staging directory
	LibraryPath      string `json:"library_path"`      // where organized downloads end up
	FolderTemplate   string `json:"folder_template"`   // e.g. "{artist}/{album} ({year})/{track:02d} - {title}"
	PlaylistPath     string `json:"playlist_path"`     // separate folder for playlist downloads
	PlaylistTemplate string `json:"playlist_template"` // e.g. "{position:02d} {artist} - {title}"
}

// QualityConfig holds quality preferences for downloads.
type QualityConfig struct {
	PreferredFormat string `json:"preferred_format"` // flac, mp3, any
	MinBitrate      int    `json:"min_bitrate"`      // kbps, 0 = no minimum
}

var validQualities = map[string]bool{"flac": true, "mp3_320": true, "mp3_128": true}
var validFormats = map[string]bool{"flac": true, "mp3": true, "any": true}
var folderTokenRE = regexp.MustCompile(`\{[a-z_][a-z0-9_:]*\}`)

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	allowFallback := true
	return Config{
		Soulseek: SoulseekConfig{
			SlskdURL:      "",
			SearchTimeout: 60,
		},
		Deezer: DeezerConfig{
			Quality:       "flac",
			AllowFallback: &allowFallback,
		},
		Library: LibraryConfig{
			DownloadPath:     "./downloads",
			LibraryPath:      "./music",
			FolderTemplate:   "{artist}/{album} ({year})/{track:02d} - {title}",
			PlaylistPath:     "./playlists",
			PlaylistTemplate: "{position:02d} {artist} - {title}",
		},
		Quality: QualityConfig{
			PreferredFormat: "flac",
		},
	}
}

// Validate checks the config for errors and returns human-readable messages.
// Empty list means valid.
func (c Config) Validate() []string {
	var errs []string

	// Soulseek URL (optional — only validate if set).
	if c.Soulseek.SlskdURL != "" {
		u, err := url.Parse(c.Soulseek.SlskdURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Sprintf("soulseek.slskd_url: must be a valid absolute URL (e.g. http://localhost:5030), got %q", c.Soulseek.SlskdURL))
		}
	}
	if c.Soulseek.SearchTimeout < 1 {
		errs = append(errs, "soulseek.search_timeout: must be >= 1 second")
	}
	if c.Soulseek.MinUploadSpeed < 0 {
		errs = append(errs, "soulseek.min_upload_speed: must be >= 0")
	}

	// Deezer quality.
	if c.Deezer.ARL != "" && c.Deezer.Quality != "" && !validQualities[c.Deezer.Quality] {
		errs = append(errs, fmt.Sprintf("deezer.quality: must be one of flac, mp3_320, mp3_128 (got %q)", c.Deezer.Quality))
	}

	// Library.
	if c.Library.FolderTemplate != "" {
		// Warn if template has no known tokens.
		if !folderTokenRE.MatchString(c.Library.FolderTemplate) {
			errs = append(errs, "library.folder_template: contains no recognized tokens (e.g. {artist}, {album})")
		}
	}
	if c.Library.LibraryPath != "" && strings.Contains(c.Library.LibraryPath, "\x00") {
		errs = append(errs, "library.library_path: contains null bytes")
	}

	// Quality.
	if c.Quality.PreferredFormat != "" && !validFormats[c.Quality.PreferredFormat] {
		errs = append(errs, fmt.Sprintf("quality.preferred_format: must be flac, mp3, or any (got %q)", c.Quality.PreferredFormat))
	}
	if c.Quality.MinBitrate < 0 {
		errs = append(errs, "quality.min_bitrate: must be >= 0")
	}

	return errs
}

// Merge copies non-zero fields from partial into c.
func (c *Config) Merge(partial *Config) {
	if partial.Soulseek.SlskdURL != "" {
		c.Soulseek.SlskdURL = partial.Soulseek.SlskdURL
	}
	if partial.Soulseek.APIKey != "" {
		c.Soulseek.APIKey = partial.Soulseek.APIKey
	}
	if partial.Soulseek.SearchTimeout > 0 {
		c.Soulseek.SearchTimeout = partial.Soulseek.SearchTimeout
	}
	if partial.Soulseek.MinUploadSpeed > 0 {
		c.Soulseek.MinUploadSpeed = partial.Soulseek.MinUploadSpeed
	}
	if partial.Deezer.ARL != "" {
		c.Deezer.ARL = partial.Deezer.ARL
	}
	if partial.Deezer.Quality != "" {
		c.Deezer.Quality = partial.Deezer.Quality
	}
	if partial.Deezer.AccessToken != "" {
		c.Deezer.AccessToken = partial.Deezer.AccessToken
	}
	if partial.Deezer.AllowFallback != nil {
		c.Deezer.AllowFallback = partial.Deezer.AllowFallback
	}

	if partial.Library.DownloadPath != "" {
		c.Library.DownloadPath = partial.Library.DownloadPath
	}
	if partial.Library.FolderTemplate != "" {
		c.Library.FolderTemplate = partial.Library.FolderTemplate
	}
	if partial.Library.LibraryPath != "" {
		c.Library.LibraryPath = partial.Library.LibraryPath
	}
	if partial.Library.PlaylistPath != "" {
		c.Library.PlaylistPath = partial.Library.PlaylistPath
	}
	if partial.Library.PlaylistTemplate != "" {
		c.Library.PlaylistTemplate = partial.Library.PlaylistTemplate
	}

	if partial.Quality.PreferredFormat != "" {
		c.Quality.PreferredFormat = partial.Quality.PreferredFormat
	}
	if partial.Quality.MinBitrate > 0 {
		c.Quality.MinBitrate = partial.Quality.MinBitrate
	}
}

// Load reads config from a JSON file, falling back to defaults for missing fields.
func Load(path string) (Config, error) {
	cfg, err := readConfigFile(path)
	if err != nil {
		return cfg, err
	}

	// Expand relative paths.
	expandPaths(&cfg)

	// Log validation warnings at startup.
	if errs := cfg.Validate(); len(errs) > 0 {
		log.Printf("config: validation warnings in %s:", path)
		for _, e := range errs {
			log.Printf("  - %s", e)
		}
	}

	return cfg, nil
}

// readConfigFile reads a JSON config file, merging onto DefaultConfig.
// Returns DefaultConfig if the file does not exist.
func readConfigFile(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// expandPaths converts relative library paths to absolute.
func expandPaths(cfg *Config) {
	if cfg.Library.DownloadPath != "" && !filepath.IsAbs(cfg.Library.DownloadPath) {
		if abs, err := filepath.Abs(cfg.Library.DownloadPath); err == nil {
			cfg.Library.DownloadPath = abs
		}
	}
	if cfg.Library.LibraryPath != "" && !filepath.IsAbs(cfg.Library.LibraryPath) {
		if abs, err := filepath.Abs(cfg.Library.LibraryPath); err == nil {
			cfg.Library.LibraryPath = abs
		}
	}
	if cfg.Library.PlaylistPath != "" && !filepath.IsAbs(cfg.Library.PlaylistPath) {
		if abs, err := filepath.Abs(cfg.Library.PlaylistPath); err == nil {
			cfg.Library.PlaylistPath = abs
		}
	}
}
