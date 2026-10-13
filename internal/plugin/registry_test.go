package plugin

import (
	"encoding/json"
	"reflect"
	"testing"
)

// stubFactory is a minimal PluginFactory for registry tests. Capabilities() is
// the only method WithCapability consults; the rest satisfy the interface.
type stubFactory struct {
	name         string
	capabilities []string
}

func (f *stubFactory) Name() string           { return f.name }
func (f *stubFactory) DisplayName() string    { return f.name }
func (f *stubFactory) Capabilities() []string { return f.capabilities }

func (f *stubFactory) Create(json.RawMessage, PluginResources) (BasePlugin, error) {
	return newStub(f.name, true, false), nil
}

func (f *stubFactory) ValidateConfig(json.RawMessage) error { return nil }
func (f *stubFactory) DefaultConfig() json.RawMessage       { return json.RawMessage("{}") }

// namesOf projects a plugin slice to names for order assertions.
func namesOf(plugins []BasePlugin) []string {
	out := make([]string, len(plugins))
	for i, p := range plugins {
		out[i] = p.Name()
	}
	return out
}

// TestRegistryWithCapabilityDeterministicOrder pins the WithCapability contract:
// results follow plugin registration order (r.names), not Go map iteration order.
// It calls the method many times per case — the old map-range implementation
// would flake here because Go randomizes map iteration.
func TestRegistryWithCapabilityDeterministicOrder(t *testing.T) {
	tests := []struct {
		name       string
		order      []string
		caps       map[string][]string
		capability string
		want       []string
	}{
		{
			name:  "three discovery providers keep registration order",
			order: []string{"alpha", "bravo", "charlie"},
			caps: map[string][]string{
				"alpha":   {"discovery"},
				"bravo":   {"discovery", "metadata"},
				"charlie": {"discovery"},
			},
			capability: "discovery",
			want:       []string{"alpha", "bravo", "charlie"},
		},
		{
			name:  "capability filter preserves registration order",
			order: []string{"delta", "echo", "foxtrot"},
			caps: map[string][]string{
				"delta":   {"discovery"},
				"echo":    {"metadata"},
				"foxtrot": {"discovery"},
			},
			capability: "discovery",
			want:       []string{"delta", "foxtrot"},
		},
		{
			name:  "single-capability group returns every plugin in order",
			order: []string{"golf", "hotel", "india"},
			caps: map[string][]string{
				"golf":  {"discovery"},
				"hotel": {"discovery"},
				"india": {"discovery"},
			},
			capability: "discovery",
			want:       []string{"golf", "hotel", "india"},
		},
		{
			name:  "no matching capability yields empty result",
			order: []string{"juliet", "kilo", "lima"},
			caps: map[string][]string{
				"juliet": {"metadata"},
				"kilo":   {"metadata"},
				"lima":   {"metadata"},
			},
			capability: "discovery",
			want:       []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := NewRegistry()
			for _, name := range tt.order {
				if err := reg.RegisterFactory(&stubFactory{name: name, capabilities: tt.caps[name]}); err != nil {
					t.Fatalf("RegisterFactory(%q): %v", name, err)
				}
				if err := reg.Register(newStub(name, true, false)); err != nil {
					t.Fatalf("Register(%q): %v", name, err)
				}
			}

			// Repeated identical calls must be identical; map iteration would flake.
			for i := 0; i < 100; i++ {
				got := namesOf(reg.WithCapability(tt.capability))
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("call %d: WithCapability(%q) = %v, want %v",
						i, tt.capability, got, tt.want)
				}
			}
		})
	}
}

