// Package plugin defines the shared plugin framework used by all capability domains.
package plugin

import (
	"context"
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

// HealthChecker periodically verifies plugin connectivity by calling
// CheckConnection on each registered plugin. Results are reported via
// the plugin's Connected() method (which providers implement as an
// atomic/synchronized bool).
type HealthChecker struct {
	registry *Registry
	log      *slog.Logger
	interval time.Duration

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

	start := time.Now()
	err := p.CheckConnection(ctx)
	elapsed := time.Since(start)

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
