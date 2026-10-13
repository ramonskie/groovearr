package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// stubResolver is a discovery.ReleaseResolver test double. The reconcile path
// only stores and re-injects it, so its resolve method is never called.
type stubResolver struct{ name string }

func (s *stubResolver) Name() string                          { return s.name }
func (s *stubResolver) DisplayName() string                   { return s.name }
func (s *stubResolver) IsConfigured() bool                    { return true }
func (s *stubResolver) CheckConnection(context.Context) error { return nil }
func (s *stubResolver) Connected() bool                       { return true }
func (s *stubResolver) CapabilityStatus() map[string]string   { return nil }
func (s *stubResolver) ResolveTracksForCount(context.Context, string, string, int, string) ([]discovery.TrackInfo, string, error) {
	return nil, "", nil
}

var _ discovery.ReleaseResolver = (*stubResolver)(nil)

// resolverConsumerPlugin is a plugin that needs an injected release resolver —
// the behavioural shape of a rebuilt indexer/album provider. Its factory never
// sets a resolver, so it starts nil, exactly like a real consumer plugin.
type resolverConsumerPlugin struct {
	name     string
	resolver discovery.ReleaseResolver
}

func (p *resolverConsumerPlugin) Name() string                          { return p.name }
func (p *resolverConsumerPlugin) DisplayName() string                   { return p.name }
func (p *resolverConsumerPlugin) IsConfigured() bool                    { return true }
func (p *resolverConsumerPlugin) CheckConnection(context.Context) error { return nil }
func (p *resolverConsumerPlugin) Connected() bool                       { return true }
func (p *resolverConsumerPlugin) CapabilityStatus() map[string]string   { return nil }
func (p *resolverConsumerPlugin) SetReleaseResolver(r discovery.ReleaseResolver) {
	p.resolver = r
}

var _ plugin.BasePlugin = (*resolverConsumerPlugin)(nil)
var _ discovery.ReleaseResolverConsumer = (*resolverConsumerPlugin)(nil)

// resolverConsumerFactory builds resolver-free consumer plugins and records
// every instance it makes, so a test can prove a rebuild produced a fresh
// (uninjected) instance.
type resolverConsumerFactory struct {
	name string
	made []*resolverConsumerPlugin
}

func (f *resolverConsumerFactory) Name() string                         { return f.name }
func (f *resolverConsumerFactory) DisplayName() string                  { return f.name }
func (f *resolverConsumerFactory) Capabilities() []string               { return []string{"download"} }
func (f *resolverConsumerFactory) ValidateConfig(json.RawMessage) error { return nil }
func (f *resolverConsumerFactory) DefaultConfig() json.RawMessage       { return json.RawMessage(`{}`) }
func (f *resolverConsumerFactory) Create(_ json.RawMessage, _ plugin.PluginResources) (plugin.BasePlugin, error) {
	p := &resolverConsumerPlugin{name: f.name}
	f.made = append(f.made, p)
	return p, nil
}

