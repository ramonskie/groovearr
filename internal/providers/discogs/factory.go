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
func (f *factory) Capabilities() []string { return []string{"discovery", "metadata"} }

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
