package plugin

import (
	"encoding/json"
	"log/slog"
)

// PluginResources holds runtime resources that factories need to construct plugins.
// Domain-specific factories can embed this or define their own.
type PluginResources struct {
	DownloadPath string       // where downloads for this source will be stored on disk
	Logger       *slog.Logger // structured logger for plugin use
}

// PluginFactory is implemented by each plugin package to enable self-registration.
// The factory handles config parsing, validation, construction, and capability discovery.
type PluginFactory interface {
	// Name returns the canonical source name (e.g. "soulseek", "deezer").
	Name() string

	// DisplayName returns a human-readable label (e.g. "Soulseek", "Deezer").
	DisplayName() string

	// Capabilities returns the list of capability domains this plugin provides.
	// Examples: ["download"], ["download", "playlist"], ["metadata"].
	// Used by the registry for capability-based routing.
	Capabilities() []string

	// Create builds a BasePlugin from raw JSON config and runtime resources.
	Create(rawCfg json.RawMessage, resources PluginResources) (BasePlugin, error)

	// ValidateConfig checks whether the raw config is structurally valid.
	ValidateConfig(rawCfg json.RawMessage) error

	// DefaultConfig returns the default configuration for this plugin as raw JSON.
	DefaultConfig() json.RawMessage
}
