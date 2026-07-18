package download

import (
	"fmt"
	"sync"
)

// Registry holds all registered download plugins and provides name-based lookup.
// Plugins are registered by name; duplicates are rejected.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin       // canonical name → plugin
	aliases map[string]string       // alias → canonical name
	names   []string                // insertion order
}

// NewRegistry creates an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins: make(map[string]Plugin),
		aliases: make(map[string]string),
	}
}

// Register adds a plugin under its canonical name and optional aliases.
// Aliases allow legacy names (e.g. "deezer_dl" → "deezer") to resolve correctly.
func (r *Registry) Register(p Plugin, aliases ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := p.Name()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("download plugin %q already registered", name)
	}
	r.plugins[name] = p
	r.names = append(r.names, name)
	for _, alias := range aliases {
		if alias == name {
			continue
		}
		if _, exists := r.aliases[alias]; exists {
			return fmt.Errorf("download plugin alias %q already registered", alias)
		}
		r.aliases[alias] = name
	}
	return nil
}

// Get returns the plugin for a canonical name or alias. Returns nil if not found.
func (r *Registry) Get(name string) Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if p, ok := r.plugins[name]; ok {
		return p
	}
	if canonical, ok := r.aliases[name]; ok {
		return r.plugins[canonical]
	}
	return nil
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
func (r *Registry) All() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Plugin, 0, len(r.names))
	for _, name := range r.names {
		if p, ok := r.plugins[name]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Replace swaps an existing plugin under its canonical name.
// Returns an error if the name is not already registered.
func (r *Registry) Replace(name string, p Plugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[name]; !exists {
		return fmt.Errorf("download plugin %q not registered", name)
	}
	r.plugins[name] = p
	return nil
}

// Configured returns plugins that report IsConfigured() == true.
func (r *Registry) Configured() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Plugin
	for _, name := range r.names {
		p, ok := r.plugins[name]
		if !ok {
			continue
		}
		if p.IsConfigured() {
			out = append(out, p)
		}
	}
	return out
}
