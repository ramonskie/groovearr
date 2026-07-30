// Package soulseek implements a download plugin for slskd (Soulseek daemon REST API).
package soulseek

import (
	"encoding/json"
	"fmt"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// Factory is the plugin factory for Soulseek (slskd).
// It implements plugin.PluginFactory for self-registration via the plugin registry.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string        { return pluginName }  // "soulseek"
func (f *factory) DisplayName() string { return displayName } // "Soulseek"

// Capabilities returns the capability domains this plugin provides.
func (f *factory) Capabilities() []string { return []string{"download"} }

// Create builds a Soulseek Client from raw JSON config and runtime resources.
func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	client, err := New(rawCfg, resources.DownloadPath, resources.Logger)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// ValidateConfig checks whether raw config is structurally valid for Soulseek.
func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg SoulseekConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}
	if cfg.SearchTimeout < 0 {
		return fmt.Errorf("soulseek.search_timeout: must be >= 0 (got %d)", cfg.SearchTimeout)
	}
	if cfg.MinUploadSpeed < 0 {
		return fmt.Errorf("soulseek.min_upload_speed: must be >= 0 (got %d)", cfg.MinUploadSpeed)
	}
	return nil
}

// DefaultConfig returns the default Soulseek configuration as raw JSON.
func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"slskd_url":"","api_key":"","download_path":"","search_timeout":90,"min_upload_speed":0}`)
}

func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:        "slskd_url",
			Type:        "text",
			Label:       "slskd URL",
			Hint:        "Full URL to your slskd instance (e.g. https://slskd.example.com:5030).",
			Required:    true,
			Placeholder: "https://slskd.example.com:5030",
			Validation:  &plugin.FieldValidation{Format: "url"},
		},
		{
			Name:        "api_key",
			Type:        "password",
			Label:       "API Key",
			Hint:        "Your slskd API key from the slskd web interface.",
			Secret:      true,
			Placeholder: "Enter API key",
		},
		{
			Name:        "download_path",
			Type:        "text",
			Label:       "Download Path",
			Hint:        "Override the global download path for Soulseek downloads. Leave empty to use library.download_path.",
			Placeholder: "./downloads",
		},
	}
}

func (f *factory) Icon() string                   { return "globe" }
func (f *factory) OAuthConfig() *plugin.OAuthInfo { return nil }
func (f *factory) UISlots() *plugin.UISlots       { return nil }
