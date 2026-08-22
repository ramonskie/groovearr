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
	})}
