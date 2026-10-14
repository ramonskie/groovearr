package discovery

import (
	"fmt"
	"sync"

	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// Registry wraps plugin.Registry for type-safe discovery.Provider access.
type Registry struct {
	inner *plugin.Registry
	// mu guards order. A config update can re-wire the shared provider order
	// while discovery search handlers or the catalog refresh drain are reading
	// it, so the write and every read take the lock (D-001 §3 / AGENTS §9).
	mu sync.RWMutex
	// order is the shared provider priority (D-001 §3). When set, Configured
	// and Any return providers in that order; when nil, they keep the plugin
	// registry's native (registration) order. It is read live on every call, so
	// a settings change is honored without a restart (AGENTS §9).
	order *metadata.ProviderOrder
}

// NewRegistry creates a discovery registry backed by a plugin.Registry.
func NewRegistry(inner *plugin.Registry) *Registry {
	return &Registry{inner: inner}
}

// SetProviderOrder wires the one shared provider priority order. The registry
// holds only the live closure-backed ProviderOrder (never a snapshot), so the
// discover search handlers, catalog refresh engine, and tracking all iterate
// discovery providers in the single configured order. A nil order keeps the
// registry's native order (unchanged behavior). Safe to call concurrently with
// the readers below.
func (r *Registry) SetProviderOrder(order *metadata.ProviderOrder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = order
}

// providerOrder returns the current order under the read lock.
func (r *Registry) providerOrder() *metadata.ProviderOrder {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.order
}

// Inner returns the underlying plugin.Registry for cross-domain access.
func (r *Registry) Inner() *plugin.Registry { return r.inner }

// Get returns a discovery provider by canonical name.
func (r *Registry) Get(name string) Provider {
	p := r.inner.Get(name)
	if p == nil {
		return nil
	}
	dp, ok := p.(Provider)
	if !ok {
		return nil
	}
	return dp
}

// Configured returns all discovery providers where IsConfigured() is true,
// ordered by the shared provider priority (SetProviderOrder). A nil order keeps
// the plugin registry's native registration order.
func (r *Registry) Configured() []Provider {
	plugins := r.inner.WithCapability("discovery")
	var out []Provider
	for _, p := range plugins {
		if dp, ok := p.(Provider); ok && dp.IsConfigured() {
			out = append(out, dp)
		}
	}
	return metadata.Order(r.providerOrder(), out)
}

// Searchable returns discovery providers usable for album search: every
// configured provider PLUS any provider that exposes its metadata/discovery API
// without full credentials (metadata.Provider.IsMetadataAvailable) — e.g.
// Deezer's ARL-free public API (IsConfigured is false without an ARL, yet its
// public search/track API works). Ordering matches Configured. This is the
// selection the catalog refresh engine uses so a credential-free discovery
// source can materialize albums instead of MusicBrainz carrying the whole load.
func (r *Registry) Searchable() []Provider {
	plugins := r.inner.WithCapability("discovery")
	var out []Provider
	for _, p := range plugins {
		dp, ok := p.(Provider)
		if !ok {
			continue
		}
		if dp.IsConfigured() || publicMetadataAvailable(dp) {
			out = append(out, dp)
		}
	}
	return metadata.Order(r.providerOrder(), out)
}

// publicMetadataAvailable reports whether p exposes its API without full
// credentials (metadata.Provider.IsMetadataAvailable). Providers that do not
// implement the method are treated as unavailable.
func publicMetadataAvailable(p Provider) bool {
	m, ok := p.(interface{ IsMetadataAvailable() bool })
	return ok && m.IsMetadataAvailable()
}

// Any returns all discovery-capable plugins regardless of IsConfigured(),
// ordered by the shared provider priority (SetProviderOrder). Useful for
// best-effort operations (e.g. artist image enrichment) where a partially
// configured provider may still handle some requests.
func (r *Registry) Any() []Provider {
	plugins := r.inner.WithCapability("discovery")
	var out []Provider
	for _, p := range plugins {
		if dp, ok := p.(Provider); ok {
			out = append(out, dp)
		}
	}
	return metadata.Order(r.providerOrder(), out)
}

// RegisterFactory registers a plugin factory for a discovery provider.
func (r *Registry) RegisterFactory(f plugin.PluginFactory) error {
	if !hasCapability(f.Capabilities(), "discovery") {
		return fmt.Errorf("discovery: factory %q must declare capability \"discovery\"", f.Name())
	}
	return r.inner.RegisterFactory(f)
}

func hasCapability(caps []string, target string) bool {
	for _, c := range caps {
		if c == target {
			return true
		}
	}
	return false
}
