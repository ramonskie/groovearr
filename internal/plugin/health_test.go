package plugin

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
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

// stubCooldown is a minimal CooldownSource for tests.
type stubCooldown struct {
	cooling []string
	marked  map[string]time.Duration
}

func (c *stubCooldown) CoolingDown(name string) bool {
	for _, n := range c.cooling {
		if n == name {
			return true
		}
	}
	return false
}

func (c *stubCooldown) MarkAfter(name string, retryAfter time.Duration) {
	if c.marked == nil {
		c.marked = make(map[string]time.Duration)
	}
	c.marked[name] = retryAfter
	// Model ProviderCooldown semantics: a mark puts the provider in cooldown.
	if !c.CoolingDown(name) {
		c.cooling = append(c.cooling, name)
	}
}

// stubRateLimitedError implements rateLimitedBackoffer for tests.
type stubRateLimitedError struct{ backoff time.Duration }

func (e *stubRateLimitedError) Error() string                   { return "provider rate limited: stub" }
func (e *stubRateLimitedError) Unwrap() error                   { return errors.New("provider rate limited") }
func (e *stubRateLimitedError) RateLimitBackoff() time.Duration { return e.backoff }

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

// TestHealthCheckerCooldownSkipsOnlyCooledProviders verifies the shared
// rate-limit cooldown is honored: a provider cooling down is not probed (so a
// 429 backoff is not re-armed), while non-cooled providers still are.
func TestHealthCheckerCooldownSkipsOnlyCooledProviders(t *testing.T) {
	cooled := newStub("cooled", true, false)
	normal := newStub("normal", true, false)

	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	h.SetProviderCooldown(&stubCooldown{cooling: []string{"cooled"}})
	for _, p := range []*stubPlugin{cooled, normal} {
		if err := h.registry.Register(p); err != nil {
			t.Fatalf("register: %v", err)
		}
	}

	h.CheckNow(context.Background())

	if cooled.checks.Load() != 0 {
		t.Errorf("cooled plugin checks = %d, want 0 (must skip probe)", cooled.checks.Load())
	}
	if normal.checks.Load() != 1 {
		t.Errorf("normal plugin checks = %d, want 1", normal.checks.Load())
	}
	// Skipped probe must not write a status entry — the previous status is kept.
	if st := h.StatusOf("cooled"); st != nil {
		t.Errorf("skipped probe should not set status, got %+v", st)
	}
}

// TestHealthCheckerNilCooldownProbesAll verifies a nil cooldown (not wired)
// leaves the probe behavior unchanged.
func TestHealthCheckerNilCooldownProbesAll(t *testing.T) {
	p := newStub("nocd", true, false)
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	h.CheckNow(context.Background())
	if p.checks.Load() != 1 {
		t.Errorf("plugin checks = %d, want 1 (nil cooldown must not skip)", p.checks.Load())
	}
}

// TestHealthCheckerCooldownKeepsLastStatus verifies the "keep last status"
// contract: after a successful probe, a provider entering cooldown has its
// next probe skipped and the previous status preserved.
func TestHealthCheckerCooldownKeepsLastStatus(t *testing.T) {
	p := newStub("cooled", true, false)
	cd := &stubCooldown{}
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	h.SetProviderCooldown(cd)
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}

	// First probe succeeds and records a connected status.
	h.CheckNow(context.Background())
	if p.checks.Load() != 1 {
		t.Fatalf("plugin checks = %d, want 1 after first probe", p.checks.Load())
	}
	if st := h.StatusOf("cooled"); st == nil || !st.Connected {
		t.Fatalf("expected connected status after first probe, got %+v", st)
	}

	// Provider enters cooldown: next probe is skipped, old status kept.
	cd.cooling = []string{"cooled"}
	h.CheckNow(context.Background())
	if p.checks.Load() != 1 {
		t.Errorf("plugin checks = %d, want 1 (probe skipped during cooldown)", p.checks.Load())
	}
	if st := h.StatusOf("cooled"); st == nil || !st.Connected {
		t.Errorf("previous status must be kept during cooldown, got %+v", st)
	}
}

// TestHealthCheckerRateLimitRemarksCooldown verifies a probe returning a
// rate-limit error re-marks the shared cooldown so the next probe is skipped
// instead of re-arming a longer backoff.
func TestHealthCheckerRateLimitRemarksCooldown(t *testing.T) {
	p := newStub("rl", true, false)
	p.checkErr = &stubRateLimitedError{backoff: 7 * time.Minute}
	cd := &stubCooldown{}
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	h.SetProviderCooldown(cd)
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}

	h.CheckNow(context.Background())
	if got := cd.marked["rl"]; got != 7*time.Minute {
		t.Errorf("cooldown mark = %v, want 7m", got)
	}
	// A rate-limited probe is reachable (it answered 429) — it is recorded as
	// connected, not disconnected.
	st := h.StatusOf("rl")
	if st == nil || !st.Connected {
		t.Fatalf("rate-limited probe should record connected status, got %+v", st)
	}

	// Provider is now cooling down — the next probe is skipped.
	h.CheckNow(context.Background())
	if p.checks.Load() != 1 {
		t.Errorf("plugin checks = %d, want 1 (second probe skipped)", p.checks.Load())
	}
}

// TestHealthCheckerNonRateLimitErrorDoesNotMark verifies a probe failure that
// is not a rate-limit error leaves the cooldown untouched.
func TestHealthCheckerNonRateLimitErrorDoesNotMark(t *testing.T) {
	p := newStub("down", true, false)
	p.checkErr = errors.New("unreachable")
	cd := &stubCooldown{}
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	h.SetProviderCooldown(cd)
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}

	h.CheckNow(context.Background())
	if _, ok := cd.marked["down"]; ok {
		t.Errorf("non-rate-limit failure must not mark cooldown, got %v", cd.marked)
	}
}

// TestHealthCheckerRequestCheckWithRunningLoop verifies RequestCheck pokes the
// background loop: only the named plugin is probed, without blocking the caller.
func TestHealthCheckerRequestCheckWithRunningLoop(t *testing.T) {
	a := newStub("a", true, false)
	b := newStub("b", true, false)
	h := NewHealthChecker(NewRegistry(), time.Hour, slog.New(slog.DiscardHandler))
	for _, p := range []*stubPlugin{a, b} {
		if err := h.registry.Register(p); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	// The loop probes all plugins once at startup — wait for that pass.
	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)
	defer cancel()
	waitFor(t, func() bool { return a.checks.Load() >= 1 && b.checks.Load() >= 1 }, 2*time.Second)

	h.RequestCheck([]string{"a"})
	waitFor(t, func() bool { return a.checks.Load() >= 2 }, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	if b.checks.Load() != 1 {
		t.Errorf("non-requested plugin checks = %d, want 1", b.checks.Load())
	}
}

// TestHealthCheckerRequestCheckWithoutLoop verifies RequestCheck still probes
// when the periodic loop is disabled (interval <= 0): the check runs on a
// single bounded goroutine owned by the checker.
func TestHealthCheckerRequestCheckWithoutLoop(t *testing.T) {
	p := newStub("solo", true, false)
	h := NewHealthChecker(NewRegistry(), 0, slog.New(slog.DiscardHandler))
	if err := h.registry.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	h.RequestCheck([]string{"solo"})
	waitFor(t, func() bool { return p.checks.Load() >= 1 }, 2*time.Second)
}

// waitFor polls cond until it returns true or the timeout elapses.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
