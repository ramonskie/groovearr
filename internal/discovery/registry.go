package discovery

import (
	"fmt"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// Registry wraps plugin.Registry for type-safe discovery.Provider access.
type Registry struct {
	inner *plugin.Registry
}

// NewRegistry creates a discovery registry backed by a plugin.Registry.
func NewRegistry(inner *plugin.Registry) *Registry {
	return &Registry{inner: inner}
}

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

// Configured returns all discovery providers where IsConfigured() is true.
func (r *Registry) Configured() []Provider {
	plugins := r.inner.WithCapability("discovery")
	var out []Provider
	for _, p := range plugins {
		if dp, ok := p.(Provider); ok && dp.IsConfigured() {
			out = append(out, dp)
		}
	}
	return out
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
