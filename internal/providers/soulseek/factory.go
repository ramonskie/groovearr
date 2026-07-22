// Package soulseek implements a download plugin for slskd (Soulseek daemon REST API).
package soulseek

import (
	"encoding/json"

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
// Actual connectivity testing happens at connection time, not here.
func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg SoulseekConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}
	return nil
}

// DefaultConfig returns the default Soulseek configuration as raw JSON.
func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"slskd_url":"","api_key":"","search_timeout":60,"min_upload_speed":0}`)
}
