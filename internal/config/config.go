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
	SlskdURL      string `json:"slskd_url"`      // e.g. "http://localhost:5030"
	APIKey        string `json:"api_key"`         // X-API-Key for slskd
	DownloadPath  string `json:"download_path"`   // default: "./downloads"
	SearchTimeout int    `json:"search_timeout"`  // seconds, default: 60
	MinUploadSpeed int  `json:"min_upload_speed"` // Mbps, default: 0
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
	RootPath       string   `json:"root_path"`       // where organized downloads end up
	MusicPaths     []string `json:"music_paths"`     // additional directories to scan
	FolderTemplate string   `json:"folder_template"` // e.g. "{artist}/{album} ({year})/{track:02d} - {title}"
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
			DownloadPath:  "./downloads",
			SearchTimeout: 60,
		},
		Deezer: DeezerConfig{
			Quality:       "flac",
			AllowFallback: &allowFallback,
		},
		Library: LibraryConfig{
			RootPath:      "./music",
			FolderTemplate: "{artist}/{album} ({year})/{track:02d} - {title}",
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
	if c.Library.RootPath != "" && strings.Contains(c.Library.RootPath, "\x00") {
		errs = append(errs, "library.root_path: contains null bytes")
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

// Load reads config from a JSON file, falling back to defaults for missing fields.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // use defaults
		}
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}

	// Expand relative paths.
	if !filepath.IsAbs(cfg.Soulseek.DownloadPath) {
		abs, err := filepath.Abs(cfg.Soulseek.DownloadPath)
		if err == nil {
			cfg.Soulseek.DownloadPath = abs
		}
	}
	if cfg.Library.RootPath != "" && !filepath.IsAbs(cfg.Library.RootPath) {
		abs, err := filepath.Abs(cfg.Library.RootPath)
		if err == nil {
			cfg.Library.RootPath = abs
		}
	}

	// Log validation warnings at startup.
	if errs := cfg.Validate(); len(errs) > 0 {
		log.Printf("config: validation warnings in %s:", path)
		for _, e := range errs {
			log.Printf("  - %s", e)
		}
	}

	return cfg, nil
}
