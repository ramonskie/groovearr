package metadata

import (
	"sync"
	"time"
)

// ProviderRateLimitCooldown is how long a provider is skipped after a
// rate-limit response (metadata.ErrRateLimited) when the server supplied no
// Retry-After. Long enough to ride out a typical 429 backoff, short enough
// that a healthy provider is retried within the same job or scan. Repeated
// failures without a server backoff escalate (see escalationWindow below).
// Var so tests can shrink it.
var ProviderRateLimitCooldown = 2 * time.Minute

// maxRateLimitCooldown caps how long a provider can be parked. Mirrors
// SoulSync's 4h escalation cap: long enough to honor a real server backoff
// (e.g. Spotify 1h53m), short enough that a pathological header can't disable
// a provider for a whole day. Var so tests can shrink it.
var maxRateLimitCooldown = 4 * time.Hour

// banThreshold is the Retry-After length above which a response is treated as
// a real ban (rather than a transient blip). Ban-length backoffs get a
// post-ban grace period on top.
var banThreshold = time.Minute

// postBanGrace is extra cooldown added after a server-requested ban expires.
// Prevents the "immediate re-probe → re-ban" loop where the server-side
// cooldown outlasts its own Retry-After (observed by SoulSync: a single call
// 32s after cooldown end re-armed a 4-hour ban).
var postBanGrace = 5 * time.Minute

// escalationWindow bounds how close repeated rate-limit hits must be to count
// as an escalation. Hits further apart than this reset the counter.
var escalationWindow = time.Hour

// maxEscalationCooldown caps the escalated (guessed) cooldown. Matches
// SoulSync's 4h cap; the *arr family goes further (24h) but 4h is a sane
// bound for a self-hosted app.
var maxEscalationCooldown = 4 * time.Hour

// maxEvents bounds the in-memory rate-limit event ring for Status().
const maxEvents = 200

// CooldownEntry is a persisted cooldown: provider → expiry time.
type CooldownEntry struct {
	Provider string
	Until    time.Time
}

// RateLimitEvent records a cooldown being applied, for observability.
type RateLimitEvent struct {
	TS         time.Time
	Provider   string
	Duration   time.Duration
	Source     string // "server", "server+grace", "default", "escalated"
	RetryAfter time.Duration
}

// Store persists cooldown state and rate-limit events so a long backoff
// survives a restart and is observable. Implemented by the SQLite store;
// nil means no persistence.
type Store interface {
	SetCooldown(provider string, until time.Time) error
	DeleteCooldown(provider string) error
	LoadCooldowns() ([]CooldownEntry, error)
	LogRateLimitEvent(provider string, duration time.Duration, source string, retryAfter time.Duration) error
	LoadRateLimitEvents(limit int) ([]RateLimitEvent, error)
}

// hitInfo tracks repeated rate-limit hits within the escalation window.
type hitInfo struct {
	count int
	first time.Time
}

// ProviderCooldown is a shared, mutex-guarded per-provider rate-limit
// cooldown. The app wires a single instance into the enrichment handler, the
// API server (album discovery, discover search) and the health checker so a
// provider that just returned ErrRateLimited is skipped app-wide for the
// backoff window instead of being hammered repeatedly. The duplicate-scan path
// uses pause-and-retry instead of the cooldown.
//
// Cooldown state is never wiped between runs: a fresh bulk job must not go
// around a server-requested backoff. When a Store is wired, marks are
// persisted so a long backoff survives a restart, and rate-limit events are
// recorded for observability.
type ProviderCooldown struct {
	mu     sync.Mutex
	until  map[string]time.Time
	hits   map[string]hitInfo // escalation state for no-server-backoff marks
	events []RateLimitEvent   // in-memory ring for Status()
	store  Store
}

// NewProviderCooldown returns an empty cooldown registry.
func NewProviderCooldown() *ProviderCooldown {
	return &ProviderCooldown{
		until:  make(map[string]time.Time),
		hits:   make(map[string]hitInfo),
		events: make([]RateLimitEvent, 0, maxEvents),
	}
}

// SetStore wires optional persistence + event logging. Pass nil to keep the
// cooldown purely in-memory. Call before Restore.
func (c *ProviderCooldown) SetStore(s Store) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = s
}

