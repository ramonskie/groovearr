// Package qbittorrent implements a DownloadClient plugin for qBittorrent WebUI API v2.
package qbittorrent

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// Factory is the plugin factory for qBittorrent.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string           { return pluginName }
func (f *factory) DisplayName() string    { return displayName }
func (f *factory) Capabilities() []string { return []string{"download_client"} }

func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg Config
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, fmt.Errorf("qbittorrent: invalid config: %w", err)
	}
	if resources.Logger == nil {
		resources.Logger = slog.Default()
	}
	dlPath := cfg.DownloadPath
	if dlPath == "" {
		dlPath = resources.DownloadPath // fallback to global library.download_path
	}
	return newPlugin(cfg, dlPath, resources.Logger)
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}
	if cfg.URL == "" {
		return fmt.Errorf("qbittorrent.url: must not be empty")
	}
	return nil
}

func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"url":"","api_key":"","enabled":true,"download_path":"","qbt_download_root":"/downloads","category":"music","remove_completed":true}`)
}

func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:    "enabled",
			Type:    "toggle",
			Label:   "Enabled",
			Hint:    "Enable or disable qBittorrent. When disabled, health checks are skipped.",
			Default: "true",
		},
		{
			Name:        "url",
			Type:        "text",
			Label:       "WebUI URL",
			Hint:        "Full URL to your qBittorrent WebUI (e.g. http://localhost:8080).",
			Required:    true,
			Placeholder: "http://localhost:8080",
			Validation:  &plugin.FieldValidation{Format: "url"},
		},
		{
			Name:        "api_key",
			Type:        "password",
			Label:       "API Key",
			Hint:        "API key from qBittorrent WebUI → Tools → Options → Web UI → Authentication.",
			Required:    true,
			Secret:      true,
			Placeholder: "Enter API key",
		},
		{
			Name:        "download_path",
			Type:        "text",
			Label:       "Download Path",
			Hint:        "Override the global download path for qBittorrent downloads. Leave empty to use library.download_path.",
			Placeholder: "./downloads",
		},
		{
			Name:        "qbt_download_root",
			Type:        "text",
			Label:       "qBittorrent Root",
			Hint:        "Download root path inside qBittorrent's container (default: /downloads). Only needed when running in Docker with different volume mounts.",
			Default:     "/downloads",
		},
		{
			Name:    "category",
			Type:    "text",
			Label:   "Category",
			Hint:    "qBittorrent category for music downloads.",
			Default: "music",
		},
	}
}

func (f *factory) Icon() string                   { return "disc" }
func (f *factory) OAuthConfig() *plugin.OAuthInfo { return nil }
func (f *factory) UISlots() *plugin.UISlots       { return nil }
