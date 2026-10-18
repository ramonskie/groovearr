package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/ramonskie/groovearr/internal/identity"
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

// RecheckUnmatched returns a Runner that first resolves parked unmatched albums
// by track identity (provider track id / ISRC / title+artist+duration), then
// materializes high-confidence releases. When no resolver is wired, or no
// configured provider exposes automatic identity capability, it falls back to
// the original behavior: re-arm every parked terminal album so the background
// refresh engine re-searches it with the current matcher. Review-only providers
// do not count as automatic capability.
//
// The identity pass is the explicit, user-triggered override of the 90-day
// UnmatchedRetry park. Ambiguous candidates stay parked for the review UI; the
// old album-title search is not re-run for them, because identity evidence has
// already established that the album string is not enough.
func (r *Runners) RecheckUnmatched() Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.UnmatchedResolver != nil {
			report(Report{Message: "resolving unmatched albums by track identity"})
			lastResolved := -1
			result, err := r.deps.UnmatchedResolver.ResolveUnmatched(ctx, func(p identity.Progress) {
				if p.Done%10 != 0 && p.Done != p.Total && p.Resolved == lastResolved {
					return
				}
				lastResolved = p.Resolved
				report(Report{
					Done:    p.Done,
					Total:   p.Total,
					Message: fmt.Sprintf("identity: %d/%d albums; %d resolved", p.Done, p.Total, p.Resolved),
				})
			})
			if err != nil {
				if !errors.Is(err, identity.ErrNoTrackProviders) || r.deps.Rearm == nil {
					return fmt.Errorf("recheck unmatched: %w", err)
				}
				report(Report{Message: "no automatic identity providers available (review-only providers are manual); falling back to re-arming unmatched albums"})
			} else {
				msg := fmt.Sprintf("identity resolved %d of %d unmatched albums; %d parked for review", result.Resolved, result.Total, result.Parked)
				if result.CoolingDown {
					msg = "every automatic identity provider is cooling down; nothing was resolved — retry later"
				} else {
					if result.NoIdentity > 0 {
						msg += fmt.Sprintf(" (%d had no identity candidate)", result.NoIdentity)
					}
					if result.Errors > 0 {
						msg += fmt.Sprintf("; %d errored", result.Errors)
					}
					if result.Skipped > 0 {
						msg += fmt.Sprintf("; %d already resolved", result.Skipped)
					}
				}
				done := result.Total
				if result.CoolingDown {
					done = 0
				}
				report(Report{Done: done, Total: result.Total, Message: msg})
				return nil
			}
		}

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
