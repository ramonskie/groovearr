package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/tracking"
)

// PlaylistSyncer syncs an imported playlist with its upstream source under a
// per-playlist lock (prevents a job-triggered sync from racing the auto-sync
// worker or the download-missing rebuild). Declared here (not imported from
// playlist) so jobs stays free of a concrete dependency; *playlist.Service
// satisfies it. Returns started=false when a sync for that playlist is
// already in progress.
type PlaylistSyncer interface {
	SyncPlaylistGuarded(ctx context.Context, playlistID int64) (bool, error)
}

// TrackedRefresher lists tracked artists, resolves a single artist, refreshes
// one artist's discography, and queues one artist's missing albums. Declared
// here (not imported from tracking as a concrete type) so jobs stays free of a
// construction dependency; *tracking.Service satisfies it. RefreshArtist
// consults and marks the shared per-provider cooldown bucket itself (AGENTS §8);
// the jobs additionally pre-filter cooling-down providers before spending a
// provider call.
type TrackedRefresher interface {
	ListTrackedArtists(ctx context.Context) ([]domain.TrackedArtist, error)
	// GetTrackedArtist resolves the artist so single-artist jobs can read its
	// ProviderName for the shared cooling-down pre-filter without reaching into
	// the tracking store (AGENTS §3). Returns (nil, nil) when the artist is gone.
	GetTrackedArtist(ctx context.Context, id int64) (*domain.TrackedArtist, error)
	RefreshArtist(ctx context.Context, artistID int64) (*tracking.RefreshResult, error)
	// SearchMissing queues downloads for one artist's wanted albums and returns
	// the pass summary (queued/skipped/errors).
	SearchMissing(ctx context.Context, artistID int64) (*tracking.SearchResult, error)
}

// RateLimiter is the shared per-provider rate-limit cooldown. Declared here
// so every job consults and marks the same bucket all other provider-calling
// paths use; *metadata.ProviderCooldown satisfies it. One bucket keeps one
// task from pushing another into the same throttle.
type RateLimiter interface {
	CoolingDown(name string) bool
	MarkAfter(name string, retryAfter time.Duration)
}

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
	// Playlist syncs playlists against upstream sources (may be nil).
	Playlist PlaylistSyncer
	// Tracking lists and refreshes tracked artists (may be nil — the
	// RefreshTracked job then returns an error instead of panicking).
	Tracking TrackedRefresher
	// RateLimit is the shared per-provider cooldown bucket. Jobs consult it
	// before calling a provider and mark it on rate-limit responses (may be
	// nil — the job then behaves as if no provider is ever cooling down).
	RateLimit RateLimiter
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

// SetRateLimiter wires (or swaps) the shared provider cooldown bucket. The
// api layer calls this when the app-wide cooldown instance is installed, so
// jobs always share the same bucket as enrichment, discovery, and the health
// checker. Called once at startup, before any job can run (the write is not
// synchronized with running-job reads).
func (r *Runners) SetRateLimiter(rl RateLimiter) {
	r.deps.RateLimit = rl
}

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
