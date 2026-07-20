package soulseek

import (
	"encoding/json"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config should be valid (plugin starts unconfigured).
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
	}

	// Valid minimal config.
	valid := json.RawMessage(`{"slskd_url":"http://localhost:5030","api_key":"test","search_timeout":60}`)
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
	var cfg SoulseekConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "soulseek" {
		t.Errorf("name = %q, want soulseek", Factory.Name())
	}
	if Factory.DisplayName() != "Soulseek" {
		t.Errorf("display = %q, want Soulseek", Factory.DisplayName())
	}
	caps := Factory.Capabilities()
	if len(caps) != 1 || caps[0] != "download" {
		t.Errorf("capabilities = %v, want [download]", caps)
	}
}
