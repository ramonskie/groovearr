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

// maxSearchPassesPerArtist is the hard upper bound on the per-artist drain
// loop — a safety net for a discography so deep it would otherwise need more
// than 250 albums of passes. The loop normally stops after one full rotation
// over the wanted set (see drainArtist).
const maxSearchPassesPerArtist = 50

// searchTally aggregates the outcome of one or more SearchMissing passes across
// one or more artists.
type searchTally struct {
	queued          int
	skipped         int
	noSource        int
	errors          int
	processedAlbums int
	// leftover is the number of wanted albums still unprocessed after a drain
	// stopped at its pass bound (a truncation, not data loss — they stay wanted
	// and the next run resumes). truncations counts the artists affected.
	leftover    int
	truncations int
}

// add accumulates a single pass result. A nil result is ignored.
func (t *searchTally) add(res *tracking.SearchResult) {
	if res == nil {
		return
	}
	t.queued += res.Queued
	t.skipped += res.Skipped
	t.noSource += res.NoSource
	t.errors += res.Errors
	t.processedAlbums += res.Processed
}

// drainArtist runs SearchMissing repeatedly for one artist until its wanted
// backlog is drained, or a bound stops it: one full rotation over the wanted
// set, the safety pass cap, a provider cooldown, or a cancelled context.
//
// The drain is bounded to a single cycle. Each pass stamps the albums it
// processed (last_searched_at) and the next pass selects the next
// never-searched albums first, so ceil(wanted / batch) passes cover the initial
// wanted set exactly once. That bound matters: an album that cannot be queued
// (nothing found, no acquisition path, already in the pipeline) never leaves the
// wanted set, so Remaining never reaches 0 and an unbounded loop would re-search
// the same albums until the safety cap. Any album left unprocessed at the bound
// stays wanted and is picked up by the next run.
//
// A SearchMissing error is returned wrapped; the caller decides whether that
// fails the whole job or just this artist (a cancelled parent consistently
// returns ctx.Err()).
func (r *Runners) drainArtist(ctx context.Context, artist domain.TrackedArtist, tally *searchTally) error {
	startProcessed := tally.processedAlbums
	maxPasses := maxSearchPassesPerArtist
	var wantedAtStart int
	for pass := 0; ; pass++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if pass >= maxPasses {
			break
		}
		sctx, cancel := context.WithTimeout(ctx, trackedArtistRefreshTimeout)
		res, err := r.deps.Tracking.SearchMissing(sctx, artist.ID)
		cancel()
		if err != nil {
			return fmt.Errorf("search missing artist %d: %w", artist.ID, err)
		}
		if res == nil {
			break
		}
		if pass == 0 {
			wantedAtStart = res.Processed + res.Remaining
			// ceil(wanted / batch): one full rotation over the initial set.
			if res.Processed > 0 {
				if cycle := (wantedAtStart + res.Processed - 1) / res.Processed; cycle >= 1 && cycle < maxPasses {
					maxPasses = cycle
				}
			}
		}
		tally.add(res)
		if res.Remaining == 0 || res.Processed == 0 {
			break
		}
		// A provider parked mid-drain must stop the loop; the next run resumes
		// once the shared bucket clears.
		if r.coolingDown(artist.ProviderName) {
			break
		}
	}
	if leftover := wantedAtStart - (tally.processedAlbums - startProcessed); leftover > 0 {
		tally.leftover += leftover
		tally.truncations++
		r.logWarn("tracked search drain truncated at pass bound",
			"artist_id", artist.ID, "remaining", leftover)
	}
	return nil
}

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
// missing albums through the tracking service, draining the artist's wanted
// backlog in bounded passes. It observes ctx and spawns no goroutines.
//
// Behavior:
//   - A nil Tracking dependency returns a clear error rather than panicking.
//   - The artist is resolved through the TrackedRefresher (never the store) so
//     a cooling-down provider can be skipped before any call.
//   - Each pass gets its own trackedArtistRefreshTimeout budget derived from the
//     job ctx, so a wedged provider search can't pin the job's single slot (and
//     with it shutdown) until the server stops. A parent cancellation returns
//     context.Canceled cleanly; a per-run timeout is returned wrapped so the job
//     is marked failed rather than swallowing the deadline.
//   - Passes repeat until Remaining==0 (bounded by maxSearchPassesPerArtist), so
//     one click clears the whole backlog, not just the first batch.
//   - When any pass reports Errors > 0 the runner returns an error so the job is
//     marked failed; a pass that merely had nothing to do still succeeds.
func (r *Runners) SearchMissingArtist(artistID int64) Runner {
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

		var tally searchTally
		if derr := r.drainArtist(ctx, *artist, &tally); derr != nil {
			// A cancelled parent is a clean stop, not a search failure.
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			return derr
		}
		report(Report{
			Done:  1,
			Total: 1,
			Message: fmt.Sprintf("queued %d, skipped %d, no-source %d, errors %d, remaining %d",
				tally.queued, tally.skipped, tally.noSource, tally.errors, tally.leftover),
		})
		if tally.errors > 0 {
			return fmt.Errorf("search missing for artist %d: %d errors", artistID, tally.errors)
		}
		return nil
	}
}

