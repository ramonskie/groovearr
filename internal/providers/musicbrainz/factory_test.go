package musicbrainz

import (
	"encoding/json"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config should be valid (no credentials required for MusicBrainz).
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
	}

	// Valid config with email.
	valid := json.RawMessage(`{"email":"user@example.com"}`)
	if err := Factory.ValidateConfig(valid); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	// Invalid JSON.
	if err := Factory.ValidateConfig(json.RawMessage(`{bad`)); err == nil {
		t.Error("invalid JSON should return error")
	}
}

func TestFactoryDefaultConfig(t *testing.T) {
	def := Factory.DefaultConfig()
	if !json.Valid(def) {
		t.Error("default config is not valid JSON")
	}
	var cfg MusicBrainzConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "musicbrainz" {
		t.Errorf("name = %q, want musicbrainz", Factory.Name())
	}
	if Factory.DisplayName() != "MusicBrainz" {
		t.Errorf("display = %q, want MusicBrainz", Factory.DisplayName())
	}
	caps := Factory.Capabilities()
	hasMetadata := false
	for _, c := range caps {
		if c == "metadata" {
			hasMetadata = true
		}
	}
	if !hasMetadata {
		t.Error("capabilities missing 'metadata'")
	}
}

func TestEscapeLucene(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"AC/DC", `AC\/DC`},
		{"hello", "hello"},
		{`"quoted"`, `\"quoted\"`},
		{"artist (feat. other)", `artist \(feat. other\)`},
		{"test?", `test\?`},
		{"test*", `test\*`},
	}
	for _, tt := range tests {
		got := escapeLucene(tt.input)
		if got != tt.expected {
			t.Errorf("escapeLucene(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
