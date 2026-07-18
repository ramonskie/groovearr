// Package config provides application configuration loading.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	SlskdURL      string `json:"slskd_url"`       // e.g. "http://localhost:5030"
	APIKey        string `json:"api_key"`          // X-API-Key for slskd
	DownloadPath  string `json:"download_path"`    // default: "./downloads"
	SearchTimeout int    `json:"search_timeout"`   // seconds, default: 60
	MinUploadSpeed int  `json:"min_upload_speed"`  // Mbps, default: 0
}

// DeezerConfig holds Deezer API and download settings.
type DeezerConfig struct {
	ARL            string `json:"arl"`              // browser cookie token
	Quality        string `json:"quality"`          // flac, mp3_320, mp3_128
	AllowFallback  bool   `json:"allow_fallback"`   // try lower quality if preferred unavailable
	AccessToken    string `json:"access_token"`     // OAuth token for user data (optional)
}

// LibraryConfig holds music library paths.
type LibraryConfig struct {
	MusicPaths       []string `json:"music_paths"`
	MusicVideosPath  string   `json:"music_videos_path,omitempty"`
	FolderTemplate   string   `json:"folder_template"` // e.g. "{artist}/{album} ({year})/{track:02d} - {title}"
}

// QualityConfig holds quality preferences for downloads.
type QualityConfig struct {
	PreferredFormat string `json:"preferred_format"` // flac, mp3, any
	MinBitrate      int    `json:"min_bitrate"`      // kbps, 0 = no minimum
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Soulseek: SoulseekConfig{
			SlskdURL:      "",
			DownloadPath:  "./downloads",
			SearchTimeout: 60,
		},
		Deezer: DeezerConfig{
			Quality:       "flac",
			AllowFallback: true,
		},
		Library: LibraryConfig{
			FolderTemplate: "{artist}/{album} ({year})/{track:02d} - {title}",
		},
		Quality: QualityConfig{
			PreferredFormat: "flac",
		},
	}
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

	return cfg, nil
}