// SearchMissingAll returns a Runner that searches every tracked artist's
// missing albums, draining each artist's wanted backlog in bounded passes. It
// is the global counterpart of SearchMissingArtist ("Search all missing").
// It observes ctx between artists and passes, and spawns no goroutines.
//
// Behavior:
//   - A nil Tracking dependency returns a clear error rather than panicking.
//   - Artists whose provider is cooling down are skipped without a provider
//     call; the loop also stops an artist mid-drain if its provider parks.
//   - One artist's failure is logged and counted but does not stop the run;
//     when any artist failed the runner returns a summary error so the job is
//     marked failed and the failure is visible.
func (r *Runners) SearchMissingAll() Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Tracking == nil {
			return errors.New("tracking service not available")
		}
		artists, err := r.deps.Tracking.ListTrackedArtists(ctx)
		if err != nil {
			return fmt.Errorf("list tracked artists: %w", err)
		}
		total := len(artists)
		if total == 0 {
			report(Report{Message: "no tracked artists"})
			return nil
		}

		var tally searchTally
		var failed int
		for i, artist := range artists {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if r.coolingDown(artist.ProviderName) {
				report(Report{Done: i + 1, Total: total, Message: fmt.Sprintf("skipped %s (provider cooling down)", artist.Name)})
				continue
			}
			before := tally.queued
			if derr := r.drainArtist(ctx, artist, &tally); derr != nil {
				if cerr := ctx.Err(); cerr != nil {
					return cerr
				}
				failed++
				r.logWarn("tracked artist search failed",
					"artist_id", artist.ID, "provider", artist.ProviderName, "error", derr)
				report(Report{Done: i + 1, Total: total, Message: fmt.Sprintf("failed %s: %v", artist.Name, derr)})
				continue
			}
			report(Report{Done: i + 1, Total: total, Message: fmt.Sprintf("%s: queued %d", artist.Name, tally.queued-before)})
		}

		summary := fmt.Sprintf("searched %d artists: queued %d, skipped %d, no-source %d, errors %d, remaining %d",
			total, tally.queued, tally.skipped, tally.noSource, tally.errors, tally.leftover)
		report(Report{Done: total, Total: total, Message: summary})
		if failed > 0 {
			return fmt.Errorf("tracked search: %d of %d artists failed; %s", failed, total, summary)
		}
		return nil
	}
}

// RefreshAndSearchTracked composes the discography refresh and the full
// search-missing drain into one Runner, so the scheduler can auto-acquire under
// a single Manager slot (single-flight) when tracking.auto_search_missing is
// enabled. A parent cancellation stops the sequence cleanly. A refresh error is
// returned only after the drain has had its chance, so a partial refresh still
// lets already-wanted releases be queued.
func (r *Runners) RefreshAndSearchTracked() Runner {
	refresh := r.RefreshTracked()
	search := r.SearchMissingAll()
	return func(ctx context.Context, report func(Report)) error {
		refreshErr := refresh(ctx, report)
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		searchErr := search(ctx, report)
		return errors.Join(refreshErr, searchErr)
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
