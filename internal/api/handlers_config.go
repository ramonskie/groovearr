package api

import (
	"net/http"

	"github.com/ramonskie/groovearr/internal/plugin"
)

func (s *Server) handleGetSources(w http.ResponseWriter, r *http.Request) {
	inner := s.registry.Inner()
	var sources []map[string]any
	seen := make(map[string]bool)

	// Enumerate plugins grouped by capability. Order determines section order
	// in the settings UI. Plugins listing multiple capabilities appear once.
	for _, cap := range []string{"download", "download_client", "metadata", "discovery", "album_search"} {
		for _, p := range inner.WithCapability(cap) {
			if seen[p.Name()] {
				continue
			}
			seen[p.Name()] = true
			schema := resolveSchema(inner, p.Name())
			enabled := true
			if enabler, ok := p.(plugin.Enabler); ok {
				enabled = enabler.IsEnabled()
			}
			sources = append(sources, sourceEntry(p.Name(), p.DisplayName(), p.IsConfigured(), p.Connected(), enabled, p.CapabilityStatus(), schema))
		}
	}

	writeJSON(w, http.StatusOK, sources)
}

// resolveSchema looks up the factory for a source name in the plugin registry
// and returns its ConfigSchemaProvider if the factory implements that interface.
func resolveSchema(reg *plugin.Registry, name string) plugin.ConfigSchemaProvider {
	if reg == nil {
		return nil
	}
	f := reg.Factory(name)
	if f == nil {
		return nil
	}
	if sp, ok := f.(plugin.ConfigSchemaProvider); ok {
		return sp
	}
	return nil
}

func sourceEntry(name, displayName string, configured, connected, enabled bool, caps map[string]string, schema plugin.ConfigSchemaProvider) map[string]any {
	status := "not_configured"
	if configured {
		status = "configured"
		if connected {
			status = "connected"
		}
	}
	entry := map[string]any{
		"name":         name,
		"display_name": displayName,
		"configured":   configured,
		"enabled":      enabled,
		"status":       status,
	}
	if len(caps) > 0 {
		entry["capabilities"] = caps
	}
	if schema != nil {
		entry["icon"] = schema.Icon()
		if fields := schema.ConfigSchema(); fields != nil {
			entry["config_schema"] = fields
		}
		if oa := schema.OAuthConfig(); oa != nil {
			entry["oauth"] = oa
		}
		if slots := schema.UISlots(); slots != nil {
			entry["ui_slots"] = slots
		}
	}
	return entry
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")

	// Check all registries.
	var p plugin.BasePlugin
	if dp := s.registry.Get(source); dp != nil {
		p = dp
	} else if mp := s.mdRegistry.Get(source); mp != nil {
		p = mp
	} else if s.discoveryReg != nil {
		for _, dp := range s.discoveryReg.Any() {
			if dp.Name() == source {
				p = dp
				break
			}
		}
	}
	// Fallback: inner plugin registry for capabilities without typed wrappers
	// (album_search, etc.). Type-asserted registries above are preferred paths.
	if p == nil {
		p = s.registry.Inner().Get(source)
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "source not found"})
		return
	}
	if !p.IsConfigured() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source not configured", "status": "not_configured"})
		return
	}

	err := p.CheckConnection(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "configured", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "connected"})
}
