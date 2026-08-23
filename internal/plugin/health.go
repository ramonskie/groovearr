// Package plugin defines the shared plugin framework used by all capability domains.
package plugin

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// HealthStatus tracks the result of a health check for a single plugin.
type HealthStatus struct {
	Name      string
	Connected bool
	Error     string
	CheckedAt time.Time
}

// metadataAvailable marks plugins that can serve metadata without their
// primary credentials (e.g. Deezer via the public API without an ARL).
// The health checker probes these even when IsConfigured() is false so
// their reachability can be verified and reflected in capability status.
type metadataAvailable interface {
	IsMetadataAvailable() bool
}

// CooldownSource reports whether a named provider is currently cooling down
// from a rate-limit response, and lets callers re-mark it. Implemented by
// *metadata.ProviderCooldown; declared here as an interface so plugin does not
// import metadata (metadata imports plugin for BasePlugin).
type CooldownSource interface {
	CoolingDown(name string) bool
	MarkAfter(name string, retryAfter time.Duration)
}

// rateLimitedBackoffer is implemented by errors that carry a server-requested
// backoff (e.g. *metadata.RateLimitError). Declared as an interface so plugin
// can detect a rate-limit error without importing metadata.
type rateLimitedBackoffer interface {
	RateLimitBackoff() time.Duration
}

// rateLimitBackoffOf returns the server-requested backoff for a rate-limit
// error, or nil when err is not a rate-limit error.
func rateLimitBackoffOf(err error) *time.Duration {
	var rl rateLimitedBackoffer
	if errors.As(err, &rl) {
		d := rl.RateLimitBackoff()
		return &d
	}
	return nil
}

// HealthChecker periodically verifies plugin connectivity by calling
// CheckConnection on each registered plugin. Results are reported via
// the plugin's Connected() method (which providers implement as an
// atomic/synchronized bool).
type HealthChecker struct {
	registry *Registry
	log      *slog.Logger
	interval time.Duration

	// cooldown is the shared provider rate-limit bucket. When set, a provider
	// currently cooling down is not probed: re-probing a throttled API would
	// go around the server-requested backoff and can re-arm a longer 429.
	// The previous status is kept so the provider isn't shown as disconnected.
	cooldown CooldownSource

	mu     sync.RWMutex
	status map[string]HealthStatus // latest result per plugin
}

// NewHealthChecker creates a health checker that probes all plugins
// every interval. Pass 0 to disable periodic checks (only manual via CheckNow).
func NewHealthChecker(registry *Registry, interval time.Duration, logger *slog.Logger) *HealthChecker {
	return &HealthChecker{
		registry: registry,
		log:      logger,
		interval: interval,
		status:   make(map[string]HealthStatus),
	}
}

// Start begins periodic health checks in a background goroutine.
// Returns immediately. Call Shutdown to stop.
func (h *HealthChecker) Start(ctx context.Context) {
	if h.interval <= 0 {
		return
	}
	go h.loop(ctx)
}

// Shutdown stops the background health check loop.
func (h *HealthChecker) Shutdown() {
	// Cancellation handled via the context passed to Start.
}

// SetProviderCooldown wires the shared provider rate-limit cooldown so the
// periodic probe skips providers currently cooling down. Pass nil to disable.
func (h *HealthChecker) SetProviderCooldown(c CooldownSource) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cooldown = c
}

// CheckNow runs a health check on all registered plugins immediately.
func (h *HealthChecker) CheckNow(ctx context.Context) {
	plugins := h.registry.All()
	for _, p := range plugins {
		h.checkOne(ctx, p)
	}
}

// Status returns the latest health status for all plugins.
func (h *HealthChecker) Status() map[string]HealthStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]HealthStatus, len(h.status))
	for k, v := range h.status {
		out[k] = v
	}
	return out
}

// StatusOf returns the latest health status for a single plugin, or nil.
func (h *HealthChecker) StatusOf(name string) *HealthStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if s, ok := h.status[name]; ok {
		return &s
	}
	return nil
}

func (h *HealthChecker) loop(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	// Run once immediately at startup.
	h.CheckNow(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.CheckNow(ctx)
		}
	}
}

func (h *HealthChecker) checkOne(ctx context.Context, p BasePlugin) {
	// Skip unconfigured plugins — no credentials means the probe will
	// always fail and produces nothing but noise. Exception: providers that
	// can serve metadata without their primary credentials (e.g. Deezer's
	// public API) are still probed so that reachability can be verified and
	// reflected in their capability status.
	if !p.IsConfigured() {
		// Providers that serve a capability without credentials (e.g. Deezer's
		// public metadata API) are still probed so reachability can be verified
		// and reflected in capability status. Others produce only noise.
		if !CanProbeUnconfigured(p) {
			return
		}
	}
	// Respect explicit disable toggle when the plugin supports it.
	if enabler, ok := p.(Enabler); ok && !enabler.IsEnabled() {
		return
	}

	h.mu.RLock()
	cooldown := h.cooldown
	h.mu.RUnlock()

	// Respect the shared provider rate-limit cooldown: a provider that
	// recently rate-limited must not be probed — re-probing it would go
	// around the server-requested backoff and can re-arm a longer 429.
	// Skip the probe and keep the previous status.
	if cooldown != nil && cooldown.CoolingDown(p.Name()) {
		h.log.Debug("health check skipped: provider cooling down",
			"plugin", p.Name(),
			"component", "health",
		)
		return
	}

	start := time.Now()
	err := p.CheckConnection(ctx)
	elapsed := time.Since(start)

	// A probe that returns a rate-limit error re-marks the shared cooldown so
	// the next probe is skipped instead of re-arming a longer backoff. A 429 is
	// evidence the API is reachable — record it as connected (with the backoff
	// noted) rather than keeping a stale disconnected status for the whole
	// cooldown.
	if cooldown != nil {
		if ra := rateLimitBackoffOf(err); ra != nil {
			cooldown.MarkAfter(p.Name(), *ra)
			h.log.Warn("health probe rate limited, re-marking cooldown",
				"plugin", p.Name(),
				"retry_after", *ra,
				"component", "health",
			)
			h.mu.Lock()
			h.status[p.Name()] = HealthStatus{
				Name:      p.Name(),
				Connected: true,
				Error:     "rate limited (retry after " + ra.String() + ")",
				CheckedAt: time.Now(),
			}
			h.mu.Unlock()
			return
		}
	}

	hs := HealthStatus{
		Name:      p.Name(),
		Connected: err == nil,
		CheckedAt: time.Now(),
	}
	if err != nil {
		hs.Error = err.Error()
		h.log.Warn("plugin health check failed",
			"plugin", p.Name(),
			"error", err,
			"elapsed_ms", elapsed.Milliseconds(),
			"component", "health",
		)
	} else {
		h.log.Debug("plugin health check passed",
			"plugin", p.Name(),
			"elapsed_ms", elapsed.Milliseconds(),
			"component", "health",
		)
	}

	h.mu.Lock()
	h.status[p.Name()] = hs
	h.mu.Unlock()
}
