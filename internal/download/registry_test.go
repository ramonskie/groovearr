package download

import (
	"context"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
)

type mockPlugin struct {
	name          string
	display       string
	configured    bool
	connected     bool
	searchResults []domain.TrackResult
}

func (m *mockPlugin) Name() string                                  { return m.name }
func (m *mockPlugin) DisplayName() string                           { return m.display }
func (m *mockPlugin) IsConfigured() bool                            { return m.configured }
func (m *mockPlugin) Connected() bool                                { return m.connected }
func (m *mockPlugin) CheckConnection(ctx context.Context) error       { return nil }
func (m *mockPlugin) Search(ctx context.Context, q string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if m.searchResults != nil {
		return m.searchResults, nil, nil
	}
	return nil, nil, nil
}
func (m *mockPlugin) Download(ctx context.Context, u, f string, s int64) (string, error) { return "mock-dl-1", nil }
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
	orch := NewOrchestrator(r, nil)

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
	orch := NewOrchestrator(r, nil)

	_, err := orch.Download(context.Background(), "nonexistent", "user", "file", 0)
	if err == nil {
		t.Error("expected error for nonexistent source")
	}
}

func TestFilterByQuality(t *testing.T) {
	makeCand := func(quality string, bitrate int) candidate {
		return candidate{
			track:      domain.TrackResult{SearchResult: domain.SearchResult{Quality: quality, Bitrate: bitrate}},
			sourceName: "test",
			score:      0.8,
		}
	}

	t.Run("preferred format flac filters out mp3", func(t *testing.T) {
		candidates := []candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 320),
		}
		qc := config.QualityConfig{PreferredFormat: "flac"}
		result := filterByQuality(candidates, qc)
		if len(result) != 1 || result[0].track.Quality != "flac" {
			t.Errorf("expected 1 flac candidate, got %d", len(result))
		}
	})

	t.Run("preferred format mp3 accepts mp3_320", func(t *testing.T) {
		candidates := []candidate{
			makeCand("flac", 1411),
			makeCand("mp3_320", 320),
		}
		qc := config.QualityConfig{PreferredFormat: "mp3"}
		result := filterByQuality(candidates, qc)
		if len(result) != 1 || result[0].track.Quality != "mp3_320" {
			t.Errorf("expected 1 mp3_320 candidate, got %d", len(result))
		}
	})

	t.Run("min bitrate filters low quality", func(t *testing.T) {
		candidates := []candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 128),
		}
		qc := config.QualityConfig{MinBitrate: 320}
		result := filterByQuality(candidates, qc)
		if len(result) != 1 || result[0].track.Quality != "flac" {
			t.Errorf("expected 1 candidate >= 320kbps, got %d", len(result))
		}
	})

	t.Run("combined format + bitrate filter", func(t *testing.T) {
		candidates := []candidate{
			makeCand("flac", 800),           // wrong format
			makeCand("mp3_320", 320),         // right format, good bitrate
			makeCand("mp3_128", 128),         // right format, low bitrate
			makeCand("flac", 1411),           // wrong format
		}
		qc := config.QualityConfig{PreferredFormat: "mp3", MinBitrate: 300}
		result := filterByQuality(candidates, qc)
		if len(result) != 1 || result[0].track.Bitrate != 320 {
			t.Errorf("expected 1 mp3_320 candidate, got %d", len(result))
		}
	})

	t.Run("empty config is no-op", func(t *testing.T) {
		candidates := []candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 320),
		}
		result := filterByQuality(candidates, config.QualityConfig{})
		if len(result) != 2 {
			t.Errorf("expected all candidates to pass, got %d", len(result))
		}
	})

	t.Run("preferred any is no-op", func(t *testing.T) {
		candidates := []candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 320),
			makeCand("ogg", 192),
		}
		qc := config.QualityConfig{PreferredFormat: "any"}
		result := filterByQuality(candidates, qc)
		if len(result) != 3 {
			t.Errorf("expected all candidates with any format, got %d", len(result))
		}
	})

	t.Run("mp3 matches mp3_128", func(t *testing.T) {
		candidates := []candidate{
			makeCand("mp3_128", 128),
		}
		qc := config.QualityConfig{PreferredFormat: "mp3"}
		result := filterByQuality(candidates, qc)
		if len(result) != 1 {
			t.Errorf("expected mp3 to match mp3_128, got %d", len(result))
		}
	})
}

func TestDownloadBestQualityFilter(t *testing.T) {
	r := NewRegistry()

	// Plugin that returns results with mixed quality.
	searchResults := []domain.TrackResult{
		{
			SearchResult: domain.SearchResult{
				Username: "test_user", Filename: "low_quality.mp3",
				Size: 1000, Bitrate: 128, Quality: "mp3_128",
				Duration: 200000,
			},
			Artist: "Test Artist", Title: "Test Title",
		},
		{
			SearchResult: domain.SearchResult{
				Username: "test_user", Filename: "high_quality.flac",
				Size: 5000, Bitrate: 1411, Quality: "flac",
				Duration: 200000,
			},
			Artist: "Test Artist", Title: "Test Title",
		},
	}
	r.Register(&mockPlugin{name: "test", display: "Test", configured: true, searchResults: searchResults})

	t.Run("filters out low bitrate candidates", func(t *testing.T) {
		orch := NewOrchestrator(r, func() config.QualityConfig {
			return config.QualityConfig{PreferredFormat: "any", MinBitrate: 500}
		})
		_, _, _, err := orch.DownloadBest(context.Background(), "Test Title", "Test Artist", 200000, "")
		if err != nil {
			t.Fatalf("DownloadBest failed: %v", err)
		}
		// Should have picked the flac (1411kbps) over mp3_128 (128kbps).
	})

	t.Run("returns error when no candidates match quality", func(t *testing.T) {
		orch := NewOrchestrator(r, func() config.QualityConfig {
			return config.QualityConfig{PreferredFormat: "flac", MinBitrate: 2000}
		})
		_, _, _, err := orch.DownloadBest(context.Background(), "Test Title", "Test Artist", 200000, "")
		if err == nil {
			t.Fatal("expected error when no candidates match quality")
		}
	})
}
