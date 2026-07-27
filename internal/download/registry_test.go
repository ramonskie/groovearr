package download

import (
	"context"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/quality"
)

type mockPlugin struct {
	name          string
	display       string
	configured    bool
	connected     bool
	searchResults []domain.TrackResult
}

func (m *mockPlugin) Name() string        { return m.name }
func (m *mockPlugin) DisplayName() string { return m.display }
func (m *mockPlugin) IsConfigured() bool  { return m.configured }
func (m *mockPlugin) Connected() bool     { return m.connected }
func (m *mockPlugin) CapabilityStatus() map[string]string {
	s := "not_configured"
	if m.configured {
		s = "configured"
	}
	if m.connected {
		s = "connected"
	}
	return map[string]string{"download": s}
}
func (m *mockPlugin) CheckConnection(ctx context.Context) error { return nil }
func (m *mockPlugin) Search(ctx context.Context, q string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if m.searchResults != nil {
		return m.searchResults, nil, nil
	}
	return nil, nil, nil
}

// MonitoredProvider stubs for retry/search tests.
func (m *mockPlugin) StartDownload(_ context.Context, _ Meta) (string, error)    { return "", nil }
func (m *mockPlugin) GetStatus(_ context.Context, _ string) (*Record, error)     { return nil, nil }
func (m *mockPlugin) GetProgress(_ context.Context, _ string) (*Progress, error) { return nil, nil }
func (m *mockPlugin) Cancel(_ context.Context, _ string, _ bool) error           { return nil }
func (m *mockPlugin) ActiveDownloads() []string                                  { return nil }
func (m *mockPlugin) MaxConcurrent() int                                         { return 0 }
func (m *mockPlugin) DownloadTimeout() time.Duration                             { return 0 }

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
	r.Register(&mockPlugin{name: "soulseek", display: "Soulseek"})

	if p := r.Get("soulseek"); p == nil {
		t.Error("Get by canonical name returned nil")
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
	r.Register(&mockPlugin{name: "a", display: "A", configured: true, connected: true})
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
	r.Register(&mockPlugin{name: "soulseek", display: "Soulseek", configured: true, connected: true})
	orch := NewOrchestrator(r, testLogger())

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

func TestFilterByProfile(t *testing.T) {
	makeCand := func(format string, bitrate int) Candidate {
		return Candidate{
			Track: domain.TrackResult{
				SearchResult: domain.SearchResult{
					AudioQuality: quality.AudioQuality{Format: format, Bitrate: bitrate},
				},
			},
			SourceName: "test",
			Score:      0.8,
		}
	}

	t.Run("single target flac filters out mp3", func(t *testing.T) {
		candidates := []Candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 320),
		}
		profile := &quality.QualityProfile{
			Name: "Lossless",
			RankedTargets: quality.RankedTargets{
				{Label: "FLAC", Format: "flac"},
			},
		}
		result := FilterByProfile(candidates, profile)
		if len(result) != 1 || result[0].Track.AudioQuality.Format != "flac" {
			t.Errorf("expected 1 flac candidate, got %d", len(result))
		}
	})

	t.Run("min bitrate filters low quality", func(t *testing.T) {
		candidates := []Candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 128),
		}
		profile := &quality.QualityProfile{
			Name: "High Quality",
			RankedTargets: quality.RankedTargets{
				{Label: "320kbps+", MinBitrate: 320},
			},
		}
		result := FilterByProfile(candidates, profile)
		if len(result) != 1 || result[0].Track.AudioQuality.Format != "flac" {
			t.Errorf("expected 1 candidate >= 320kbps, got %d", len(result))
		}
	})

	t.Run("ranked targets pick best tier", func(t *testing.T) {
		candidates := []Candidate{
			makeCand("flac", 1411), // matches tier 0
			makeCand("mp3", 320),   // matches tier 1
			makeCand("mp3", 128),   // matches tier 2
		}
		profile := &quality.QualityProfile{
			Name: "Ranked",
			RankedTargets: quality.RankedTargets{
				{Label: "FLAC", Format: "flac"},
				{Label: "MP3-320", Format: "mp3", MinBitrate: 320},
				{Label: "MP3-128", Format: "mp3"},
			},
		}
		result := FilterByProfile(candidates, profile)
		// Best tier (FLAC) should be the only result.
		if len(result) != 1 || result[0].Track.AudioQuality.Format != "flac" {
			t.Errorf("expected 1 flac candidate (best tier), got %d", len(result))
		}
	})

	t.Run("fallback enabled returns lower tier", func(t *testing.T) {
		candidates := []Candidate{
			makeCand("mp3", 320),
			makeCand("mp3", 128),
		}
		profile := &quality.QualityProfile{
			Name:            "FLAC preferred",
			FallbackEnabled: true,
			RankedTargets: quality.RankedTargets{
				{Label: "FLAC", Format: "flac"},
			},
		}
		result := FilterByProfile(candidates, profile)
		// No FLAC matches but fallback enabled — returns all by tier score.
		if len(result) != 2 {
			t.Errorf("expected 2 candidates with fallback, got %d", len(result))
		}
	})

	t.Run("nil profile returns all", func(t *testing.T) {
		candidates := []Candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 320),
		}
		result := FilterByProfile(candidates, nil)
		if len(result) != 2 {
			t.Errorf("expected all candidates with nil profile, got %d", len(result))
		}
	})

	t.Run("empty targets returns all", func(t *testing.T) {
		candidates := []Candidate{
			makeCand("flac", 1411),
			makeCand("mp3", 320),
		}
		profile := &quality.QualityProfile{Name: "Empty"}
		result := FilterByProfile(candidates, profile)
		if len(result) != 2 {
			t.Errorf("expected all candidates with empty targets, got %d", len(result))
		}
	})
}

