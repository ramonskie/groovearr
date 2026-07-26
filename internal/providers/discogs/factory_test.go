package discogs

import (
	"encoding/json"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config should be valid (no credentials required for Discogs public API).
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
	}

	// Valid config with credentials.
	valid := json.RawMessage(`{"consumer_key":"key123","consumer_secret":"secret456"}`)
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
	var cfg DiscogsConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "discogs" {
		t.Errorf("name = %q, want discogs", Factory.Name())
	}
	if Factory.DisplayName() != "Discogs" {
		t.Errorf("display = %q, want Discogs", Factory.DisplayName())
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
	if hasDiscovery {
		t.Error("capabilities should not include 'discovery'")
	}
	if !hasMetadata {
		t.Error("capabilities missing 'metadata'")
	}
}
