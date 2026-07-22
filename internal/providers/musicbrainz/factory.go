package musicbrainz

import (
	"encoding/json"

	"github.com/ramonskie/groovearr/internal/plugin"
)

const pluginName = "musicbrainz"
const displayName = "MusicBrainz"

// MusicBrainzConfig holds MusicBrainz-specific plugin configuration.
// Email is used in the User-Agent header per MusicBrainz API requirements.
// Empty = unconfigured (generic User-Agent used).
type MusicBrainzConfig struct {
	Email string `json:"email"`
}

// Factory is the plugin factory for MusicBrainz metadata provider.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string        { return pluginName }
func (f *factory) DisplayName() string { return displayName }
func (f *factory) Capabilities() []string { return []string{"metadata"} }

// Create builds a MusicBrainz metadata provider from raw JSON config.
func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg MusicBrainzConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, err
	}
	return NewClient(cfg, resources.Logger), nil
}

// ValidateConfig checks whether the raw config is structurally valid.
func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg MusicBrainzConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}
	return nil // any email value is valid
}

// DefaultConfig returns the default configuration for MusicBrainz.
func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"email":""}`)
}
