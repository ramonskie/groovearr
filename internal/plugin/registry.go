package plugin

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Registry holds all registered plugins and factories with name/capability-based lookup.
type Registry struct {
	mu        sync.RWMutex
	plugins   map[string]BasePlugin   // canonical name → plugin
	names     []string                // insertion order
	factories map[string]PluginFactory // canonical name → factory
}

// NewRegistry creates an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins:   make(map[string]BasePlugin),
		factories: make(map[string]PluginFactory),
	}
}

// Register adds a plugin under its canonical name. Duplicates are rejected.
func (r *Registry) Register(p BasePlugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := p.Name()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("plugin %q already registered", name)
	}
	r.plugins[name] = p
	r.names = append(r.names, name)
	return nil
}

// Get returns a plugin by canonical name. Returns nil if not found.
func (r *Registry) Get(name string) BasePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.plugins[name]
}

// Names returns canonical names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.names))
	copy(out, r.names)
	return out
}

// All returns all registered plugins in registration order.
func (r *Registry) All() []BasePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]BasePlugin, 0, len(r.names))
	for _, name := range r.names {
		if p, ok := r.plugins[name]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Configured returns plugins where IsConfigured() == true.
func (r *Registry) Configured() []BasePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []BasePlugin
	for _, name := range r.names {
		if p, ok := r.plugins[name]; ok && p.IsConfigured() {
			out = append(out, p)
		}
	}
	return out
}

// WithCapability returns plugins whose factory declares the given capability.
// Useful for routing: download domain gets download-capable plugins,
// metadata domain gets metadata-capable, etc.
func (r *Registry) WithCapability(cap string) []BasePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []BasePlugin
	for name, factory := range r.factories {
		if p, ok := r.plugins[name]; ok {
			for _, c := range factory.Capabilities() {
				if c == cap {
					out = append(out, p)
					break
				}
			}
		}
	}
	return out
}

// Replace swaps an existing plugin under its canonical name.
func (r *Registry) Replace(name string, p BasePlugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[name]; !exists {
		return fmt.Errorf("plugin %q not registered", name)
	}
	r.plugins[name] = p
	return nil
}

// RegisterFactory registers a plugin factory for deferred construction.
func (r *Registry) RegisterFactory(f PluginFactory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := f.Name()
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("plugin factory %q already registered", name)
	}
	r.factories[name] = f
	return nil
}

// InitAll creates and registers plugins from all registered factories using
// the provided sources config map. Sources whose key does not match any
// registered factory are skipped without error.
func (r *Registry) InitAll(sources map[string]json.RawMessage, resources PluginResources) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for name, rawCfg := range sources {
		f, ok := r.factories[name]
		if !ok {
			continue
		}
		p, err := f.Create(rawCfg, resources)
		if err != nil {
			return fmt.Errorf("plugin %q: create: %w", name, err)
		}
		if _, exists := r.plugins[name]; exists {
			return fmt.Errorf("plugin %q already registered", name)
		}
		r.plugins[name] = p
		r.names = append(r.names, name)
	}
	return nil
}

// Rebuild tears down an existing plugin and recreates it with new config.
func (r *Registry) Rebuild(name string, rawCfg json.RawMessage, resources PluginResources) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	f, ok := r.factories[name]
	if !ok {
		return fmt.Errorf("plugin factory %q not registered", name)
	}
	p, err := f.Create(rawCfg, resources)
	if err != nil {
		return fmt.Errorf("plugin %q: create: %w", name, err)
	}
	r.plugins[name] = p
	found := false
	for _, n := range r.names {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		r.names = append(r.names, name)
	}
	return nil
}


