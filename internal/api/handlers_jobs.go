package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ramonskie/groovearr/internal/jobs"
)

// ─── Background jobs ────────────────────────────────────────────────
//
// The job BODIES live in internal/jobs (one file per job: scan.go, enrich.go,
// duplicates.go, organize.go). These handlers are thin
// HTTP wrappers: they pick a runner, wrap it with lifecycle persistence, and
// expose the manager + per-job state (organize report, enrich activity).

// handleGetJob returns the current (or last) background job, or null when none
// has run yet. Falls back to the persisted snapshot restored at boot so an
// interrupted job stays visible after a restart.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.Current()
	if job == nil {
		job = s.runners.BootJob()
	}
	writeJSON(w, http.StatusOK, job)
}

// handleJobScan starts a background library scan.
func (s *Server) handleJobScan(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "scan", s.runners.Scan())
}

// handleJobEnrich starts a background metadata enrichment of the library.
func (s *Server) handleJobEnrich(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "enrich", s.runners.Enrich())
}

// handleJobDuplicates starts a background duplicate-artist scan.
func (s *Server) handleJobDuplicates(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "duplicates", s.runners.Duplicates())
}

// handleOrganizeReport returns the last organize job's report (nil when none
// has run yet).
func (s *Server) handleOrganizeReport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runners.OrganizeReport())
}

// handleJobOrganize starts a background folder-template reorganization of the
// library. With ?dryRun=true it only reports what would move.
func (s *Server) handleJobOrganize(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dryRun") == "true"
	s.startJob(w, "organize", s.runners.Organize(dryRun))
}

// handleJobCancel requests cancellation of the running job.
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	s.jobs.Cancel()
	writeJSON(w, http.StatusOK, s.jobs.Current())
}

// startJob starts a job, returning {job, started}. When a job is already
// running the current job is returned with started=false (idempotent) so the
// client can tell "started by this request" from "already running". The job's
// lifecycle is persisted to disk so an interrupted run is visible after a
// restart (see restoreInterruptedJob).
func (s *Server) startJob(w http.ResponseWriter, jobType string, runner jobs.Runner) {
	job, err := s.jobs.Start(jobType, s.runPersistedJob(jobType, runner))
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

// runPersistedJob wraps runner so the job lifecycle is persisted to disk,
// returning a jobs.Runner the Manager can start. The running snapshot is
// written at entry — before the runner runs — so the persist order is
// deterministic (running → terminal). Persisting it after Start returns would
// race with fast runners whose terminal write lands first, leaving job.json
// stuck at "running" for a finished job.
//
// The terminal progress is read from the manager's snapshot (Current) rather
// than captured in a report closure: some runners (e.g. enrich) report from
// multiple worker goroutines, so capturing into a shared variable would be a
// data race.
func (s *Server) runPersistedJob(jobType string, runner jobs.Runner) jobs.Runner {
	return func(ctx context.Context, report func(jobs.Report)) error {
		now := time.Now().UTC()
		s.saveJobState(&jobs.Job{Type: jobType, State: jobs.StateRunning, StartedAt: &now})

		err := runner(ctx, report)

		state := jobs.StateCompleted
		switch {
		case err == nil:
			state = jobs.StateCompleted
		case errors.Is(err, context.Canceled):
			state = jobs.StateCancelled
		default:
			state = jobs.StateFailed
		}
		term := &jobs.Job{Type: jobType, State: state, FinishedAt: &now}
		if cur := s.jobs.Current(); cur != nil {
			term.StartedAt = cur.StartedAt
			term.Message = cur.Message
			term.Done = cur.Done
			term.Total = cur.Total
		} else {
			term.StartedAt = &now
		}
		if state == jobs.StateFailed {
			term.Error = err.Error()
		}
		s.saveJobState(term)
		return err
	}
}

// handleJobActivity returns the current job plus the recent per-track
// enrichment activity so callers can watch the job's internal progress. The
// buffer is populated only by the enrichment job; exposing it under a
// scan/duplicates/organize job would mislead consumers into reading stale
// enrich entries as the current job's progress. Entries are also scoped to the
// current run via StartedAt: the buffer is reset asynchronously inside the
// runner, so a poller in the startup window would otherwise see the previous
// run's tail.
func (s *Server) handleJobActivity(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.Current()
	activity := s.runners.EnrichActivity()
	if job == nil || job.Type != "enrich" || job.StartedAt == nil {
		activity = []jobs.EnrichActivity{}
	} else {
		filtered := activity[:0]
		for _, a := range activity {
			if !a.At.Before(*job.StartedAt) {
				filtered = append(filtered, a)
			}
		}
		activity = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job":      job,
		"activity": activity,
	})
}
