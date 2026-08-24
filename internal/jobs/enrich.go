package jobs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Enrich runs metadata enrichment over every track in the library. The
// handler skips already-enriched tracks and attempts each artist's image at
// most once per run, so provider load stays proportional to what's missing.
//
// Tracks are enriched concurrently with a small worker pool. Tracks of the
// same album are grouped and dispatched together: each worker owns one album
// and enriches its tracks sequentially, which keeps the album-row
// read-modify-write safe (a single writer per album) without an inter-worker
// semaphore. This also prevents a convoy stall where every worker slots onto
// one large album's tracks and blocks behind each other — when the cursor
// lands on a big album, the other workers still pick up other albums.
func (r *Runners) Enrich() Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Enrichment == nil {
			return nil
		}

		tracks, err := r.deps.Store.ListTracksWithQuality(ctx)
		if err != nil {
			return err
		}
		total := len(tracks)
		if total == 0 {
			return nil
		}

		r.deps.Enrichment.ResetBulk()
		r.ResetEnrichActivity()

		const workers = 4
		sem := make(chan struct{}, workers)
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			failed   int
			timeouts int
			done     atomic.Int64
		)
		for _, g := range groupTracksByAlbum(tracks) {
			if ctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(g albumTrackGroup) {
				defer func() { <-sem; wg.Done() }()
				for _, t := range g.Tracks {
					if ctx.Err() != nil {
						return
					}
					start := time.Now()
					r.deps.Log.Debug("enrich track start", "track_id", t.ID, "album_id", t.AlbumID, "component", "jobs")
					err := r.deps.Enrichment.EnrichLibraryTrack(ctx, t.ID)
					durMs := time.Since(start).Milliseconds()
					outcome := enrichOutcomeFor(err)
					switch {
					case err != nil && ctx.Err() != nil:
						// Job cancelled mid-track (provider call aborted). Record the
						// interrupt so the in-flight track is visible, then stop.
						outcome = EnrichOutcomeCancelled
					case err != nil:
						mu.Lock()
						if outcome == EnrichOutcomeTimeout {
							timeouts++
						} else {
							failed++
						}
						mu.Unlock()
						r.deps.Log.Warn("enrich track failed", "track_id", t.ID, "album_id", t.AlbumID, "duration_ms", durMs, "outcome", outcome, "error", err, "component", "jobs")
					default:
						r.deps.Log.Debug("enrich track done", "track_id", t.ID, "album_id", t.AlbumID, "duration_ms", durMs, "component", "jobs")
					}
					r.recordEnrichActivity(EnrichActivity{
						At:         time.Now().UTC(),
						TrackID:    t.ID,
						AlbumID:    t.AlbumID,
						Title:      t.Title,
						Outcome:    outcome,
						DurationMs: durMs,
					})
					if ctx.Err() != nil {
						return
					}
					n := done.Add(1)
					report(Report{Done: int(n), Total: total, Message: t.Title})
				}
			}(g)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}

		// Surface the outcome in the final progress message (shown once the job
		// completes). Tracks already fully enriched are skipped inside the handler;
		// timeouts are reported separately from hard failures.
		report(Report{
			Done:    total,
			Total:   total,
			Message: fmt.Sprintf("processed %d tracks, %d errors, %d timeouts", total, failed, timeouts),
		})
		return nil
	}
}
