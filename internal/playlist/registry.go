package playlist

import "fmt"

// Registry manages playlist sources (Deezer, Spotify, Tidal, etc.).
type Registry struct {
	sources map[string]Source // name → source
}

// NewRegistry creates a playlist source registry.
func NewRegistry() *Registry {
	return &Registry{sources: make(map[string]Source)}
}

// Register adds a playlist source. Returns error if name already registered.
func (r *Registry) Register(s Source) error {
	name := s.Name()
	if _, ok := r.sources[name]; ok {
		return fmt.Errorf("playlist source %q already registered", name)
	}
	r.sources[name] = s
	return nil
}

// Get returns a source by name, or nil if not found.
func (r *Registry) Get(name string) Source {
	return r.sources[name]
}

// Configured returns all configured sources.
func (r *Registry) Configured() []Source {
	var out []Source
	for _, s := range r.sources {
		if s.IsConfigured() {
			out = append(out, s)
		}
	}
	return out
}
