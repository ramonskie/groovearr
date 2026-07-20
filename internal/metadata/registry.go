package metadata

import (
	"encoding/json"
	"log"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// Registry is a type-safe wrapper around plugin.Registry for metadata capabilities.
// It ensures Get/All/Configured return metadata.Provider (not plugin.BasePlugin),
// so callers don't need type assertions everywhere.
type Registry struct {
	inner *plugin.Registry
}

// NewRegistry creates a registry for metadata providers.
func NewRegistry() *Registry {
	return &Registry{inner: plugin.NewRegistry()}
}

// Inner returns the underlying plugin.Registry for capability-based queries
// and cross-domain access.
func (r *Registry) Inner() *plugin.Registry { return r.inner }

// Register adds a metadata provider. Duplicates are rejected.
func (r *Registry) Register(p Provider) error { return r.inner.Register(p) }

// Get returns a metadata provider by canonical name, or nil.
func (r *Registry) Get(name string) Provider {
	bp := r.inner.Get(name)
	if p, ok := bp.(Provider); ok {
		return p
	}
	if bp != nil {
		log.Printf("registry: plugin %q does not implement metadata.Provider", name)
	}
	return nil
}

// Names returns canonical names in registration order.
func (r *Registry) Names() []string { return r.inner.Names() }

// All returns all registered metadata providers in registration order.
// Non-metadata plugins are silently skipped with a warning log.
func (r *Registry) All() []Provider {
	bps := r.inner.All()
	out := make([]Provider, 0, len(bps))
	for _, bp := range bps {
		if p, ok := bp.(Provider); ok {
			out = append(out, p)
		} else {
			log.Printf("registry: plugin %q does not implement metadata.Provider, skipping", bp.Name())
		}
	}
	return out
}

// Configured returns metadata providers where IsConfigured() == true.
// Non-metadata plugins are silently skipped with a warning log.
func (r *Registry) Configured() []Provider {
	bps := r.inner.Configured()
	out := make([]Provider, 0, len(bps))
	for _, bp := range bps {
		if p, ok := bp.(Provider); ok {
			out = append(out, p)
		} else {
			log.Printf("registry: configured plugin %q does not implement metadata.Provider, skipping", bp.Name())
		}
	}
	return out
}

// Replace swaps an existing metadata provider under its canonical name.
func (r *Registry) Replace(name string, p Provider) error { return r.inner.Replace(name, p) }

// RegisterFactory registers a plugin factory for deferred construction.
func (r *Registry) RegisterFactory(f plugin.PluginFactory) error {
	return r.inner.RegisterFactory(f)
}

// InitAll creates and registers metadata providers from all registered factories
// using the provided sources config map.
func (r *Registry) InitAll(sources map[string]json.RawMessage, resources plugin.PluginResources) error {
	return r.inner.InitAll(sources, resources)
}

// Rebuild tears down an existing metadata provider and recreates it with new config.
func (r *Registry) Rebuild(name string, rawCfg json.RawMessage, resources plugin.PluginResources) error {
	return r.inner.Rebuild(name, rawCfg, resources)
}
