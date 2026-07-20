package download

import (
	"encoding/json"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// Registry is a type-safe wrapper around plugin.Registry for download capabilities.
// It ensures Get/All/Configured return download.Plugin (not plugin.BasePlugin),
// so callers don't need type assertions everywhere.
type Registry struct {
	inner *plugin.Registry
}

// NewRegistry creates a registry for download plugins.
func NewRegistry() *Registry {
	return &Registry{inner: plugin.NewRegistry()}
}

// NewRegistryFrom wraps an existing plugin.Registry with download type-safety.
// Multiple typed registries can share the same inner Registry for capability-based routing.
func NewRegistryFrom(inner *plugin.Registry) *Registry {
	return &Registry{inner: inner}
}

// Inner returns the underlying plugin.Registry for capability-based queries,
// playlist wiring, and other cross-domain access.
func (r *Registry) Inner() *plugin.Registry { return r.inner }

// Register adds a download plugin. Duplicates are rejected.
func (r *Registry) Register(p Plugin) error { return r.inner.Register(p) }

// Get returns a download plugin by canonical name, or nil.
func (r *Registry) Get(name string) Plugin {
	bp := r.inner.Get(name)
	if p, ok := bp.(Plugin); ok {
		return p
	}
	return nil
}

// Names returns canonical names in registration order.
func (r *Registry) Names() []string { return r.inner.Names() }

// All returns all registered download plugins in registration order.
// Non-download plugins are silently skipped.
func (r *Registry) All() []Plugin {
	bps := r.inner.All()
	out := make([]Plugin, 0, len(bps))
	for _, bp := range bps {
		if p, ok := bp.(Plugin); ok {
			out = append(out, p)
		}
	}
	return out
}

// Configured returns download plugins where IsConfigured() == true.
// Non-download plugins are silently skipped.
func (r *Registry) Configured() []Plugin {
	bps := r.inner.Configured()
	out := make([]Plugin, 0, len(bps))
	for _, bp := range bps {
		if p, ok := bp.(Plugin); ok {
			out = append(out, p)
		}
	}
	return out
}

// Replace swaps an existing download plugin under its canonical name.
func (r *Registry) Replace(name string, p Plugin) error { return r.inner.Replace(name, p) }

// RegisterFactory registers a plugin factory for deferred construction.
func (r *Registry) RegisterFactory(f plugin.PluginFactory) error {
	return r.inner.RegisterFactory(f)
}

// InitAll creates and registers plugins from all registered factories
// using the provided sources config map.
func (r *Registry) InitAll(sources map[string]json.RawMessage, resources plugin.PluginResources) error {
	return r.inner.InitAll(sources, resources)
}

// Rebuild tears down an existing plugin and recreates it with new config.
func (r *Registry) Rebuild(name string, rawCfg json.RawMessage, resources plugin.PluginResources) error {
	return r.inner.Rebuild(name, rawCfg, resources)
}
