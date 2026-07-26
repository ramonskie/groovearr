// Package discogs implements a Discogs discovery plugin.
package discogs

import (
	"encoding/json"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// DiscogsConfig holds Discogs-specific plugin configuration.
type DiscogsConfig struct {
	ConsumerKey    string `json:"consumer_key"`
	ConsumerSecret string `json:"consumer_secret"`
}

// Factory is the plugin factory for Discogs.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string        { return pluginName }
func (f *factory) DisplayName() string { return displayName }

// Capabilities returns the capability domains this plugin provides.
// Discogs is metadata-only — search API returns no images and the
// 25 req/min unauthenticated rate limit makes discovery browsing impractical.
func (f *factory) Capabilities() []string { return []string{"metadata"} }

// Create builds a Discogs plugin from raw JSON config and runtime resources.
func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg DiscogsConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, err
	}
	return NewPlugin(cfg, resources.Logger), nil
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg DiscogsConfig
	return json.Unmarshal(rawCfg, &cfg)
}

func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"consumer_key":"","consumer_secret":""}`)
}

func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:        "consumer_key",
			Type:        "text",
			Label:       "Consumer Key (optional)",
			Hint:        "Discogs API consumer key. Leave empty for anonymous access (lower rate limits).",
			Placeholder: "Consumer key",
		},
		{
			Name:        "consumer_secret",
			Type:        "password",
			Label:       "Consumer Secret (optional)",
			Hint:        "Discogs API consumer secret. Only needed with consumer key.",
			Secret:      true,
			Placeholder: "Consumer secret",
		},
	}
}

func (f *factory) Icon() string                  { return "disc" }
func (f *factory) OAuthConfig() *plugin.OAuthInfo { return nil }
func (f *factory) UISlots() *plugin.UISlots       { return nil }
