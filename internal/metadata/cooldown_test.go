package metadata

import (
	"testing"
	"time"
)

func TestProviderCooldownLifecycle(t *testing.T) {
	c := NewProviderCooldown()

	if c.CoolingDown("spotify") {
		t.Error("fresh cooldown should not report cooling down")
	}

	c.Mark("spotify")
	if !c.CoolingDown("spotify") {
		t.Error("provider should be cooling down after Mark")
	}
	if c.CoolingDown("tidal") {
		t.Error("unrelated provider must not be affected")
	}

	c.Reset()
	if c.CoolingDown("spotify") {
		t.Error("Reset should clear all cooldowns")
	}
}

func TestProviderCooldownExpiry(t *testing.T) {
	orig := ProviderRateLimitCooldown
	ProviderRateLimitCooldown = 20 * time.Millisecond
	defer func() { ProviderRateLimitCooldown = orig }()

	c := NewProviderCooldown()
	c.Mark("musicbrainz")
	if !c.CoolingDown("musicbrainz") {
		t.Fatal("expected cooling down right after Mark")
	}
	time.Sleep(40 * time.Millisecond)
	if c.CoolingDown("musicbrainz") {
		t.Error("cooldown should have expired")
	}
}

func TestProviderCooldownNilSafe(t *testing.T) {
	var c *ProviderCooldown
	if c.CoolingDown("x") {
		t.Error("nil cooldown should report not cooling down")
	}
	c.Mark("x") // must not panic
	c.Reset()   // must not panic
}

func TestProviderCooldownMarkAfter(t *testing.T) {
	orig := ProviderRateLimitCooldown
	ProviderRateLimitCooldown = 2 * time.Minute
	defer func() { ProviderRateLimitCooldown = orig }()
	maxOrig := maxRateLimitCooldown
	maxRateLimitCooldown = 10 * time.Minute
	defer func() { maxRateLimitCooldown = maxOrig }()

	t.Run("short retry-after stays at floor", func(t *testing.T) {
		c := NewProviderCooldown()
		c.MarkAfter("spotify", 5*time.Second)
		// Cooldown should be ≥ the default floor (minus µs of test latency), not 5s.
		if d := c.until["spotify"].Sub(time.Now()); d < ProviderRateLimitCooldown-time.Second {
			t.Errorf("cooldown = %v, want at least floor %v", d, ProviderRateLimitCooldown)
		}
	})

	t.Run("long retry-after is honored", func(t *testing.T) {
		c := NewProviderCooldown()
		c.MarkAfter("discogs", 7*time.Minute)
		if d := c.until["discogs"].Sub(time.Now()); d < 6*time.Minute {
			t.Errorf("cooldown = %v, want ~7m honoring retry-after", d)
		}
	})

	t.Run("pathological retry-after is capped", func(t *testing.T) {
		c := NewProviderCooldown()
		c.MarkAfter("tidal", 24*time.Hour)
		if d := c.until["tidal"].Sub(time.Now()); d > maxRateLimitCooldown+time.Second {
			t.Errorf("cooldown = %v, want capped at %v", d, maxRateLimitCooldown)
		}
	})
}

func TestProviderCooldownEscalation(t *testing.T) {
	orig := ProviderRateLimitCooldown
	ProviderRateLimitCooldown = time.Minute
	defer func() { ProviderRateLimitCooldown = orig }()

	c := NewProviderCooldown()
	c.Mark("spotify") // hit 1 → base 1m
	if d := c.until["spotify"].Sub(time.Now()); d < 50*time.Second {
		t.Fatalf("hit 1 cooldown = %v, want ~1m", d)
	}
	c.Mark("spotify") // hit 2 → 2m
	if d := c.until["spotify"].Sub(time.Now()); d < 2*time.Minute-2*time.Second {
		t.Errorf("hit 2 cooldown = %v, want ~2m (escalated)", d)
	}
	c.Mark("spotify") // hit 3 → 4m
	if d := c.until["spotify"].Sub(time.Now()); d < 4*time.Minute-2*time.Second {
		t.Errorf("hit 3 cooldown = %v, want ~4m (escalated)", d)
	}
}

func TestProviderCooldownEscalationResetsAfterWindow(t *testing.T) {
	origPC := ProviderRateLimitCooldown
	ProviderRateLimitCooldown = 10 * time.Millisecond
	defer func() { ProviderRateLimitCooldown = origPC }()
	origWin := escalationWindow
	escalationWindow = 30 * time.Millisecond
	defer func() { escalationWindow = origWin }()

	c := NewProviderCooldown()
	c.Mark("spotify")
	c.Mark("spotify") // escalated (2nd hit)
	time.Sleep(40 * time.Millisecond)
	c.Mark("spotify") // past window → hit 1 → base duration
	if d := c.until["spotify"].Sub(time.Now()); d > 30*time.Millisecond {
		t.Errorf("after window reset cooldown = %v, want base ~10ms", d)
	}
}

func TestProviderCooldownServerRetryAfterResetsEscalation(t *testing.T) {
	orig := ProviderRateLimitCooldown
	ProviderRateLimitCooldown = time.Minute
	defer func() { ProviderRateLimitCooldown = orig }()

	c := NewProviderCooldown()
	c.Mark("spotify")
	c.Mark("spotify") // escalated to 2m
	// A server-provided backoff is authoritative — resets the hit counter.
	c.MarkAfter("spotify", 30*time.Second)
	// Next guess returns to the base duration, not escalated.
	c.Mark("spotify")
	if d := c.until["spotify"].Sub(time.Now()); d > 70*time.Second {
		t.Errorf("post-server cooldown = %v, want base ~1m (escalation reset)", d)
	}
}

func TestProviderCooldownClear(t *testing.T) {
	c := NewProviderCooldown()
	c.Mark("spotify")
	if !c.CoolingDown("spotify") {
		t.Fatal("expected cooling down after Mark")
	}
	if !c.Clear("spotify") {
		t.Error("Clear should report removal for a cooling provider")
	}
	if c.CoolingDown("spotify") {
		t.Error("Clear should remove the cooldown")
	}
	// Clearing an unknown provider returns false and must not panic.
	if c.Clear("nope") {
		t.Error("Clear should report false for an unknown provider")
	}
}

func TestProviderCooldownPostBanGrace(t *testing.T) {
	orig := ProviderRateLimitCooldown
	ProviderRateLimitCooldown = time.Minute
	defer func() { ProviderRateLimitCooldown = orig }()

	c := NewProviderCooldown()
	// 90s retry-after ≥ banThreshold (60s) → ban + 5m grace.
	c.MarkAfter("spotify", 90*time.Second)
	if d := c.until["spotify"].Sub(time.Now()); d < 6*time.Minute-2*time.Second {
		t.Errorf("cooldown = %v, want ban(90s)+grace(5m) ≈ 6m30s", d)
	}
	// Short transient retry-after (< banThreshold) gets no grace.
	c.MarkAfter("tidal", 5*time.Second)
	if d := c.until["tidal"].Sub(time.Now()); d > 70*time.Second {
		t.Errorf("short retry-after cooldown = %v, want no grace (~1m)", d)
	}
}
