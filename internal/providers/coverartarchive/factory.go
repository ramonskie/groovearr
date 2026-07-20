package coverartarchive

import (
	"encoding/json"

	"github.com/ramonskie/groovearr/internal/plugin"
)

const pluginName = "coverartarchive"
const displayName = "Cover Art Archive"

// Config holds Cover Art Archive plugin configuration. No credentials required.
type Config struct{}

// Factory is the plugin factory for Cover Art Archive metadata provider.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string          { return pluginName }
func (f *factory) DisplayName() string   { return displayName }
func (f *factory) Capabilities() []string { return []string{"metadata"} }

func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	return NewClient(), nil
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg Config
	return json.Unmarshal(rawCfg, &cfg)
}

func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{}`)
}
