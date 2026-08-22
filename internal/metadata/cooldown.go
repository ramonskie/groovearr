package metadata

import (
	"sync"
	"time"
)

// ProviderRateLimitCooldown is how long a provider is skipped after a
// rate-limit response (metadata.ErrRateLimited). Long enough to ride out a
// typical 429 backoff, short enough that a healthy provider is retried within
// the same job or scan. Var so tests can shrink it.
var ProviderRateLimitCooldown = 2 * time.Minute

// maxRateLimitCooldown caps how long a single server-provided Retry-After can
// park a provider. Prevents one pathological header (e.g. "retry after 1 day")
// from disabling a provider for the rest of a job. Var so tests can shrink it.
var maxRateLimitCooldown = 10 * time.Minute

// ProviderCooldown is a shared, mutex-guarded per-provider rate-limit
// cooldown. The app wires a single instance into the enrichment handler and
// the API server (album discovery, discover search) so a provider that just
// returned ErrRateLimited is skipped app-wide for a short window instead of
// being hammered repeatedly. The duplicate-scan path uses pause-and-retry
// instead of the cooldown.
type ProviderCooldown struct {
	mu    sync.Mutex
	until map[string]time.Time
}

// NewProviderCooldown returns an empty cooldown registry.
func NewProviderCooldown() *ProviderCooldown {
	return &ProviderCooldown{until: make(map[string]time.Time)}
}

// CoolingDown reports whether the named provider is currently in cooldown.
func (c *ProviderCooldown) CoolingDown(name string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Before(c.until[name])
}

// Mark puts the named provider in cooldown for ProviderRateLimitCooldown.
// No-op for an empty name.
func (c *ProviderCooldown) Mark(name string) {
	c.MarkAfter(name, 0)
}

// MarkAfter puts the named provider in cooldown for at least
// ProviderRateLimitCooldown, honoring a server-requested backoff (Retry-After)
// when it is longer, capped at maxRateLimitCooldown. Pass 0 to get the
// default cooldown. No-op for an empty name.
func (c *ProviderCooldown) MarkAfter(name string, retryAfter time.Duration) {
	if c == nil || name == "" {
		return
	}
	d := ProviderRateLimitCooldown
	if retryAfter > d {
		d = retryAfter
	}
	if d > maxRateLimitCooldown {
		d = maxRateLimitCooldown
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[name] = time.Now().Add(d)
}

// Reset clears all cooldowns. Call at the start of a fresh bulk job or scan so
// every provider is re-attempted rather than staying cooled down from an
// earlier run.
func (c *ProviderCooldown) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = make(map[string]time.Time)
}
