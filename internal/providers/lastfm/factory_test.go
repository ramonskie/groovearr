package lastfm

import (
	"encoding/json"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config should be valid (API key optional, plugin handles missing key).
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
	}

	// Valid config with API key.
	valid := json.RawMessage(`{"api_key":"abc123"}`)
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
	var cfg LastFMConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "lastfm" {
		t.Errorf("name = %q, want lastfm", Factory.Name())
	}
	if Factory.DisplayName() != "Last.fm" {
		t.Errorf("display = %q, want Last.fm", Factory.DisplayName())
	}
	caps := Factory.Capabilities()
	hasDiscovery := false
	hasMetadata := false
	for _, c := range caps {
		if c == "discovery" {
			hasDiscovery = true
		}
		if c == "metadata" {
			hasMetadata = true
		}
	}
	if !hasDiscovery {
		t.Error("capabilities missing 'discovery'")
	}
	if !hasMetadata {
		t.Error("capabilities missing 'metadata'")
	}
}
