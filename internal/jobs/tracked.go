package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/tracking"
)

// trackedArtistRefreshTimeout bounds a single artist's RefreshArtist call so a
// slow or wedged discovery provider can't pin the job's single slot (and with
// it shutdown) indefinitely. A package var, not a const, so tests in this
// package can shorten it while exercising the timeout path; production never
// overrides it.
var trackedArtistRefreshTimeout = 60 * time.Second

// RefreshTracked returns a Runner that refreshes the discography of every
// tracked artist with AutoRefresh enabled. It runs strictly sequentially and
// observes ctx between artists, so a cancelled job (or server shutdown) stops
// promptly and never blocks the Manager; it spawns no goroutines.
//
// Behavior:
//   - Artists with AutoRefresh == false are ignored (never refreshed).
//   - An artist whose provider is currently cooling down in the shared bucket
//     is skipped without a provider call. RefreshArtist itself also consults
//     and marks that same bucket (AGENTS §8), so this is an additional
//     pre-filter, not a substitute.
//   - Each artist gets its own trackedArtistRefreshTimeout budget derived from
//     the job ctx. A parent cancellation returns context.Canceled cleanly; a
//     per-artist timeout counts as that artist's failure and the run continues.
//   - One artist failure is logged and counted but does not stop the run; when
//     any artist failed the runner returns a summary error ("N of M failed").
//
// A nil Tracking dependency returns a clear error rather than panicking.
func (r *Runners) RefreshTracked() Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Tracking == nil {
			return errors.New("tracking service not available")
		}
		artists, err := r.deps.Tracking.ListTrackedArtists(ctx)
		if err != nil {
			return fmt.Errorf("list tracked artists: %w", err)
		}

		candidates := make([]domain.TrackedArtist, 0, len(artists))
		for _, a := range artists {
			if a.AutoRefresh {
				candidates = append(candidates, a)
			}
		}
		total := len(candidates)
		if total == 0 {
			report(Report{Message: "no auto-refresh artists"})
			return nil
		}

		var refreshed, skipped, failed int
		for i, artist := range candidates {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if r.coolingDown(artist.ProviderName) {
				skipped++
				report(Report{
					Done:    i + 1,
					Total:   total,
					Message: fmt.Sprintf("skipped %s (provider cooling down)", artist.Name),
				})
				continue
			}

			actx, cancel := context.WithTimeout(ctx, trackedArtistRefreshTimeout)
			_, rerr := r.deps.Tracking.RefreshArtist(actx, artist.ID)
			cancel()

			if rerr != nil {
				// A cancelled parent is a clean stop, not an artist failure.
				if cerr := ctx.Err(); cerr != nil {
					return cerr
				}
				failed++
				r.logWarn("tracked artist refresh failed",
					"artist_id", artist.ID, "provider", artist.ProviderName, "error", rerr)
				report(Report{
					Done:    i + 1,
					Total:   total,
					Message: fmt.Sprintf("failed %s: %v", artist.Name, rerr),
				})
				continue
			}

			refreshed++
			report(Report{Done: i + 1, Total: total, Message: artist.Name})
		}

		if failed > 0 {
			return fmt.Errorf("tracked refresh: %d of %d artists failed", failed, total)
		}
		report(Report{
			Done:    total,
			Total:   total,
			Message: fmt.Sprintf("refreshed %d artists, skipped %d cooling down", refreshed, skipped),
		})
		return nil
	}
}