// ─── pickBest helpers ──────────────────────────────────────────────

func makeQualCand(format string, bitrate int, score float64) Candidate {
	return Candidate{
		Track: domain.TrackResult{
			SearchResult: domain.SearchResult{
				AudioQuality: quality.AudioQuality{Format: format, Bitrate: bitrate},
			},
		},
		SourceName: "test",
		Score:      score,
	}
}

func TestPickBestByScore(t *testing.T) {
	candidates := []Candidate{
		makeQualCand("mp3", 128, 0.7),
		makeQualCand("mp3", 320, 0.6),
		makeQualCand("mp3", 256, 0.9),
	}
	best := pickBestByScore(candidates)
	if best.Score != 0.9 {
		t.Errorf("expected best score 0.9, got %.1f", best.Score)
	}
}

func TestPickBestByTierScore(t *testing.T) {
	candidates := []Candidate{
		makeQualCand("mp3", 128, 0.9),
		makeQualCand("mp3", 320, 0.6),
		makeQualCand("mp3", 256, 0.7),
	}
	best := pickBestByTierScore(candidates)
	if best.Track.AudioQuality.Bitrate != 320 {
		t.Errorf("expected best tier MP3 320, got bitrate %d", best.Track.AudioQuality.Bitrate)
	}
}

func TestRankAllByQuality(t *testing.T) {
	candidates := []Candidate{
		makeQualCand("mp3", 128, 0.9),
		makeQualCand("flac", 1411, 0.6),
		makeQualCand("mp3", 320, 0.8),
	}
	profile := &quality.QualityProfile{
		Name: "Test",
		RankedTargets: quality.RankedTargets{
			{Label: "FLAC", Format: "flac"},
			{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
		},
		FallbackEnabled: true,
		SearchMode:      quality.SearchBestQuality,
	}
	result := rankAllByQuality(candidates, profile)
	if len(result) != 3 {
		t.Fatalf("expected 3 candidates with best_quality, got %d", len(result))
	}
	if result[0].Track.AudioQuality.Format != "flac" {
		t.Errorf("expected FLAC first with best_quality, got %s", result[0].Track.AudioQuality.Format)
	}
}
