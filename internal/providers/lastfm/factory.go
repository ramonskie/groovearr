// Package lastfm implements a Last.fm discovery plugin.
package lastfm

import (
	"encoding/json"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// LastFMConfig holds Last.fm-specific plugin configuration.
type LastFMConfig struct {
	APIKey string `json:"api_key"`
}

// Factory is the plugin factory for Last.fm.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string        { return pluginName }
func (f *factory) DisplayName() string { return displayName }

// Capabilities returns the capability domains this plugin provides.
func (f *factory) Capabilities() []string { return []string{"discovery", "metadata"} }

// Create builds a Last.fm plugin from raw JSON config and runtime resources.
func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg LastFMConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, err
	}
	return NewPlugin(cfg, resources.Logger), nil
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg LastFMConfig
	return json.Unmarshal(rawCfg, &cfg)
}

func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"api_key":""}`)
}

func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:        "api_key",
			Type:        "text",
			Label:       "API Key",
			Hint:        "Free API key from last.fm/api/account/create. Required for artist images, metadata, and discovery.",
			Placeholder: "Your Last.fm API key",
		},
	}
}

func (f *factory) Icon() string                  { return "radio" }
func (f *factory) OAuthConfig() *plugin.OAuthInfo { return nil }
func (f *factory) UISlots() *plugin.UISlots       { return nil }