// Restore reloads persisted cooldowns (e.g. from a previous process) so a
// long server-requested backoff survives a restart. Expired entries are
// dropped. No-op without a Store.
func (c *ProviderCooldown) Restore() {
	if c == nil {
		return
	}
	c.mu.Lock()
	store := c.store
	c.mu.Unlock()
	if store == nil {
		return
	}
	entries, err := store.LoadCooldowns()
	if err != nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range entries {
		if e.Until.After(now) {
			c.until[e.Provider] = e.Until
		}
	}
	// Repopulate the event ring from the durable log so the debug endpoint
	// still shows recent history after a restart.
	if events, err := store.LoadRateLimitEvents(maxEvents); err == nil && len(events) > 0 {
		c.events = events
	}
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

// Mark puts the named provider in cooldown for ProviderRateLimitCooldown
// (escalating on repeated hits). No-op for an empty name.
func (c *ProviderCooldown) Mark(name string) {
	c.MarkAfter(name, 0)
}

// MarkAfter puts the named provider in cooldown for at least
// ProviderRateLimitCooldown, honoring a server-requested backoff (Retry-After)
// when it is longer. Server backoffs longer than banThreshold get a post-ban
// grace on top; both are capped at maxRateLimitCooldown. Pass 0 to get the
// default cooldown, which escalates on repeated hits within escalationWindow.
// No-op for an empty name.
func (c *ProviderCooldown) MarkAfter(name string, retryAfter time.Duration) {
	if c == nil || name == "" {
		return
	}
	d := ProviderRateLimitCooldown
	if retryAfter > d {
		d = retryAfter
	}

	now := time.Now()
	source := "default"
	c.mu.Lock()

	if retryAfter > 0 {
		// Server-provided backoff: honor it. A real ban (≥ banThreshold) gets
		// a post-ban grace so an immediate re-probe right after expiry can't
		// re-arm a longer ban. Server info is authoritative — reset escalation.
		if retryAfter >= banThreshold {
			d += postBanGrace
			source = "server+grace"
		} else {
			source = "server"
		}
		delete(c.hits, name)
	} else {
		// No server backoff (we're guessing): escalate on repeated hits within
		// the window — circuit-breaker style, like the *arr family and
		// SoulSync. A provider that keeps failing gets longer cooldowns.
		h := c.hits[name]
		if h.count == 0 || now.Sub(h.first) > escalationWindow {
			h = hitInfo{count: 1, first: now}
		} else {
			h.count++
		}
		c.hits[name] = h
		d = escalationDuration(h.count)
		if h.count > 1 {
			source = "escalated"
		}
	}

	if d > maxRateLimitCooldown {
		d = maxRateLimitCooldown
	}

	until := now.Add(d)
	c.until[name] = until
	c.events = append(c.events, RateLimitEvent{
		TS: now, Provider: name, Duration: d, Source: source, RetryAfter: retryAfter,
	})
	if len(c.events) > maxEvents {
		c.events = c.events[len(c.events)-maxEvents:]
	}
	store := c.store
	c.mu.Unlock()

	// Persist best-effort outside the lock — never fail the caller.
	if store != nil {
		_ = store.SetCooldown(name, until)
		_ = store.LogRateLimitEvent(name, d, source, retryAfter)
	}
}

// escalationDuration doubles the default cooldown per repeated hit, capped at
// maxEscalationCooldown. count=1 → ProviderRateLimitCooldown.
func escalationDuration(count int) time.Duration {
	d := ProviderRateLimitCooldown
	for i := 1; i < count && d < maxEscalationCooldown; i++ {
		d *= 2
	}
	if d > maxEscalationCooldown {
		d = maxEscalationCooldown
	}
	return d
}

// Clear removes a provider's cooldown immediately (manual override, e.g. via
// the debug endpoint). Also clears its escalation state. Returns true when a
// cooldown was actually removed. The lock is held across the store delete so a
// concurrent MarkAfter cannot diverge memory from persisted state.
func (c *ProviderCooldown) Clear(name string) bool {
	if c == nil || name == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.until[name]; !ok {
		return false
	}
	delete(c.until, name)
	delete(c.hits, name)
	if c.store != nil {
		_ = c.store.DeleteCooldown(name)
	}
	return true
}

// Reset clears all cooldowns. No longer called in production — the cooldown is
// intentionally never wiped between runs (a fresh bulk job must not go around
// a server-requested backoff). Retained for tests that need a clean slate.
func (c *ProviderCooldown) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = make(map[string]time.Time)
	c.hits = make(map[string]hitInfo)
}

// Status returns a snapshot of active cooldowns and recent rate-limit events
// for observability/debug endpoints.
func (c *ProviderCooldown) Status() ([]CooldownEntry, []RateLimitEvent) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var cd []CooldownEntry
	for name, until := range c.until {
		if until.After(now) {
			cd = append(cd, CooldownEntry{Provider: name, Until: until})
		}
	}
	events := make([]RateLimitEvent, len(c.events))
	copy(events, c.events)
	return cd, events
}
