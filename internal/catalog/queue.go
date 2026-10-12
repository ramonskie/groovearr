package catalog

import "time"

// Refresh-queue retry timing. These are pure, deterministic values so callers
// and tests can compute the same schedule without touching the database.
const (
	// BackoffBase is the delay before the first retry of a failed refresh.
	BackoffBase = time.Minute
	// BackoffCap caps the exponential retry delay. After enough failures the
	// delay stops growing so a persistent error cannot push a row years out.
	BackoffCap = 24 * time.Hour
	// UnmatchedRetry is the long retry interval used for terminal "unmatched"
	// entities. An entity no provider can match is retried only this often so
	// the queue drains instead of re-searching the provider forever.
	UnmatchedRetry = 90 * 24 * time.Hour
)

// BackoffFor returns the delay before attempt n (1-based) retries.
//
// The schedule is exponential: BackoffBase * 2^(n-1), capped at BackoffCap. It
// is deterministic — no jitter — so it is reproducible in tests and callers
// agree on the next due time. attempts <= 1 yields BackoffBase.
//
// Example (BackoffBase=1m): 1→1m, 2→2m, 3→4m, 4→8m, … capped at 24h.
func BackoffFor(attempts int) time.Duration {
	if attempts <= 1 {
		return BackoffBase
	}
	d := BackoffBase
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= BackoffCap {
			return BackoffCap
		}
	}
	return d
}
