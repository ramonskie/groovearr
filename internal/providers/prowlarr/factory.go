// Package prowlarr implements an AlbumProvider plugin for Prowlarr/RuTracker.
package prowlarr

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/providers/musicbrainz"
)

// Factory is the plugin factory for Prowlarr.
var Factory plugin.PluginFactory = &factory{}

type factory struct {
	mbOnce   sync.Once
	mbClient *musicbrainz.APIClient
}

func (f *factory) Name() string           { return pluginName }
func (f *factory) DisplayName() string    { return displayName }
func (f *factory) Capabilities() []string { return []string{"album_search"} }

func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg Config
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, fmt.Errorf("prowlarr: invalid config: %w", err)
	}
	if resources.Logger == nil {
		resources.Logger = slog.Default()
	}

	f.mbOnce.Do(func() {
		f.mbClient = musicbrainz.NewAPIClient(musicbrainz.MusicBrainzConfig{}, resources.Logger)
	})
	return newPlugin(cfg, f.mbClient, resources.Logger)
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}
	if cfg.URL == "" {
		return fmt.Errorf("prowlarr.url: must not be empty")
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("prowlarr.api_key: must not be empty")
	}
	return nil
}

func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"url":"","api_key":"","indexer_tag":"","categories":[]}`)
}

func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:        "url",
			Type:        "text",
			Label:       "Prowlarr URL",
			Hint:        "Full URL to your Prowlarr instance (e.g. http://localhost:9696).",
			Required:    true,
			Placeholder: "http://localhost:9696",
			Validation:  &plugin.FieldValidation{Format: "url"},
		},
		{
			Name:        "api_key",
			Type:        "password",
			Label:       "API Key",
			Hint:        "Your Prowlarr API key from Settings → General.",
			Secret:      true,
			Placeholder: "Enter API key",
		},
		{
			Name:    "indexer_tag",
			Type:    "text",
			Label:   "Indexer Tag",
			Hint:    "Prowlarr tag to identify indexers (default: groovearr). Tag your RuTracker indexer with this tag.",
			Default: "groovearr",
		},
	}
}

func (f *factory) Icon() string                   { return "radio" }
func (f *factory) OAuthConfig() *plugin.OAuthInfo { return nil }
func (f *factory) UISlots() *plugin.UISlots       { return nil }
