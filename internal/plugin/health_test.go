package plugin

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
)

type stubPlugin struct {
	name          string
	configured    bool
	metaAvailable bool
	checkErr      error
	checks        atomic.Int32
	enabled       bool
}

func (s *stubPlugin) Name() string              { return s.name }
func (s *stubPlugin) DisplayName() string       { return s.name }
func (s *stubPlugin) IsConfigured() bool        { return s.configured }
func (s *stubPlugin) IsEnabled() bool           { return s.enabled }
func (s *stubPlugin) IsMetadataAvailable() bool { return s.metaAvailable }
func (s *stubPlugin) CheckConnection(context.Context) error {
	s.checks.Add(1)
	return s.checkErr
}
func (s *stubPlugin) Connected() bool                     { return s.checkErr == nil }
func (s *stubPlugin) CapabilityStatus() map[string]string { return nil }

func newStub(name string, configured, metaAvailable bool) *stubPlugin {
	return &stubPlugin{name: name, configured: configured, metaAvailable: metaAvailable, enabled: true}
}

func TestHealthCheckerProbesConfiguredPlugins(t *testing.T) {
	p := newStub("configured", true, false)
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	h.CheckNow(context.Background())
	if p.checks.Load() != 1 {
		t.Errorf("configured plugin checks = %d, want 1", p.checks.Load())
	}
}

func TestHealthCheckerSkipsUnconfiguredPlugins(t *testing.T) {
	p := newStub("unconfigured", false, false)
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	h.CheckNow(context.Background())
	if p.checks.Load() != 0 {
		t.Errorf("unconfigured plugin checks = %d, want 0", p.checks.Load())
	}
}

func TestHealthCheckerProbesMetadataAvailableWithoutConfig(t *testing.T) {
	// Metadata-only providers (e.g. Deezer without ARL) must still be probed
	// so their public-API reachability can be verified.
	p := newStub("metadata-only", false, true)
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	h.CheckNow(context.Background())
	if p.checks.Load() != 1 {
		t.Errorf("metadata-only plugin checks = %d, want 1", p.checks.Load())
	}
}

func TestHealthCheckerStatusReflectsResult(t *testing.T) {
	p := newStub("failing", true, false)
	p.checkErr = errors.New("unreachable")
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	h.CheckNow(context.Background())
	st := h.StatusOf("failing")
	if st == nil {
		t.Fatal("StatusOf returned nil")
	}
	if st.Connected {
		t.Error("status Connected = true, want false for failing check")
	}
	if st.Error != "unreachable" {
		t.Errorf("status Error = %q, want unreachable", st.Error)
	}
}

func TestHealthCheckerSkipsDisabledPlugins(t *testing.T) {
	p := newStub("disabled", true, false)
	p.enabled = false
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	h.CheckNow(context.Background())
	if p.checks.Load() != 0 {
		t.Errorf("disabled plugin checks = %d, want 0", p.checks.Load())
	}
}
