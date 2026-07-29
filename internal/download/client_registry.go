package download

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// DownloadClientRegistry manages DownloadClient plugins and provides
// name-based lookup. Separated from the main plugin Registry because
// download clients have a distinct lifecycle (no search, different config).
type DownloadClientRegistry struct {
	mu       sync.RWMutex
	clients  map[string]DownloadClient
	factories []downloadClientFactory
}

// downloadClientFactory wraps a plugin.PluginFactory that produces
// DownloadClient instances.
type downloadClientFactory struct {
	factory plugin.PluginFactory
}

// NewDownloadClientRegistry creates an empty DownloadClientRegistry.
func NewDownloadClientRegistry() *DownloadClientRegistry {
	return &DownloadClientRegistry{clients: make(map[string]DownloadClient)}
}

// RegisterFactory adds a factory that can produce DownloadClient instances.
func (r *DownloadClientRegistry) RegisterFactory(f plugin.PluginFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories = append(r.factories, downloadClientFactory{factory: f})
}

// Get returns a DownloadClient by name, or nil if not found.
func (r *DownloadClientRegistry) Get(name string) DownloadClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clients[name]
}

// InitAll instantiates DownloadClients from config. Config keys are
// matched against factory names. Returns the first error encountered.
func (r *DownloadClientRegistry) InitAll(sources map[string]json.RawMessage, resources plugin.PluginResources) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, rawCfg := range sources {
		for _, df := range r.factories {
			if df.factory.Name() != key {
				continue
			}
			base, err := df.factory.Create(rawCfg, resources)
			if err != nil {
				return fmt.Errorf("download client %q init: %w", key, err)
			}
			dc, ok := base.(DownloadClient)
			if !ok {
				return fmt.Errorf("download client factory %q does not produce DownloadClient", key)
			}
			r.clients[key] = dc
			break
		}
	}
	return nil
}
