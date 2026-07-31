package deezer

import (
	"encoding/json"
	"fmt"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// DeezerConfig holds Deezer-specific plugin configuration.
type DeezerConfig struct {
	ARL           string `json:"arl"`
	Quality       string `json:"quality"`
	AllowFallback *bool  `json:"allow_fallback"`
	AccessToken   string `json:"access_token"`
	Enabled       bool   `json:"enabled"` // user-facing enable/disable toggle (default true)
}

// Factory is the plugin factory for Deezer.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string        { return downloadPluginName }
func (f *factory) DisplayName() string { return downloadDisplayName }

// Capabilities returns the capability domains this plugin provides.
func (f *factory) Capabilities() []string {
	return []string{"download", "playlist", "discovery", "metadata"}
}

// Create builds a Deezer download client from raw JSON config and runtime resources.
func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg DeezerConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, err
	}
	return NewDownloadClient(cfg, resources.DownloadPath, resources.Logger), nil
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg DeezerConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}
	if cfg.ARL != "" && cfg.Quality != "" {
		valid := map[string]bool{"flac": true, "mp3_320": true, "mp3_128": true}
		if !valid[cfg.Quality] {
			return fmt.Errorf("deezer.quality: must be flac, mp3_320, or mp3_128")
		}
	}
	return nil
}

func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"arl":"","quality":"flac","allow_fallback":true,"access_token":"","enabled":true}`)
}

func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:    "enabled",
			Type:    "toggle",
			Label:   "Enabled",
			Hint:    "Enable or disable Deezer. When disabled, health checks are skipped.",
			Default: "true",
		},
		{
			Name:        "arl",
			Type:        "password",
			Label:       "ARL Token",
			Hint:        "Your Deezer ARL token for authentication.",
			Secret:      true,
			Placeholder: "Enter ARL token",
		},
		{
			Name:    "quality",
			Type:    "select",
			Label:   "Quality",
			Hint:    "Preferred download quality for Deezer tracks.",
			Default: "flac",
			Options: []plugin.FieldOption{
				{Value: "flac", Label: "FLAC Lossless"},
				{Value: "mp3_320", Label: "MP3 320kbps"},
				{Value: "mp3_128", Label: "MP3 128kbps"},
			},
		},
	}
}

func (f *factory) Icon() string                   { return "music2" }
func (f *factory) OAuthConfig() *plugin.OAuthInfo { return nil }
func (f *factory) UISlots() *plugin.UISlots {
	return &plugin.UISlots{
		PlaylistBrowser: true,
		ImportURLPatterns: []plugin.ImportPattern{
			{Pattern: `/playlist/(\d+)(?:[/?#]|$)`, Label: "Deezer playlist URL"},
			{Pattern: `^(\d+)$`, Label: "Numeric Deezer ID", IsFallback: true},
		},
	}
}
