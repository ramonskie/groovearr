package download

import (
	"github.com/ramonskie/groovearr/internal/plugin"
)

// DownloadClientRegistry manages DownloadClient plugins and provides
// name-based lookup. Plugins are created by the main plugin.Registry
// (via pluginReg.InitAll); this registry simply casts them to DownloadClient
// on lookup.
type DownloadClientRegistry struct {
	pluginReg *plugin.Registry
}

// NewDownloadClientRegistry creates a DownloadClientRegistry backed by
// the main plugin registry. DownloadClient plugins must be registered
// via pluginReg.RegisterFactory() and instantiated via pluginReg.InitAll().
func NewDownloadClientRegistry(pluginReg *plugin.Registry) *DownloadClientRegistry {
	return &DownloadClientRegistry{pluginReg: pluginReg}
}

// Get returns a DownloadClient by name by looking up the plugin in the
// main registry and casting to DownloadClient. Returns nil if not found
// or if the plugin does not implement DownloadClient.
func (r *DownloadClientRegistry) Get(name string) DownloadClient {
	if r.pluginReg == nil {
		return nil
	}
	bp := r.pluginReg.Get(name)
	if dc, ok := bp.(DownloadClient); ok {
		return dc
	}
	return nil
}
