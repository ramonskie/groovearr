package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// RunnerDeps carries the shared dependencies every background job needs.
type RunnerDeps struct {
	Log    *slog.Logger
	Store  library.Store
	Config func() config.Config
	// Scanner is the library scanner (may be nil for jobs that don't scan).
	Scanner *library.Scanner
	// Enrichment is the metadata enrichment handler (may be nil).
	Enrichment *download.MetadataEnrichmentHandler
	// Metadata is the metadata provider registry used for canonical artist
	// lookups in the duplicates job.
	Metadata *metadata.Registry
}

// Runners implements every background job body. One instance is constructed
// per server with the shared dependencies; each job is a method returning a
// Runner, so the api layer stays a thin HTTP wrapper around job logic.
type Runners struct {
	deps RunnerDeps

	// organizeReport is the last organize run's report (repair or dry run),
	// kept under a mutex so the HTTP handler can read it while a job runs.
	organizeMu     sync.Mutex
	organizeReport *OrganizeReport

	// enrichActivity is a rolling buffer of per-track enrichment events so the
	// UI can watch a running job. Bounded by EnrichActivityMax.
	enrichMu       sync.Mutex
	enrichActivity []EnrichActivity

	// divergenceRepairDone disarms the killed-organize reconcile after one
	// completed full pass (see organizeDivergencePossible).
	divergenceRepairDone bool
	// bootJob is the persisted job snapshot restored at startup.
	bootJob *Job
}

// NewRunners constructs the job runner set from shared dependencies.
func NewRunners(deps RunnerDeps) *Runners {
	return &Runners{deps: deps}
}

// Logger returns the shared logger.
func (r *Runners) Logger() *slog.Logger { return r.deps.Log }

// SetBootJob records the persisted job snapshot restored at startup. A job
// left in "running" (process died mid-run) is marked interrupted so the UI
// shows what happened after a restart.
func (r *Runners) SetBootJob(j *Job) { r.bootJob = j }

// BootJob returns the restored startup snapshot (may be nil).
func (r *Runners) BootJob() *Job { return r.bootJob }

// OrganizeReport returns the last organize report under the mutex, or nil.
func (r *Runners) OrganizeReport() *OrganizeReport {
	r.organizeMu.Lock()
	defer r.organizeMu.Unlock()
	return r.organizeReport
}

// EnrichActivity returns a copy of the rolling activity buffer.
func (r *Runners) EnrichActivity() []EnrichActivity {
	r.enrichMu.Lock()
	defer r.enrichMu.Unlock()
	out := make([]EnrichActivity, len(r.enrichActivity))
	copy(out, r.enrichActivity)
	return out
}

// setOrganizeReport stores the report under the mutex.
func (r *Runners) setOrganizeReport(rep *OrganizeReport) {
	r.organizeMu.Lock()
	defer r.organizeMu.Unlock()
	r.organizeReport = rep
}

// SetOrganizeReport stores the last organize report (exported for tests and
// callers that seed a report without running the job).
func (r *Runners) SetOrganizeReport(rep *OrganizeReport) {
	r.setOrganizeReport(rep)
}

// ResetEnrichActivity clears the rolling buffer.
func (r *Runners) ResetEnrichActivity() {
	r.enrichMu.Lock()
	defer r.enrichMu.Unlock()
	r.enrichActivity = r.enrichActivity[:0]
}

// recordEnrichActivity appends a per-track event, evicting the oldest entry
// once the buffer reaches EnrichActivityMax.
func (r *Runners) recordEnrichActivity(a EnrichActivity) {
	r.enrichMu.Lock()
	defer r.enrichMu.Unlock()
	if len(r.enrichActivity) >= EnrichActivityMax {
		r.enrichActivity = append(r.enrichActivity[:0], r.enrichActivity[1:]...)
	}
	r.enrichActivity = append(r.enrichActivity, a)
}

// RecordEnrichActivity appends a per-track enrichment event to the rolling
// buffer (exported for tests and the enrichment job).
func (r *Runners) RecordEnrichActivity(a EnrichActivity) {
	r.recordEnrichActivity(a)
}

// OrganizeDivergencePossible reports whether a killed organize run left a
// moved-but-DB-stale track path behind. The trigger is the in-memory boot
// snapshot: job.json is overwritten by the current job's own running write the
// moment it starts, so it can never still describe the interrupted organize by
// the time the scan runs.
func (r *Runners) OrganizeDivergencePossible() bool {
	if r.divergenceRepairDone {
		return false
	}
	last := r.bootJob
	return last != nil && last.Type == "organize" && last.State == StateInterrupted
}

// MarkDivergenceRepairDone disarms the reconcile after one completed full pass.
func (r *Runners) MarkDivergenceRepairDone() {
	r.divergenceRepairDone = true
}

// reconcileDivergedPaths runs the moved-but-DB-stale repair when a killed
// organize left the trigger armed. Returns the number repaired.
func (r *Runners) reconcileDivergedPaths(ctx context.Context, report func(Report), cfg config.Config) (int, error) {
	root := cfg.Library.LibraryPath
	if root == "" {
		root = config.DefaultLibraryPath
	}
	org := library.NewOrganizer(cfg.Library.FolderTemplate, cfg.Library.CompilationTemplate, root, r.deps.Store, r.deps.Log)
	report(Report{Message: "Reconciling moved tracks…"})
	n, err := org.RepairDivergedPaths(ctx, nil)
	if err == nil {
		r.MarkDivergenceRepairDone()
		if n > 0 {
			r.deps.Log.Info("reconciled diverged track paths", "count", n, "component", "jobs")
		}
	}
	return n, err
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
func (r *Runners) pauseForRateLimit(ctx context.Context, err error) bool {
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
