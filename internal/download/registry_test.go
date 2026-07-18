package download

import (
	"context"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

type mockPlugin struct {
	name        string
	display     string
	configured  bool
	connected   bool
}

func (m *mockPlugin) Name() string                                  { return m.name }
func (m *mockPlugin) DisplayName() string                           { return m.display }
func (m *mockPlugin) IsConfigured() bool                            { return m.configured }
func (m *mockPlugin) Connected() bool                                { return m.connected }
func (m *mockPlugin) CheckConnection(ctx context.Context) error       { return nil }
func (m *mockPlugin) Search(ctx context.Context, q string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	return nil, nil, nil
}
func (m *mockPlugin) Download(ctx context.Context, u, f string, s int64) (string, error) { return "", nil }
func (m *mockPlugin) GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error)   { return nil, nil }
func (m *mockPlugin) GetDownloadStatus(ctx context.Context, id string) (*domain.DownloadRecord, error) {
	return nil, nil
}
func (m *mockPlugin) CancelDownload(ctx context.Context, id string, remove bool) error { return nil }
func (m *mockPlugin) ClearCompleted(ctx context.Context) error                          { return nil }

func TestRegistryRegister(t *testing.T) {
	r := NewRegistry()

	if err := r.Register(&mockPlugin{name: "soulseek", display: "Soulseek", configured: true}); err != nil {
		t.Fatal(err)
	}

	// Duplicate should fail.
	if err := r.Register(&mockPlugin{name: "soulseek", display: "Soulseek"}); err == nil {
		t.Error("expected error for duplicate plugin")
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockPlugin{name: "soulseek", display: "Soulseek"}, "slskd")

	if p := r.Get("soulseek"); p == nil {
		t.Error("Get by canonical name returned nil")
	}
	if p := r.Get("slskd"); p == nil {
		t.Error("Get by alias returned nil")
	}
	if p := r.Get("nonexistent"); p != nil {
		t.Error("Get nonexistent should return nil")
	}
}

func TestRegistryNames(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockPlugin{name: "a", display: "A"})
	r.Register(&mockPlugin{name: "b", display: "B"})

	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "a" || names[1] != "b" {
		t.Errorf("names out of order: %v", names)
	}
}

func TestRegistryAll(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockPlugin{name: "a", display: "A"})
	r.Register(&mockPlugin{name: "b", display: "B"})

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(all))
	}
}

func TestRegistryConfigured(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockPlugin{name: "a", display: "A", configured: true})
	r.Register(&mockPlugin{name: "b", display: "B", configured: false})

	cfg := r.Configured()
	if len(cfg) != 1 {
		t.Fatalf("expected 1 configured, got %d", len(cfg))
	}
	if cfg[0].Name() != "a" {
		t.Errorf("wrong configured plugin: %s", cfg[0].Name())
	}
}

func TestRegistryReplace(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockPlugin{name: "a", display: "A", configured: false})

	// Replace with configured version.
	if err := r.Replace("a", &mockPlugin{name: "a", display: "A", configured: true, connected: true}); err != nil {
		t.Fatal(err)
	}

	if p := r.Get("a"); !p.IsConfigured() {
		t.Error("replaced plugin should be configured")
	}
	if p := r.Get("a"); !p.Connected() {
		t.Error("replaced plugin should be connected")
	}

	// Replace nonexistent should fail.
	if err := r.Replace("nonexistent", &mockPlugin{name: "x"}); err == nil {
		t.Error("expected error replacing nonexistent plugin")
	}
}

func TestOrchestratorSearch(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockPlugin{name: "soulseek", display: "Soulseek", configured: true})
	orch := NewOrchestrator(r)

	_, _, err := orch.Search(context.Background(), "soulseek", "query")
	if err != nil {
		t.Errorf("search by source failed: %v", err)
	}

	_, _, err = orch.Search(context.Background(), "", "query")
	if err != nil {
		t.Errorf("search default failed: %v", err)
	}

	_, _, err = orch.Search(context.Background(), "hybrid", "query")
	if err != nil {
		t.Errorf("search hybrid failed: %v", err)
	}
}

func TestOrchestratorDownloadInvalidSource(t *testing.T) {
	r := NewRegistry()
	orch := NewOrchestrator(r)

	_, err := orch.Download(context.Background(), "nonexistent", "user", "file", 0)
	if err == nil {
		t.Error("expected error for nonexistent source")
	}
}
