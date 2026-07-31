// Package plugin defines the shared plugin framework used by all capability domains
// (download, metadata, notify, etc.). It provides the base interfaces that
// domain-specific plugins extend, plus a generic registry and factory system.
package plugin

import "context"

// BasePlugin is the minimal contract every plugin must satisfy.
// Domain-specific interfaces (e.g. download.Plugin, metadata.Provider) embed this.
type BasePlugin interface {
	// Name returns the canonical source name (e.g. "soulseek", "deezer").
	Name() string

	// DisplayName returns a human-readable label (e.g. "Soulseek", "Deezer").
	DisplayName() string

	// IsConfigured returns true if the plugin has valid credentials/settings
	// for its primary capability (usually download).
	IsConfigured() bool

	// CheckConnection probes the plugin's backend for reachability.
	CheckConnection(ctx context.Context) error

	// Connected returns true if the plugin has been verified (auth tested, API reachable).
	Connected() bool

	// CapabilityStatus returns per-capability connection status.
	// Values: "connected", "configured", "not_configured".
	// Plugins with split capabilities (e.g., Deezer metadata without ARL)
	// can report different status per capability.
	CapabilityStatus() map[string]string
}

// Enabler is an optional interface for plugins that support explicit
// enable/disable via configuration. Plugins that do NOT implement this
// are implicitly always enabled. The health checker and other subsystems
// use this to skip disabled plugins.
type Enabler interface {
	// IsEnabled returns false when the plugin has been explicitly disabled
	// by the user (enabled: false in config). Default is true.
	IsEnabled() bool
}
