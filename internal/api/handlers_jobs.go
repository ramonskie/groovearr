package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/library"
)

// ─── Background jobs ────────────────────────────────────────────────

// handleGetJob returns the current (or last) background job, or null when none
// has run yet.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.jobs.Current())
}

// handleJobScan starts a background library scan.
func (s *Server) handleJobScan(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "scan", s.scanRunner)
}

// handleJobEnrich starts a background metadata enrichment of the library.
func (s *Server) handleJobEnrich(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "enrich", s.enrichRunner)
}

// handleJobCancel requests cancellation of the running job.
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	s.jobs.Cancel()
	writeJSON(w, http.StatusOK, s.jobs.Current())
}

// startJob starts a job, returning {job, started}. When a job is already
// running the current job is returned with started=false (idempotent) so the
// client can tell "started by this request" from "already running".
func (s *Server) startJob(w http.ResponseWriter, jobType string, runner jobs.Runner) {
	job, err := s.jobs.Start(jobType, runner)
	if err != nil {
		if errors.Is(err, jobs.ErrBusy) {
			writeJSON(w, http.StatusOK, map[string]any{"job": s.jobs.Current(), "started": false})
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job, "started": true})
}

// scanRunner scans all configured library paths, reporting per-file progress.
func (s *Server) scanRunner(ctx context.Context, report func(jobs.Report)) error {
	cfg := s.cfg.Get()
	paths := []string{cfg.Library.LibraryPath}
	if paths[0] == "" {
		paths[0] = "./music"
	}

	// Pre-count for progress estimation. Cancellable; reports a message so the
	// UI isn't stuck at 0% during the counting walk.
	report(jobs.Report{Message: "Counting audio files…"})
	total := 0
	for _, p := range paths {
		n, err := library.CountAudioFiles(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Warn("count audio files failed", "path", p, "error", err, "component", "jobs")
			continue
		}
		total += n
	}
	if total == 0 {
		total = 1
	}

	done := 0
	var scanned, imported, skipped, scanErrors int
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		stats, err := s.scanner.ScanPathWithProgress(ctx, p, func(path string) {
			done++
			report(jobs.Report{Done: done, Total: total, Message: filepath.Base(path)})
		})
		scanned += stats.Scanned
		imported += stats.Imported
		skipped += stats.Skipped
		scanErrors += stats.Errors
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Non-cancellation scan errors are logged and the next path is
			// still attempted — mirrors the old synchronous scan handler.
			s.log.Error("scan path failed", "path", p, "error", err, "component", "jobs")
		}
	}

	// Surface the outcome counts in the final progress message so the UI can
	// show them once the job completes.
	report(jobs.Report{
		Done:    done,
		Total:   total,
		Message: fmt.Sprintf("scanned %d, imported %d, skipped %d, errors %d", scanned, imported, skipped, scanErrors),
	})
	return nil
}

// enrichRunner runs metadata enrichment over every track in the library. The
// handler skips already-enriched tracks and attempts each artist's image at
// most once per run, so provider load stays proportional to what's missing.
//
// Tracks are enriched concurrently with a small worker pool: each provider
// client rate-limits its own shared transport, so concurrency saturates those
// limits instead of exceeding them — a strictly sequential run over tens of
// thousands of tracks would otherwise take hours.
func (s *Server) enrichRunner(ctx context.Context, report func(jobs.Report)) error {
	if s.enrichmentHandler == nil {
		return nil
	}

	tracks, err := s.store.ListTracksWithQuality(ctx)
	if err != nil {
		return err
	}
	total := len(tracks)
	if total == 0 {
		return nil
	}

	s.enrichmentHandler.ResetBulk()

	const workers = 4
	sem := make(chan struct{}, workers)
	// Tracks of the same album are enriched sequentially: each enrichTrack does
	// a read-modify-write on the album row, so concurrent workers would clobber
	// each other's field updates. Different albums still run concurrently.
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		albumSemMu sync.Mutex
		albumSems  = make(map[int64]chan struct{})
		failed     int
		done       atomic.Int64
	)
	albumSem := func(albumID int64) chan struct{} {
		albumSemMu.Lock()
		defer albumSemMu.Unlock()
		s, ok := albumSems[albumID]
		if !ok {
			s = make(chan struct{}, 1)
			albumSems[albumID] = s
		}
		return s
	}
	for i := range tracks {
		if ctx.Err() != nil {
			break
		}
		t := &tracks[i]
		sem <- struct{}{}
		wg.Add(1)
		go func(t *domain.Track) {
			defer func() { <-sem; wg.Done() }()
			as := albumSem(t.AlbumID)
			select {
			case as <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-as }()
			if err := s.enrichmentHandler.EnrichLibraryTrack(ctx, t.ID); err != nil {
				if ctx.Err() != nil {
					// Cancelled mid-track (provider call aborted) — not a real
					// failure; the job reports cancelled after wg.Wait.
					return
				}
				mu.Lock()
				failed++
				mu.Unlock()
				s.log.Warn("enrich track failed", "track_id", t.ID, "error", err, "component", "jobs")
			}
			n := done.Add(1)
			report(jobs.Report{Done: int(n), Total: total, Message: t.Title})
		}(t)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	// Surface the outcome in the final progress message (shown once the job
	// completes). Tracks already fully enriched are skipped inside the handler.
	report(jobs.Report{
		Done:    total,
		Total:   total,
		Message: fmt.Sprintf("processed %d tracks, %d errors", total, failed),
	})
	return nil
}
