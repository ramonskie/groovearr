package prowlarr

import (
	"encoding/json"
	"testing"

	"github.com/ramonskie/groovearr/internal/providers/musicbrainz"
)

func TestFactoryValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     `{"url":"http://localhost:9696","api_key":"abc123","indexer_tag":"groovearr"}`,
			wantErr: false,
		},
		{
			name:    "empty url",
			cfg:     `{"url":"","api_key":"abc123"}`,
			wantErr: true,
		},
		{
			name:    "empty api_key",
			cfg:     `{"url":"http://localhost:9696","api_key":""}`,
			wantErr: true,
		},
		{
			name:    "missing fields",
			cfg:     `{}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Factory.ValidateConfig(json.RawMessage(tt.cfg))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestFactoryBasics(t *testing.T) {
	if Factory.Name() != "prowlarr" {
		t.Errorf("Name() = %q, want %q", Factory.Name(), "prowlarr")
	}
	if Factory.DisplayName() != "Prowlarr" {
		t.Errorf("DisplayName() = %q, want %q", Factory.DisplayName(), "Prowlarr")
	}
	caps := Factory.Capabilities()
	if len(caps) != 1 || caps[0] != "album_search" {
		t.Errorf("Capabilities() = %v, want [album_search]", caps)
	}
}

func testPlugin(t *testing.T) *Plugin {
	t.Helper()
	p, err := newPlugin(Config{
		URL:        "http://localhost:9696",
		APIKey:     "test-key",
		IndexerTag: "groovearr",
		Categories: []int{3040},
	}, musicbrainz.NewAPIClient(musicbrainz.MusicBrainzConfig{}, nil), nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	return p
}

func TestPluginBasics(t *testing.T) {
	p := testPlugin(t)

	if p.Name() != "prowlarr" {
		t.Errorf("Name() = %q, want %q", p.Name(), "prowlarr")
	}
	if p.DisplayName() != "Prowlarr" {
		t.Errorf("DisplayName() = %q, want %q", p.DisplayName(), "Prowlarr")
	}
	if !p.IsConfigured() {
		t.Error("IsConfigured() should be true with URL + API key")
	}
	if p.Connected() {
		t.Error("Connected() should be false before CheckConnection")
	}
}

func TestPluginNotConfigured(t *testing.T) {
	mb := musicbrainz.NewAPIClient(musicbrainz.MusicBrainzConfig{}, nil)

	// Plugin should create successfully but report not configured.
	p, err := newPlugin(Config{}, mb, nil)
	if err != nil {
		t.Fatalf("newPlugin with empty config should succeed: %v", err)
	}
	if p.IsConfigured() {
		t.Error("IsConfigured() should be false with empty config")
	}
}

func TestDefaultConfigApplied(t *testing.T) {
	cfg := Config{URL: "http://localhost:9696", APIKey: "key"}
	mb := musicbrainz.NewAPIClient(musicbrainz.MusicBrainzConfig{}, nil)
	p, err := newPlugin(cfg, mb, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	if p.cfg.IndexerTag != "groovearr" {
		t.Errorf("IndexerTag default = %q, want %q", p.cfg.IndexerTag, "groovearr")
	}
	if len(p.cfg.Categories) != 0 {
		t.Errorf("Categories default = %v, want []", p.cfg.Categories)
	}
}
