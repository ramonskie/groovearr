package jobs

import (
	"context"
	"errors"
	"fmt"
)

// UnmatchedRearmer re-arms parked terminal (unmatched/failed) albums so the
// refresh engine re-searches them. Declared here (not as the concrete
// *refresh.Service) so jobs depends on behavior, not construction (AGENTS §3).
// *refresh.Service satisfies it.
type UnmatchedRearmer interface {
	ReArmTerminalAlbums(ctx context.Context, limit int) (int, error)
}

// recheckUnmatchedCap bounds one recheck pass. The queue parks roughly one row
// per terminal album (a 90-day due_at), so the cap merely prevents a
// pathological queue from being re-armed unboundedly; a normal library is far
// below it.
const recheckUnmatchedCap = 100000

// RecheckUnmatched returns a Runner that re-arms every parked terminal
// (unmatched/failed) album so the background refresh engine re-searches it with
// the current matcher. It is the explicit, user-triggered override of the 90-day
// UnmatchedRetry park: the way to re-attempt the backlog after a matching
// improvement without waiting out the window.
//
// It performs no provider I/O itself — it moves the parked queue rows to
// "due now" and the service-owned engine does the paced, cooldown-aware
// re-search on its next drain. The pass is therefore fast and bounded, and its
// progress is visible on the catalog status card ("N due now" draining) rather
// than on this job.
func (r *Runners) RecheckUnmatched() Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Rearm == nil {
			return errors.New("catalog re-arm not available")
		}
		report(Report{Message: "re-arming unmatched albums"})

		n, err := r.deps.Rearm.ReArmTerminalAlbums(ctx, recheckUnmatchedCap)
		if err != nil {
			return fmt.Errorf("recheck unmatched: %w", err)
		}

		msg := "no unmatched or failed albums to re-arm"
		if n > 0 {
			msg = fmt.Sprintf("re-armed %d unmatched/failed albums; the refresh engine will re-search them", n)
			if n >= recheckUnmatchedCap {
				msg += " (capped; re-run to continue)"
			}
		}
		report(Report{Done: n, Total: n, Message: msg})
		return nil
	}
}
