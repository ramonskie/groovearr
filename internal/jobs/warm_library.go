package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/refresh"
)

// WarmRefresher is the slice of the refresh engine the warm-library job drives:
// materialize one album's provider tracklist. Declared here (not as the concrete
// *refresh.Service) so jobs depends on behavior, not construction (AGENTS §3).
// *refresh.Service satisfies it.
type WarmRefresher interface {
	RefreshAlbum(ctx context.Context, albumID int64) (refresh.AlbumOutcome, error)
}

// warmLibraryTimeout bounds a single album's materialization so a wedged
// provider call cannot pin the job's single-flight slot (and with it shutdown)
// indefinitely. A package var, not a const, so tests can shorten it.
var warmLibraryTimeout = 2 * time.Minute

// WarmLibrary returns a Runner that materializes provider metadata for a bounded
// batch of library albums that need warming. It is the opt-in tier-3 "warm
// library" pass: default off and user-triggered only — it registers no ticker
// and runs no periodic sweep.
//
// Behavior:
//   - Candidates are recomputed at the start of every invocation from the catalog
//     and the local library (refresh.WarmCandidates): new first, then
//     never-materialized, then incomplete. Complete albums are skipped. This is
//     what makes the job resumable — an album materialized on an earlier run is
//     now complete and drops out, so no persisted cursor is needed.
//   - Only a bounded batch (the candidate limit) is processed per invocation, so
//     the runner yields the Manager's single-flight slot between chunks and never
//     monopolises it. Re-triggering the job resumes with the remaining
//     candidates.
//   - Each album is materialized through the shared refresh engine, so the
//     pass inherits its provider-link reuse and the one app-wide cooldown
//     bucket (AGENTS §1, §8). Provider I/O happens only inside the engine.
//   - Progress and a pace-based ETA are reported after every album. ctx is
//     observed between albums, so a cancelled job (or server shutdown) stops
//     promptly; a parent cancellation returns context.Canceled cleanly.
//   - A nil Refresh or Catalog dependency returns a clear error rather than
//     panicking.
func (r *Runners) WarmLibrary() Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Refresh == nil {
			return errors.New("refresh service not available")
		}
		if r.deps.Catalog == nil {
			return errors.New("catalog store not available")
		}
		if r.deps.Store == nil {
			return errors.New("library store not available")
		}

		opts := refresh.DefaultWarmOptions()
		if r.deps.WarmOptions != nil {
			opts = r.deps.WarmOptions()
		}

		candidates, err := refresh.WarmCandidates(ctx, r.deps.Catalog, r.deps.Store, opts)
		if err != nil {
			return fmt.Errorf("warm library: select candidates: %w", err)
		}
		total := len(candidates)
		if total == 0 {
			report(Report{Message: "library already warm"})
			return nil
		}
		report(Report{Done: 0, Total: total, Message: fmt.Sprintf("warming %d albums", total)})

		started := time.Now()
		var warmed, failed int
		for i, albumID := range candidates {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}

			actx, cancel := context.WithTimeout(ctx, warmLibraryTimeout)
			_, rerr := r.deps.Refresh.RefreshAlbum(actx, albumID)
			cancel()

			if rerr != nil {
				// A cancelled parent is a clean stop, not an album failure.
				if cerr := ctx.Err(); cerr != nil {
					return cerr
				}
				failed++
				r.logWarn("warm library album failed", "album_id", albumID, "error", rerr)
			} else {
				warmed++
			}

			done := i + 1
			report(Report{
				Done:    done,
				Total:   total,
				Message: warmProgressMessage(done, total, started, failed),
			})
		}

		report(Report{
			Done:    total,
			Total:   total,
			Message: fmt.Sprintf("warmed %d albums, %d failed", warmed, failed),
		})
		return nil
	}
}

// warmProgressMessage renders "done/total albums, eta <d>". The ETA is the
// observed per-album pace extrapolated to the albums still to process; it is
// omitted until at least one album has finished (no pace to extrapolate from).
func warmProgressMessage(done, total int, started time.Time, failed int) string {
	if done <= 0 {
		return fmt.Sprintf("warming %d albums", total)
	}
	per := time.Since(started) / time.Duration(done)
	remaining := per * time.Duration(total-done)
	msg := fmt.Sprintf("%d/%d albums, eta %s", done, total, remaining.Round(time.Second))
	if failed > 0 {
		msg += fmt.Sprintf(", %d failed", failed)
	}
	return msg
}