// TestReconcileAfterConfigUpdate_ReinjectsReleaseResolver is the regression guard
// for the rebuild re-injection defect: a config update rebuilds a changed plugin
// from its factory, and a rebuilt consumer plugin (e.g. an indexer) starts
// resolver-free because its factory cannot name a sibling provider (AGENTS §1).
// Before the fix it silently lost the resolver injected at start-up and its
// resolveTracks then failed. reconcileAfterConfigUpdate must re-inject the same
// shared resolver the app wiring uses.
func TestReconcileAfterConfigUpdate_ReinjectsReleaseResolver(t *testing.T) {
	// Arrange
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.Sources = map[string]json.RawMessage{"indexer": json.RawMessage(`{"v":"new"}`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry()
	factory := &resolverConsumerFactory{name: "indexer"}
	if err := reg.RegisterFactory(factory); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitAll(
		map[string]json.RawMessage{"indexer": json.RawMessage(`{"v":"old"}`)},
		plugin.PluginResources{Logger: log},
	); err != nil {
		t.Fatal(err)
	}
	// The start-up instance is resolver-free: its factory never names a sibling
	// provider (AGENTS §1), so the guard below would otherwise be vacuous.
	if initial := reg.Get("indexer").(*resolverConsumerPlugin); initial.resolver != nil {
		t.Fatal("factory-built plugin already had a resolver; the guard would be vacuous")
	}

	resolver := &stubResolver{name: "stub"}
	srv := &Server{
		log:        log,
		cfg:        cfg,
		registry:   download.NewRegistryFrom(reg),
		mdRegistry: metadata.NewRegistry(),
	}
	srv.SetReleaseResolver(resolver)

	// oldSources differs from the live config, so reconcile rebuilds "indexer".
	oldSources := map[string]json.RawMessage{"indexer": json.RawMessage(`{"v":"old"}`)}

	// Act
	srv.reconcileAfterConfigUpdate(oldSources)

	// Assert: reconcile built a fresh instance (so the rebuild really happened)
	// and re-injected the shared resolver into it.
	if len(factory.made) < 2 {
		t.Fatalf("factory built %d plugin instances, want the initial plus a rebuilt one", len(factory.made))
	}
	rebuilt, ok := reg.Get("indexer").(*resolverConsumerPlugin)
	if !ok {
		t.Fatal("rebuilt plugin is not the expected consumer type")
	}
	if rebuilt.resolver == nil {
		t.Fatal("rebuilt consumer plugin lost its release resolver (rebuild re-injection regressed)")
	}
	if rebuilt.resolver != resolver {
		t.Fatalf("rebuilt consumer resolver = %v, want the shared stub", rebuilt.resolver)
	}
}

// TestReconcileAfterConfigUpdate_ReResolvesReleaseResolver guards FIX B: a
// config-update rebuild of the resolver PROVIDER (MusicBrainz) builds a fresh
// client, so reconcile must re-resolve the resolver from the current registry
// and re-point consumers at the new instance rather than re-injecting the stale
// one frozen at start-up (which would leave two rate-limit pacers / stale
// credentials). Here the live hook is the only source of the fresh resolver.
func TestReconcileAfterConfigUpdate_ReResolvesReleaseResolver(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.Sources = map[string]json.RawMessage{
			"indexer": json.RawMessage(`{"v":"new"}`),
			"other":   json.RawMessage(`{"v":"same"}`),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry()
	indexerFactory := &resolverConsumerFactory{name: "indexer"}
	otherFactory := &resolverConsumerFactory{name: "other"}
	for _, f := range []*resolverConsumerFactory{indexerFactory, otherFactory} {
		if err := reg.RegisterFactory(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.InitAll(
		map[string]json.RawMessage{
			"indexer": json.RawMessage(`{"v":"old"}`),
			"other":   json.RawMessage(`{"v":"same"}`),
		},
		plugin.PluginResources{Logger: log},
	); err != nil {
		t.Fatal(err)
	}

	stale := &stubResolver{name: "stale"}
	fresh := &stubResolver{name: "fresh"}

	srv := &Server{
		log:        log,
		cfg:        cfg,
		registry:   download.NewRegistryFrom(reg),
		mdRegistry: metadata.NewRegistry(),
	}
	// Start-up wiring froze `stale`; the live hook is what re-resolves.
	srv.SetReleaseResolver(stale)
	resolveCalls := 0
	srv.SetReleaseResolverResolver(func() discovery.ReleaseResolver {
		resolveCalls++
		return fresh
	})

	// oldSources differs for "indexer" only, so only it is rebuilt; "other" is
	// left in place but must still be re-pointed at the fresh resolver.
	oldSources := map[string]json.RawMessage{
		"indexer": json.RawMessage(`{"v":"old"}`),
		"other":   json.RawMessage(`{"v":"same"}`),
	}

	// Act
	srv.reconcileAfterConfigUpdate(oldSources)

	// Assert: the hook ran, the rebuilt consumer got the FRESH resolver (not the
	// stale start-up one), and the non-rebuilt consumer was re-pointed too.
	if resolveCalls == 0 {
		t.Fatal("the live re-resolve hook was never called during reconcile")
	}
	rebuilt := reg.Get("indexer").(*resolverConsumerPlugin)
	if rebuilt.resolver != fresh {
		t.Fatalf("rebuilt consumer resolver = %v, want the freshly resolved one", rebuilt.resolver)
	}
	if rebuilt.resolver == stale {
		t.Fatal("rebuilt consumer kept the stale resolver (FIX B regressed)")
	}
	untouched := reg.Get("other").(*resolverConsumerPlugin)
	if untouched.resolver != fresh {
		t.Fatalf("non-rebuilt consumer resolver = %v, want the freshly resolved one", untouched.resolver)
	}
}

// TestReconcileAfterConfigUpdate_DropsResolverWhenGone guards the nil edge of
// FIX B: when the live hook reports no resolver (the resolver provider was
// disabled by the update), consumers must be CLEARED rather than left pointing
// at the stale start-up client.
func TestReconcileAfterConfigUpdate_DropsResolverWhenGone(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.Sources = map[string]json.RawMessage{"indexer": json.RawMessage(`{"v":"new"}`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry()
	if err := reg.RegisterFactory(&resolverConsumerFactory{name: "indexer"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitAll(
		map[string]json.RawMessage{"indexer": json.RawMessage(`{"v":"old"}`)},
		plugin.PluginResources{Logger: log},
	); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		log:        log,
		cfg:        cfg,
		registry:   download.NewRegistryFrom(reg),
		mdRegistry: metadata.NewRegistry(),
	}
	srv.SetReleaseResolver(&stubResolver{name: "stale"})
	srv.SetReleaseResolverResolver(func() discovery.ReleaseResolver { return nil })

	srv.reconcileAfterConfigUpdate(map[string]json.RawMessage{"indexer": json.RawMessage(`{"v":"old"}`)})

	rebuilt := reg.Get("indexer").(*resolverConsumerPlugin)
	if rebuilt.resolver != nil {
		t.Fatalf("consumer resolver = %v, want nil after the resolver provider was dropped", rebuilt.resolver)
	}
}
