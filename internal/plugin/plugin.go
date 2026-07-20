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

	// IsConfigured returns true if the plugin has valid credentials/settings.
	IsConfigured() bool

	// CheckConnection probes the plugin's backend for reachability.
	CheckConnection(ctx context.Context) error

	// Connected returns true if the plugin has been verified (auth tested, API reachable).
	Connected() bool
}
