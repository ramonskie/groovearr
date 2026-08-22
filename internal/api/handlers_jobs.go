package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// ─── Background jobs ────────────────────────────────────────────────

// handleGetJob returns the current (or last) background job, or null when none
// has run yet. Falls back to the persisted snapshot restored at boot so an
// interrupted job stays visible after a restart.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.Current()
	if job == nil {
		job = s.bootJob
	}
	writeJSON(w, http.StatusOK, job)
}

// handleJobScan starts a background library scan.
func (s *Server) handleJobScan(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "scan", s.scanRunner)
}

// handleJobEnrich starts a background metadata enrichment of the library.
func (s *Server) handleJobEnrich(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "enrich", s.enrichRunner)
}

// handleJobDuplicates starts a background duplicate-artist scan.
func (s *Server) handleJobDuplicates(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "duplicates", s.duplicatesRunner)
}

// duplicatesRunner scans the library for case-insensitive duplicate artists and
// resolves each group's canonical provider spelling, persisting results to the
// duplicate_scan cache. Lookups run sequentially so MusicBrainz's 1 req/sec
// rate limit paces them naturally — no timeouts, no bursting. A cancelled or
// failed run leaves whatever groups were already resolved in the cache.
func (s *Server) duplicatesRunner(ctx context.Context, report func(jobs.Report)) error {
	dss, ok := s.store.(duplicateScanStore)
	if !ok {
		return nil
	}

	byLower := map[string][]domain.Artist{}
	for off := 0; ; off += 200 {
		artists, err := s.store.ListArtists(ctx, off, 200)
		if err != nil {
			return err
		}
		if len(artists) == 0 {
			break
		}
		for _, a := range artists {
			key := strings.ToLower(a.Name)
			byLower[key] = append(byLower[key], a)
		}
	}

	type groupTarget struct {
		key     string
		repName string
	}
	var targets []groupTarget
	for key, list := range byLower {
		if len(list) < 2 {
			continue
		}
		targets = append(targets, groupTarget{key: key, repName: list[0].Name})
	}

	if err := dss.ClearDuplicateCanonicals(ctx); err != nil {
		return err
	}
	total := len(targets)
	if total == 0 {
		report(jobs.Report{Done: 0, Total: 1, Message: "no duplicate artists found"})
		return nil
	}

	for i, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		canonical, err := s.lookupCanonicalArtist(ctx, t.repName)
		if err != nil {
			// MusicBrainz (the sole canonical-name source) is rate-limited.
			// Pause ctx-aware so it can recover, then retry this group once so
			// every group still gets a genuine lookup attempt.
			if errors.Is(err, metadata.ErrRateLimited) {
				s.log.Warn("duplicates scan: canonical lookup rate limited, pausing then retrying",
					"group", t.key, "error", err, "component", "jobs")
				if !s.pauseForRateLimit(ctx, err) {
					return ctx.Err()
				}
				canonical, err = s.lookupCanonicalArtist(ctx, t.repName)
				if err != nil && errors.Is(err, metadata.ErrRateLimited) {
					s.log.Warn("duplicates scan: canonical lookup still rate limited after pause",
						"group", t.key, "error", err, "component", "jobs")
					canonical = ""
				}
			}
		}
		if err := dss.UpsertDuplicateCanonical(ctx, t.key, canonical); err != nil {
			s.log.Warn("duplicates scan: persist failed", "group", t.key, "error", err, "component", "jobs")
		}
		report(jobs.Report{Done: i + 1, Total: total, Message: t.key})
	}
	report(jobs.Report{
		Done:    total,
		Total:   total,
		Message: fmt.Sprintf("checked %d duplicate artist groups", total),
	})
	return nil
}

// organizeReport is the persisted result of the last organize job (dry run or
// repair), letting the UI show what would/was moved after the fact.
type organizeReport struct {
	Mode      string          `json:"mode"` // "dry run" | "repair"
	RanAt     time.Time       `json:"ran_at"`
	Summary   organizeSummary `json:"summary"`
	Entries   []organizeEntry `json:"entries"`
	Truncated bool            `json:"truncated"`
}

