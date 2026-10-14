package discovery

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// fakeProvider is a minimal discovery.Provider with a fixed name/configured
// state; it is never called by these order tests.
type fakeProvider struct {
	name       string
	configured bool
}

var _ Provider = (*fakeProvider)(nil)

func (p *fakeProvider) Name() string                          { return p.name }
func (p *fakeProvider) DisplayName() string                   { return p.name }
func (p *fakeProvider) IsConfigured() bool                    { return p.configured }
func (p *fakeProvider) CheckConnection(context.Context) error { return nil }
func (p *fakeProvider) Connected() bool                       { return true }
func (p *fakeProvider) CapabilityStatus() map[string]string   { return nil }
func (p *fakeProvider) SearchArtists(context.Context, string, int) ([]ArtistSummary, error) {
	return nil, nil
}
func (p *fakeProvider) GetArtistAlbums(context.Context, string, int) ([]AlbumResult, error) {
	return nil, nil
}
func (p *fakeProvider) GetAlbumTracks(context.Context, string) ([]TrackInfo, error) {
	return nil, nil
}
func (p *fakeProvider) SearchAlbums(context.Context, AlbumQuery, int) ([]AlbumResult, error) {
	return nil, nil
}

// fakeFactory declares the discovery capability so WithCapability includes the
// provider; the concrete instance is registered directly for these tests.
type fakeFactory struct{ name string }

var _ plugin.PluginFactory = (*fakeFactory)(nil)

func (f *fakeFactory) Name() string                         { return f.name }
func (f *fakeFactory) DisplayName() string                  { return f.name }
func (f *fakeFactory) Capabilities() []string               { return []string{"discovery"} }
func (f *fakeFactory) ValidateConfig(json.RawMessage) error { return nil }
func (f *fakeFactory) DefaultConfig() json.RawMessage       { return nil }
func (f *fakeFactory) Create(json.RawMessage, plugin.PluginResources) (plugin.BasePlugin, error) {
	return &fakeProvider{name: f.name, configured: true}, nil
}

// registryWith builds a discovery registry over the given provider names in
// registration order, all configured.
func registryWith(names ...string) *Registry {
	inner := plugin.NewRegistry()
	for _, name := range names {
		_ = inner.RegisterFactory(&fakeFactory{name: name})
		_ = inner.Register(&fakeProvider{name: name, configured: true})
	}
	return NewRegistry(inner)
}

func providerNames(ps []Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}

func TestRegistryConfiguredHonorsProviderOrder(t *testing.T) {
	reg := registryWith("alpha", "beta", "gamma")

	// No order wired: registration order is preserved (unchanged behavior).
	if got := providerNames(reg.Configured()); !slices.Equal(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("Configured() without order = %v, want registration order", got)
	}

	// With the shared order wired, listed providers come first in that order
	// and unlisted providers follow in registration order.
	reg.SetProviderOrder(metadata.NewProviderOrder(func() []string {
		return []string{"gamma", "alpha"}
	}))
	if got := providerNames(reg.Configured()); !slices.Equal(got, []string{"gamma", "alpha", "beta"}) {
		t.Errorf("Configured() with order = %v, want [gamma alpha beta]", got)
	}
}

func TestRegistryAnyHonorsProviderOrder(t *testing.T) {
	inner := plugin.NewRegistry()
	for _, name := range []string{"alpha", "beta"} {
		_ = inner.RegisterFactory(&fakeFactory{name: name})
	}
	_ = inner.Register(&fakeProvider{name: "alpha", configured: true})
	// beta is unconfigured but still discovery-capable: Any includes it.
	_ = inner.Register(&fakeProvider{name: "beta", configured: false})
	reg := NewRegistry(inner)

	if got := providerNames(reg.Any()); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("Any() without order = %v, want registration order", got)
	}

	reg.SetProviderOrder(metadata.NewProviderOrder(func() []string { return []string{"beta"} }))
	if got := providerNames(reg.Any()); !slices.Equal(got, []string{"beta", "alpha"}) {
		t.Errorf("Any() with order = %v, want [beta alpha]", got)
	}
}

// TestRegistrySetProviderOrderConcurrent guards FIX D: SetProviderOrder used to
// write r.order with no synchronization while Configured/Any read it on every
// request. Run under `go test -race`; the mutex must make this clean.
func TestRegistrySetProviderOrderConcurrent(t *testing.T) {
	reg := registryWith("alpha", "beta", "gamma")

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
				reg.SetProviderOrder(metadata.NewProviderOrder(func() []string {
					return []string{"gamma", "beta", "alpha"}
				}))
			}
		}
	}()

	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 2000; j++ {
				_ = reg.Configured()
				_ = reg.Any()
			}
		}()
	}
	readers.Wait()
	close(stop)
	writer.Wait()
}
