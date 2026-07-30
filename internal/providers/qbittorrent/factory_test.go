package qbittorrent

import (
	"encoding/json"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		wantErr bool
	}{
		{
			name:    "valid config",
		cfg:     `{"url":"http://localhost:8080","api_key":"test-key","category":"music"}`,
		wantErr: false,
	},
	{
		name:    "empty url",
		cfg:     `{"url":"","api_key":"test-key"}`,
		wantErr: true,
	},
	{
		name:    "missing url field",
		cfg:     `{"api_key":"test-key"}`,
			wantErr: true,
		},
		{
			name:    "default config is valid (empty url — validated by plugin creation not factory)",
			cfg:     string(Factory.DefaultConfig()),
			wantErr: true, // DefaultConfig has empty URL.
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
	if Factory.Name() != "qbittorrent" {
		t.Errorf("Name() = %q, want %q", Factory.Name(), "qbittorrent")
	}
	if Factory.DisplayName() != "qBittorrent" {
		t.Errorf("DisplayName() = %q, want %q", Factory.DisplayName(), "qBittorrent")
	}
	caps := Factory.Capabilities()
	if len(caps) != 1 || caps[0] != "download_client" {
		t.Errorf("Capabilities() = %v, want [download_client]", caps)
	}
	// Icon is on ConfigSchemaProvider, not PluginFactory.
	var csp interface{ Icon() string } = &factory{}
	if icon := csp.Icon(); icon != "disc" {
		t.Errorf("Icon() = %q, want %q", icon, "disc")
	}
}

func TestPluginBasics(t *testing.T) {
	p, err := 	newPlugin(Config{
		URL:      "http://localhost:8080",
		APIKey: "test-api-key",
	}, "", nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}

	if p.Name() != "qbittorrent" {
		t.Errorf("Name() = %q, want %q", p.Name(), "qbittorrent")
	}
	if p.DisplayName() != "qBittorrent" {
		t.Errorf("DisplayName() = %q, want %q", p.DisplayName(), "qBittorrent")
	}
	if !p.IsConfigured() {
		t.Error("IsConfigured() should be true with URL + API key")
	}
	if p.Connected() {
		t.Error("Connected() should be false before CheckConnection")
	}
	if p.MaxConcurrent() != 5 {
		t.Errorf("MaxConcurrent() = %d, want 5", p.MaxConcurrent())
	}
}

func TestPluginNotConfigured(t *testing.T) {
	p, err := newPlugin(Config{}, "", nil)
	if err == nil {
		t.Fatal("newPlugin with empty URL should fail")
	}

	// Manual construction for IsConfigured test.
	p = &Plugin{cfg: Config{}}
	if p.IsConfigured() {
		t.Error("IsConfigured() should be false with empty config")
	}
}

func TestMapState(t *testing.T) {
	tests := []struct {
		state  string
		amount int64
		compOn int64
		want   string
	}{
		{"downloading", 100, 0, "downloading"},
		{"forcedDL", 50, 0, "downloading"},
		{"stalledDL", 200, 0, "downloading"},
		{"metaDL", 500, 0, "downloading"},
		{"error", 0, 0, "failed"},
		{"missingFiles", 0, 0, "failed"},
		{"uploading", 0, 12345, "importPending"},
		{"stalledUP", 0, 12345, "importPending"},
	}

	for _, tt := range tests {
		info := &torrentInfo{
			State:        tt.state,
			AmountLeft:   tt.amount,
			CompletionOn: tt.compOn,
		}
		got := mapState(info)
		if string(got) != tt.want {
			t.Errorf("mapState(%s, amountLeft=%d, completionOn=%d) = %s, want %s",
				tt.state, tt.amount, tt.compOn, got, tt.want)
		}
	}
}
