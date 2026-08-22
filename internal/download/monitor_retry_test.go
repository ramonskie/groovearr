package download

import "testing"

func TestJitterBackoff(t *testing.T) {
	const cap = 60

	// Bounds: always ≥ 1 and ≤ cap, within ±20% of the input.
	for _, base := range []int{2, 4, 8, 16, 30, 60} {
		for i := 0; i < 100; i++ {
			got := jitterBackoff(base, cap)
			if got < 1 || got > cap {
				t.Fatalf("jitterBackoff(%d) = %d, want within [1, %d]", base, got, cap)
			}
			// Allow one extra minute of slack for rounding at small bases.
			min := base - base/5 - 1
			max := base + base/5 + 1
			if got < min || got > max {
				t.Fatalf("jitterBackoff(%d) = %d, want within [%d, %d]", base, got, min, max)
			}
		}
	}

	// Over-cap bases collapse to the cap window and must still vary (no lockstep).
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		got := jitterBackoff(128, cap)
		if got > cap {
			t.Fatalf("jitterBackoff(128) = %d, want ≤ %d", got, cap)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Errorf("jitterBackoff(128) produced a single value %v, want variance near cap", seen)
	}

	if got := jitterBackoff(0, cap); got != 1 {
		t.Errorf("jitterBackoff(0) = %d, want 1", got)
	}
	if got := jitterBackoff(-5, cap); got != 1 {
		t.Errorf("jitterBackoff(-5) = %d, want 1", got)
	}
}
