package coverartarchive

import (
	"encoding/json"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config — no credentials required.
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
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
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "coverartarchive" {
		t.Errorf("name = %q, want coverartarchive", Factory.Name())
	}
	if Factory.DisplayName() != "Cover Art Archive" {
		t.Errorf("display = %q, want Cover Art Archive", Factory.DisplayName())
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