type organizeSummary struct {
	Moved     int `json:"moved"`
	WouldMove int `json:"would_move"`
	InPlace   int `json:"in_place"`
	Skipped   int `json:"skipped"`
	Errors    int `json:"errors"`
}

type organizeEntry struct {
	TrackID int64  `json:"track_id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Reason  string `json:"reason"`
}

// maxOrganizeReportEntries bounds the persisted entry list so a huge library
// dry-run can't exhaust memory; the summary always keeps the full counts.
const maxOrganizeReportEntries = 20000

// maxOrganizeErrorEntries caps how many error rows are persisted so a run with
// many failing tracks doesn't crowd out the moved/would-move rows the report is
// meant to surface.
const maxOrganizeErrorEntries = 100

// setOrganizeReport stores the report under the server mutex.
func (s *Server) setOrganizeReport(rep *organizeReport) {
	s.organizeMu.Lock()
	defer s.organizeMu.Unlock()
	s.organizeReport = rep
}

// handleOrganizeReport returns the last organize job's report (nil when none
// has run yet).
func (s *Server) handleOrganizeReport(w http.ResponseWriter, r *http.Request) {
	s.organizeMu.Lock()
	defer s.organizeMu.Unlock()
	if s.organizeReport == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, s.organizeReport)
}

// handleJobOrganize starts a background folder-template reorganization of the
// library. With ?dryRun=true it only reports what would move.
func (s *Server) handleJobOrganize(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dryRun") == "true"
	s.startJob(w, "organize", s.organizeRunner(dryRun))
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
	// Wrap the runner to persist the job lifecycle. The running snapshot is
	// written at entry — before the runner runs — so the persist order is
	// deterministic (running → terminal). Persisting it after Start returns
	// would race with fast runners whose terminal write lands first, leaving
	// job.json stuck at "running" for a finished job.
	//
	// The terminal progress is read from the manager's snapshot (Current)
	// rather than captured in the report closure: enrichRunner reports from
	// multiple worker goroutines, so capturing into a shared variable would be
	// a data race.
	wrapped := func(ctx context.Context, report func(jobs.Report)) error {
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

	job, err := s.jobs.Start(jobType, wrapped)
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

// organizeDivergencePossible reports whether a killed organize run left a
// moved-but-DB-stale track path behind. The trigger is the in-memory boot
// snapshot: job.json is overwritten by the current job's own running write the
// moment it starts, so it can never still describe the interrupted organize by
// the time the scan runs. restoreInterruptedJob marks a killed organize as
// interrupted at boot and keeps it in bootJob for the process lifetime.
func (s *Server) organizeDivergencePossible() bool {
	if s.divergenceRepairDone {
		return false
	}
	last := s.bootJob
	return last != nil && last.Type == "organize" && last.State == jobs.StateInterrupted
}

// markDivergenceRepairDone disarms the reconcile after one completed full pass.
// A single pass over the whole track list resolves (or fails to find) every
// diverged path, so later scans/organizes in this process don't keep paying
// for the one interrupted organize.
func (s *Server) markDivergenceRepairDone() {
	s.divergenceRepairDone = true
}

// maxRateLimitPause caps how long the duplicates scan sleeps after a
// rate-limit response. MusicBrainz's 1 req/s limit self-corrects in a couple
// of seconds; a larger Retry-After shouldn't stall the whole scan. Var so
// tests can shrink it.
var maxRateLimitPause = 10 * time.Second

// defaultRateLimitPause is how long to sleep after a rate-limit response
// before retrying. Var so tests can shrink it.
var defaultRateLimitPause = 5 * time.Second

// pauseForRateLimit sleeps after a rate-limit response so the throttled API
// can recover, honoring the request context (job cancellation aborts the
// pause). When the error carries a server-requested Retry-After, the pause
// honors it (bounded by maxRateLimitPause); otherwise the default is used.
// Returns false when the context was cancelled.
func (s *Server) pauseForRateLimit(ctx context.Context, err error) bool {
	d := defaultRateLimitPause
	var rl *metadata.RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter > d {
		d = rl.RetryAfter
	}
	if d > maxRateLimitPause {
		d = maxRateLimitPause
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// reconcileDivergedPaths runs the moved-but-DB-stale repair when a killed
// organize left the trigger armed. Returns the number repaired.
func (s *Server) reconcileDivergedPaths(ctx context.Context, report func(jobs.Report), cfg config.Config) (int, error) {
	root := cfg.Library.LibraryPath
	if root == "" {
		root = config.DefaultLibraryPath
	}
	org := library.NewOrganizer(cfg.Library.FolderTemplate, cfg.Library.CompilationTemplate, root, s.store, s.log)
	report(jobs.Report{Message: "Reconciling moved tracks…"})
	n, err := org.RepairDivergedPaths(ctx, nil)
	if err == nil {
		s.markDivergenceRepairDone()
		if n > 0 {
			s.log.Info("reconciled diverged track paths", "count", n, "component", "jobs")
		}
	}
	return n, err
}

// scanRunner scans all configured library paths, reporting per-file progress.
func (s *Server) scanRunner(ctx context.Context, report func(jobs.Report)) error {
	cfg := s.cfg.Get()
	paths := []string{cfg.Library.LibraryPath}
	if paths[0] == "" {
		paths[0] = config.DefaultLibraryPath
	}

	// Reconcile DB paths an interrupted organize run left behind (file moved,
	// DB not updated). Only runs when a killed organize armed the trigger, so a
	// normal scan pays nothing. Run before the walk so moved files aren't
	// re-imported as duplicate tracks.
	if s.organizeDivergencePossible() {
		if n, err := s.reconcileDivergedPaths(ctx, report, cfg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Warn("reconcile diverged paths failed", "error", err, "component", "jobs")
		} else if n > 0 {
			report(jobs.Report{Message: fmt.Sprintf("Reconciled %d moved track paths", n)})
		}
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

// albumTrackGroup is one album's tracks, dispatched as a unit so concurrent
// workers always enrich different albums (see enrichRunner).
type albumTrackGroup struct {
	AlbumID int64
	Tracks  []*domain.Track
}

// groupTracksByAlbum partitions tracks into per-album groups preserving
// first-seen (rowid) order. Same-album tracks stay together so a single
// worker can own the album without an inter-worker semaphore.
func groupTracksByAlbum(tracks []domain.Track) []albumTrackGroup {
	var order []int64
	byAlbum := make(map[int64][]*domain.Track)
	for i := range tracks {
		t := &tracks[i]
		if _, ok := byAlbum[t.AlbumID]; !ok {
			order = append(order, t.AlbumID)
		}
		byAlbum[t.AlbumID] = append(byAlbum[t.AlbumID], t)
	}
	groups := make([]albumTrackGroup, 0, len(order))
	for _, id := range order {
		groups = append(groups, albumTrackGroup{AlbumID: id, Tracks: byAlbum[id]})
	}
	return groups
}

// enrichOutcome describes how a single track finished in the bulk enrichment
// job. Timeouts are reported separately so a slow provider pass is visible
// instead of being lumped into generic failures.
type enrichOutcome string

const (
	enrichOutcomeCompleted enrichOutcome = "completed"
	enrichOutcomeFailed    enrichOutcome = "failed"
	enrichOutcomeTimeout   enrichOutcome = "timeout"
	enrichOutcomeCancelled enrichOutcome = "cancelled"
)

// enrichActivity is one per-track event from the enrichment job, kept in a
// rolling buffer so the API/UI can show what the job is doing while it runs.
type enrichActivity struct {
	At         time.Time     `json:"at"`
	TrackID    int64         `json:"track_id"`
	AlbumID    int64         `json:"album_id"`
	Title      string        `json:"title"`
	Outcome    enrichOutcome `json:"outcome"`
	DurationMs int64         `json:"duration_ms"`
}

// enrichActivityMax bounds the rolling buffer so a 33k-track job can't grow it
// without limit.
const enrichActivityMax = 500

func (s *Server) resetEnrichActivity() {
	s.enrichMu.Lock()
	defer s.enrichMu.Unlock()
	s.enrichActivity = s.enrichActivity[:0]
}

func (s *Server) recordEnrichActivity(a enrichActivity) {
	s.enrichMu.Lock()
	defer s.enrichMu.Unlock()
	if len(s.enrichActivity) >= enrichActivityMax {
		s.enrichActivity = append(s.enrichActivity[:0], s.enrichActivity[1:]...)
	}
	s.enrichActivity = append(s.enrichActivity, a)
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
	s.enrichMu.Lock()
	activity := make([]enrichActivity, len(s.enrichActivity))
	copy(activity, s.enrichActivity)
	s.enrichMu.Unlock()
	if job == nil || job.Type != "enrich" || job.StartedAt == nil {
		activity = []enrichActivity{}
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

// enrichOutcomeFor maps a per-track enrichment error to the activity outcome.
// Deadline-exceeded tracks (per-track provider timeout) are reported as
// timeouts rather than generic failures.
func enrichOutcomeFor(err error) enrichOutcome {
	switch {
	case err == nil:
		return enrichOutcomeCompleted
	case errors.Is(err, context.DeadlineExceeded):
		return enrichOutcomeTimeout
	default:
		return enrichOutcomeFailed
	}
}

// enrichRunner runs metadata enrichment over every track in the library. The
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
	s.resetEnrichActivity()

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
				s.log.Debug("enrich track start", "track_id", t.ID, "album_id", t.AlbumID, "component", "jobs")
				err := s.enrichmentHandler.EnrichLibraryTrack(ctx, t.ID)
				durMs := time.Since(start).Milliseconds()
				outcome := enrichOutcomeFor(err)
				switch {
				case err != nil && ctx.Err() != nil:
					// Job cancelled mid-track (provider call aborted). Record the
					// interrupt so the in-flight track is visible, then stop.
					outcome = enrichOutcomeCancelled
				case err != nil:
					mu.Lock()
					if outcome == enrichOutcomeTimeout {
						timeouts++
					} else {
						failed++
					}
					mu.Unlock()
					s.log.Warn("enrich track failed", "track_id", t.ID, "album_id", t.AlbumID, "duration_ms", durMs, "outcome", outcome, "error", err, "component", "jobs")
				default:
					s.log.Debug("enrich track done", "track_id", t.ID, "album_id", t.AlbumID, "duration_ms", durMs, "component", "jobs")
				}
				s.recordEnrichActivity(enrichActivity{
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
				report(jobs.Report{Done: int(n), Total: total, Message: t.Title})
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
	report(jobs.Report{
		Done:    total,
		Total:   total,
		Message: fmt.Sprintf("processed %d tracks, %d errors, %d timeouts", total, failed, timeouts),
	})
	return nil
}

// organizeRunner moves library tracks into the folder-template layout. When
// dryRun is true it only counts what would move. Sequential: tracks of the
// same album move into the same target dirs, so parallel workers could race on
// the filesystem.
func (s *Server) organizeRunner(dryRun bool) jobs.Runner {
	return func(ctx context.Context, report func(jobs.Report)) error {
		cfg := s.cfg.Get()
		root := cfg.Library.LibraryPath
		if root == "" {
			root = config.DefaultLibraryPath
		}
		org := library.NewOrganizer(cfg.Library.FolderTemplate, cfg.Library.CompilationTemplate, root, s.store, s.log)

		// Heal a killed organize's moved-but-DB-stale paths before re-running,
		// so tracks orphaned by the kill aren't re-imported as duplicates and
		// don't show up as "target exists" skips below. Repair mutates the DB,
		// so it runs only in repair mode, never dry-run.
		if !dryRun && s.organizeDivergencePossible() {
			if n, err := s.reconcileDivergedPaths(ctx, report, cfg); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s.log.Warn("reconcile diverged paths failed", "error", err, "component", "jobs")
			} else if n > 0 {
				report(jobs.Report{Message: fmt.Sprintf("Reconciled %d moved track paths", n)})
			}
		}

		// Pre-count for progress estimation.
		tracks, err := s.store.ListTracksWithQuality(ctx)
		if err != nil {
			return err
		}
		total := len(tracks)
		if total == 0 {
			// Persist an empty report so the UI doesn't keep showing the
			// previous run's stale result.
			rep := &organizeReport{Mode: "repair", RanAt: time.Now().UTC()}
			if dryRun {
				rep.Mode = "dry run"
			}
			s.setOrganizeReport(rep)
			return nil
		}

		var (
			done, moved, wouldMove, inPlace, skipped, failed, errorEntries int
		)
		report(jobs.Report{Message: "Enumerating artists…"})

		rep := &organizeReport{
			Mode:  "repair",
			RanAt: time.Now().UTC(),
		}
		if dryRun {
			rep.Mode = "dry run"
		}
		addEntry := func(trackID int64, from, to, reason string) {
			if len(rep.Entries) < maxOrganizeReportEntries {
				rep.Entries = append(rep.Entries, organizeEntry{TrackID: trackID, From: from, To: to, Reason: reason})
			} else {
				rep.Truncated = true
			}
		}
		// Persist whatever was computed, even on a mid-run failure, so the UI
		// never shows a stale previous report.
		setReport := func() {
			rep.Summary = organizeSummary{Moved: moved, WouldMove: wouldMove, InPlace: inPlace, Skipped: skipped, Errors: failed}
			s.setOrganizeReport(rep)
		}
		for off := 0; ; off += 200 {
			artists, err := s.store.ListArtists(ctx, off, 200)
			if err != nil {
				setReport()
				return err
			}
			if len(artists) == 0 {
				break
			}
			for _, a := range artists {
				if ctx.Err() != nil {
					setReport()
					return ctx.Err()
				}
				albums, err := s.store.GetAlbumsByArtist(ctx, a.ID)
				if err != nil {
					failed++
					s.log.Warn("organize: list albums failed", "artist_id", a.ID, "error", err, "component", "jobs")
					continue
				}
				for _, al := range albums {
					if ctx.Err() != nil {
						setReport()
						return ctx.Err()
					}
					ts, err := s.store.GetTracksByAlbum(ctx, al.ID)
					if err != nil {
						failed++
						s.log.Warn("organize: list tracks failed", "album_id", al.ID, "error", err, "component", "jobs")
						continue
					}
					for i := range ts {
						res, err := org.Organize(ctx, &ts[i], a.Name, al.Title, al.Year, string(al.AlbumType), dryRun)
						if err != nil {
							failed++
							if errorEntries < maxOrganizeErrorEntries {
								errorEntries++
								addEntry(ts[i].ID, ts[i].FilePath, err.Error(), "errors")
							}
							s.log.Warn("organize: track failed", "track_id", ts[i].ID, "error", err, "component", "jobs")
							continue
						}
						done++
						switch {
						case res.Moved:
							moved++
							addEntry(ts[i].ID, res.From, res.To, "moved")
						case res.WouldMove:
							wouldMove++
							addEntry(ts[i].ID, res.From, res.To, "would move")
						case res.Skipped == "in place":
							inPlace++
						case res.Skipped != "":
							skipped++
							addEntry(ts[i].ID, res.From, res.To, res.Skipped)
						}
						report(jobs.Report{Done: done, Total: total, Message: ts[i].Title})
					}
				}
			}
		}
		if err := ctx.Err(); err != nil {
			setReport()
			return err
		}

		setReport()

		mode := "repair"
		verb := "moved"
		if dryRun {
			mode = "dry run"
			verb = "would move"
		}
		report(jobs.Report{
			Done:    done,
			Total:   total,
			Message: fmt.Sprintf("organize (%s): %d %s, %d would-move, %d in place, %d skipped, %d errors", mode, moved, verb, wouldMove, inPlace, skipped, failed),
		})
		return nil
	}
}

// organizeArtistRunner moves one artist's tracks into the configured folder
// layout. Started automatically after an artist merge: the merged tracks still
// sit under the removed artist's folders, and a canonical rename may have left
// the keeper's own folder name stale, so every keeper track is re-validated and
// moved when out of place. In-place tracks are no-ops.
func (s *Server) organizeArtistRunner(artistID int64) jobs.Runner {
	return func(ctx context.Context, report func(jobs.Report)) error {
		cfg := s.cfg.Get()
		root := cfg.Library.LibraryPath
		if root == "" {
			root = config.DefaultLibraryPath
		}
		// The report is scoped to one artist; use a distinct mode so the UI
		// doesn't read the outcome as a full-library organize run.
		rep := &organizeReport{Mode: "repair (artist)", RanAt: time.Now().UTC()}
		var (
			done, moved, inPlace, skipped, failed, errorEntries int
		)
		addEntry := func(trackID int64, from, to, reason string) {
			if len(rep.Entries) < maxOrganizeReportEntries {
				rep.Entries = append(rep.Entries, organizeEntry{TrackID: trackID, From: from, To: to, Reason: reason})
			} else {
				rep.Truncated = true
			}
		}
		// Persist whatever was computed, even on a failure, so the UI never
		// shows a stale report from a previous run.
		setReport := func() {
			rep.Summary = organizeSummary{Moved: moved, InPlace: inPlace, Skipped: skipped, Errors: failed}
			s.setOrganizeReport(rep)
		}
		artist, err := s.store.GetArtist(ctx, artistID)
		if err != nil || artist == nil {
			setReport()
			return err
		}
		tracks, err := s.store.GetTracksByArtist(ctx, artistID)
		if err != nil {
			setReport()
			return err
		}
		total := len(tracks)
		if total == 0 {
			setReport()
			return nil
		}
		org := library.NewOrganizer(cfg.Library.FolderTemplate, cfg.Library.CompilationTemplate, root, s.store, s.log)
		for i := range tracks {
			if ctx.Err() != nil {
				setReport()
				return ctx.Err()
			}
			album, err := s.store.GetAlbum(ctx, tracks[i].AlbumID)
			if err != nil || album == nil {
				failed++
				if errorEntries < maxOrganizeErrorEntries {
					errorEntries++
					addEntry(tracks[i].ID, tracks[i].FilePath, "album lookup failed", "errors")
				}
				s.log.Warn("merge organize: album lookup failed", "track_id", tracks[i].ID, "error", err, "component", "jobs")
				done++
				report(jobs.Report{Done: done, Total: total, Message: tracks[i].Title})
				continue
			}
			res, err := org.Organize(ctx, &tracks[i], artist.Name, album.Title, album.Year, string(album.AlbumType), false)
			if err != nil {
				failed++
				if errorEntries < maxOrganizeErrorEntries {
					errorEntries++
					addEntry(tracks[i].ID, tracks[i].FilePath, err.Error(), "errors")
				}
				s.log.Warn("merge organize: track failed", "track_id", tracks[i].ID, "error", err, "component", "jobs")
			} else {
				switch {
				case res.Moved:
					moved++
					addEntry(tracks[i].ID, res.From, res.To, "moved")
				case res.Skipped == "in place":
					inPlace++
				case res.Skipped != "":
					skipped++
					addEntry(tracks[i].ID, res.From, res.To, res.Skipped)
				}
			}
			done++
			report(jobs.Report{Done: done, Total: total, Message: tracks[i].Title})
		}
		setReport()
		report(jobs.Report{
			Done:    total,
			Total:   total,
			Message: fmt.Sprintf("moved %d tracks for %s, %d in place, %d skipped, %d errors", moved, artist.Name, inPlace, skipped, failed),
		})
		return nil
	}
}