// TestRegistryInitOrderDeterministic pins that InitAll and InitRemaining
// iterate factory registration order (not the sources/factories map), so
// r.names — and therefore Names(), WithCapability(), All() and Configured() —
// is stable across repeated construction (i.e. across restarts). A map-range
// implementation would flake here.
func TestRegistryInitOrderDeterministic(t *testing.T) {
	order := []string{"alpha", "bravo", "charlie", "delta"}

	// sources covers every factory; its own iteration order is irrelevant.
	sources := make(map[string]json.RawMessage, len(order))
	for _, name := range order {
		sources[name] = json.RawMessage("{}")
	}

	build := func(t *testing.T) *Registry {
		t.Helper()
		reg := NewRegistry()
		for _, name := range order {
			if err := reg.RegisterFactory(&stubFactory{name: name, capabilities: []string{"discovery"}}); err != nil {
				t.Fatalf("RegisterFactory(%q): %v", name, err)
			}
		}
		return reg
	}

	t.Run("InitAll follows factory registration order", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			reg := build(t)
			if err := reg.InitAll(sources, PluginResources{}); err != nil {
				t.Fatalf("iteration %d: InitAll: %v", i, err)
			}
			if got := reg.Names(); !reflect.DeepEqual(got, order) {
				t.Fatalf("iteration %d: Names() = %v, want %v", i, got, order)
			}
			if got := namesOf(reg.WithCapability("discovery")); !reflect.DeepEqual(got, order) {
				t.Fatalf("iteration %d: WithCapability(discovery) = %v, want %v", i, got, order)
			}
		}
	})

	t.Run("InitRemaining follows factory registration order", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			reg := build(t)
			reg.InitRemaining(PluginResources{})
			if got := reg.Names(); !reflect.DeepEqual(got, order) {
				t.Fatalf("iteration %d: Names() = %v, want %v", i, got, order)
			}
			if got := namesOf(reg.WithCapability("discovery")); !reflect.DeepEqual(got, order) {
				t.Fatalf("iteration %d: WithCapability(discovery) = %v, want %v", i, got, order)
			}
		}
	})

	t.Run("InitAll subset then InitRemaining is stable", func(t *testing.T) {
		// Real flow: InitAll seeds the configured subset, InitRemaining fills
		// the rest. Seeded providers come first (in factory order), then the
		// unseeded ones (in factory order) — deterministic across boots.
		partial := map[string]json.RawMessage{
			"charlie": json.RawMessage("{}"),
			"alpha":   json.RawMessage("{}"),
		}
		want := []string{"alpha", "charlie", "bravo", "delta"}
		for i := 0; i < 50; i++ {
			reg := build(t)
			if err := reg.InitAll(partial, PluginResources{}); err != nil {
				t.Fatalf("iteration %d: InitAll: %v", i, err)
			}
			reg.InitRemaining(PluginResources{})
			if got := reg.Names(); !reflect.DeepEqual(got, want) {
				t.Fatalf("iteration %d: Names() = %v, want %v", i, got, want)
			}
		}
	})
}

// TestRegistryWithCapabilityRequiresPluginAndFactory pins the "factory declares
// the capability AND plugin exists" semantics: a factory with no instantiated
// plugin, or a plugin with no factory, is never returned.
func TestRegistryWithCapabilityRequiresPluginAndFactory(t *testing.T) {
	reg := NewRegistry()

	// Factory registered, capability declared, but no plugin instantiated.
	if err := reg.RegisterFactory(&stubFactory{name: "factory-only", capabilities: []string{"discovery"}}); err != nil {
		t.Fatalf("RegisterFactory(factory-only): %v", err)
	}
	// Plugin registered, but no factory to declare the capability.
	if err := reg.Register(newStub("plugin-only", true, false)); err != nil {
		t.Fatalf("Register(plugin-only): %v", err)
	}
	// Fully wired plugin: factory + plugin + capability.
	if err := reg.RegisterFactory(&stubFactory{name: "wired", capabilities: []string{"discovery"}}); err != nil {
		t.Fatalf("RegisterFactory(wired): %v", err)
	}
	if err := reg.Register(newStub("wired", true, false)); err != nil {
		t.Fatalf("Register(wired): %v", err)
	}

	got := namesOf(reg.WithCapability("discovery"))
	want := []string{"wired"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithCapability(discovery) = %v, want %v", got, want)
	}
}