// RefreshTrackedArtist returns a Runner that refreshes exactly one tracked
// artist's discography. It is the single-artist counterpart of RefreshTracked,
// used when the API triggers a refresh for a specific artist. It runs
// sequentially, observes ctx, and spawns no goroutines.
//
// Behavior mirrors RefreshTracked's per-artist body:
//   - A nil Tracking dependency returns a clear error rather than panicking.
//   - The artist is resolved through the TrackedRefresher (never the store) so
//     its ProviderName can be checked against the shared bucket; an artist that
//     no longer exists is a clear error.
//   - A cooling-down provider is skipped without a provider call. RefreshArtist
//     itself also consults and marks that same bucket (AGENTS §8), so this is an
//     additional pre-filter, not a substitute.
//   - The refresh gets its own trackedArtistRefreshTimeout budget derived from
//     the job ctx. A parent cancellation returns context.Canceled cleanly; a
//     per-artist timeout is a failure and is returned wrapped.
//   - Progress is reported on success and the wrapped error is returned on
//     failure.
func (r *Runners) RefreshTrackedArtist(artistID int64) Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Tracking == nil {
			return errors.New("tracking service not available")
		}
		artist, err := r.deps.Tracking.GetTrackedArtist(ctx, artistID)
		if err != nil {
			return fmt.Errorf("get tracked artist %d: %w", artistID, err)
		}
		if artist == nil {
			return fmt.Errorf("tracked artist %d not found", artistID)
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if r.coolingDown(artist.ProviderName) {
			report(Report{Message: fmt.Sprintf("skipped %s (provider cooling down)", artist.Name)})
			return nil
		}

		actx, cancel := context.WithTimeout(ctx, trackedArtistRefreshTimeout)
		_, rerr := r.deps.Tracking.RefreshArtist(actx, artistID)
		cancel()

		if rerr != nil {
			// A cancelled parent is a clean stop, not an artist failure.
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			r.logWarn("tracked artist refresh failed",
				"artist_id", artistID, "provider", artist.ProviderName, "error", rerr)
			return fmt.Errorf("refresh tracked artist %d: %w", artistID, rerr)
		}

		report(Report{Done: 1, Total: 1, Message: artist.Name})
		return nil
	}
}

// SearchMissingArtist returns a Runner that searches for one tracked artist's
// missing albums through the tracking service. It observes ctx and spawns no
// goroutines.
//
// Behavior:
//   - A nil Tracking dependency returns a clear error rather than panicking.
//   - The search gets its own trackedArtistRefreshTimeout budget derived from
//     the job ctx, so a wedged provider search can't pin the job's single slot
//     (and with it shutdown) until the server stops. A parent cancellation
//     returns context.Canceled cleanly; a per-run timeout is returned wrapped
//     so the job is marked failed rather than swallowing the deadline.
//   - The service call is delegated wholesale; SearchMissing owns its own
//     per-album error accounting.
//   - The pass summary (queued/skipped/errors/remaining) is reported.
//   - When SearchResult.Errors > 0 the runner returns an error so the job is
//     marked failed. Choice: a pass where albums could not be queued is a real
//     failure that should be visible, not a silent "queued 0" success. A pass
//     that merely had nothing to do (all skipped) still succeeds.
func (r *Runners) SearchMissingArtist(artistID int64) Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Tracking == nil {
			return errors.New("tracking service not available")
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}

		sctx, cancel := context.WithTimeout(ctx, trackedArtistRefreshTimeout)
		result, err := r.deps.Tracking.SearchMissing(sctx, artistID)
		cancel()

		if err != nil {
			// A cancelled parent is a clean stop, not a search failure.
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			return fmt.Errorf("search missing for artist %d: %w", artistID, err)
		}
		if result == nil {
			result = &tracking.SearchResult{}
		}
		report(Report{
			Done:    1,
			Total:   1,
			Message: fmt.Sprintf("queued %d, skipped %d, errors %d, remaining %d", result.Queued, result.Skipped, result.Errors, result.Remaining),
		})
		if result.Errors > 0 {
			return fmt.Errorf("search missing for artist %d: %d errors", artistID, result.Errors)
		}
		return nil
	}
}

// coolingDown reports whether the shared bucket currently parks name. A nil
// bucket means "nothing is cooling down" (nil-safe, matches AGENTS §8).
func (r *Runners) coolingDown(name string) bool {
	return r.deps.RateLimit != nil && r.deps.RateLimit.CoolingDown(name)
}

// logWarn logs through the shared logger when one is wired: RunnerDeps.Log may
// be nil for tests and constructors that don't need logging.
func (r *Runners) logWarn(msg string, args ...any) {
	if r.deps.Log != nil {
		r.deps.Log.Warn(msg, args...)
	}
}
