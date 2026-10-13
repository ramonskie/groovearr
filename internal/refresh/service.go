// Package refresh runs the service-owned background engine that drains the
// persisted catalog refresh queue and materializes provider metadata into the
// catalog.
//
// The engine owns its own goroutine lifecycle (Start/Stop) rather than running
// as a jobs.Manager job: a long backfill must not monopolise the Manager's
// single-flight slot (AGENTS §2). Provider I/O happens only through
// discovery.Provider — this package never references a provider name (AGENTS
// §1). The one shared metadata.ProviderCooldown bucket is consulted before
// every provider call and marked on a rate-limit response so one task cannot
// push another into the same throttle (AGENTS §8).
//
// Processing is deliberately paced: the loop waits Interval, claims a small
// batch of due queue rows, materializes each album in short per-album
// transactions, then repeats. It never holds a transaction across a provider
// call, so the single SQLite writer connection cannot starve readers.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// Store is the slice of the catalog the refresh engine needs: the persisted
// refresh queue, the album provider link/bookkeeping, and the materialized
// album tracklist. *catalog/sqlite.Store satisfies it.
type Store interface {
	catalog.RefreshQueue
	catalog.AlbumMetadataStore
	catalog.AlbumTrackStore
}

// Library is the small read-only view of the music library the engine needs to
// resolve the artist name and album title used for a provider search. Declared
// here (not imported as library.Store) so refresh depends on behavior, not a
// concrete store (AGENTS §3). *library/sqlite.Store satisfies it.
type Library interface {
	GetAlbum(ctx context.Context, id int64) (*domain.Album, error)
	GetArtist(ctx context.Context, id int64) (*domain.Artist, error)
}

// Cooldown is the shared per-provider rate-limit bucket. Declared here so the
// engine consults and marks the same bucket every other provider-calling path
// uses. *metadata.ProviderCooldown satisfies it; a nil Cooldown means "no
// provider is ever cooling down" and marks are dropped.
type Cooldown interface {
	CoolingDown(name string) bool
	MarkAfter(name string, retryAfter time.Duration)
}

// Sentinels returned by RefreshAlbum so the queue loop can classify an outcome
// without inspecting provider-specific errors.
var (
	// ErrNoProviders means no discovery provider is currently available. It is
	// not a hard failure: the item is rescheduled so a provider added later can
	// still materialize it.
	ErrNoProviders = errors.New("refresh: no discovery providers available")
	// ErrAlbumNotFound means the local album row is gone. The queue row is
	// terminal (removed) rather than retried forever.
	ErrAlbumNotFound = errors.New("refresh: album not found")
	// errAllCooling means every candidate provider is currently cooling down, so
	// no call was attempted. Treated like a rate-limit: reschedule, no failure.
	errAllCooling = errors.New("refresh: all discovery providers cooling down")
)

// Options configures the engine. Zero values fall back to DefaultOptions so a
// caller can set only what it cares about. Subtask 14 wires these from the live
// config closure; this package never caches config itself (AGENTS §9).
type Options struct {
	// Interval is the pause between drain ticks. The loop never blocks Start.
	Interval time.Duration
	// BatchSize is the maximum number of due queue rows claimed per tick.
	BatchSize int
	// MaxAttempts is how many hard failures an album may accumulate before it is
	// parked as StatusFailed and removed from the queue.
	MaxAttempts int
	// SearchLimit caps results returned by discovery.Provider.SearchAlbums.
	SearchLimit int
	// CooldownRetry is how long an item waits before retry when a provider is
	// cooling down or returned a rate-limit response (no failure is counted).
	CooldownRetry time.Duration
	// CallTimeout bounds every single provider call (AGENTS §7).
	CallTimeout time.Duration
	// StaleAfter is the soft TTL after which materialized album metadata is
	// considered stale. The drain loop never reads it; it is exposed for the api
	// read path (via AlbumStaleAfter) to decide when a warm read should enqueue
	// a background refresh behind the response (Tier-1 lazy staleness).
	StaleAfter time.Duration
}

// DefaultAlbumStaleAfter is the soft TTL applied when no live config value is
// registered: a legacy config with no catalog section keeps the 30-day default.
const DefaultAlbumStaleAfter = 30 * 24 * time.Hour

// DefaultOptions returns the sane defaults used when a field is left unset.
func DefaultOptions() Options {
	return Options{
		Interval:      time.Minute,
		BatchSize:     8,
		MaxAttempts:   5,
		SearchLimit:   20,
		CooldownRetry: 15 * time.Minute,
		CallTimeout:   30 * time.Second,
		StaleAfter:    DefaultAlbumStaleAfter,
	}
}

// withDefaults fills every unset (non-positive) field from DefaultOptions.
func (o Options) withDefaults() Options {
	d := DefaultOptions()
	if o.Interval <= 0 {
		o.Interval = d.Interval
	}
	if o.BatchSize <= 0 {
		o.BatchSize = d.BatchSize
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = d.MaxAttempts
	}
	if o.SearchLimit <= 0 {
		o.SearchLimit = d.SearchLimit
	}
	if o.CooldownRetry <= 0 {
		o.CooldownRetry = d.CooldownRetry
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = d.CallTimeout
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = d.StaleAfter
	}
	return o
}

// AlbumOutcome reports the result of one materialization attempt.
type AlbumOutcome struct {
	// Matched is true when a provider album was resolved and materialized.
	Matched bool
	// ProviderName/ProviderAlbumID identify the materialized provider link when
	// Matched is true.
	ProviderName    string
	ProviderAlbumID string
}

// Service is the paced, cooldown-aware refresh engine. Construct it with New,
// run it with Start, stop it with Stop. It is safe to Stop before Start and to
// call Start/Stop more than once.
type Service struct {
	store     Store
	lib       Library
	providers func() []discovery.Provider
	cooldown  Cooldown
	log       *slog.Logger
	opts      Options

	// order is the shared provider priority (D-001 §3). When set, the engine
	// tries providers in that order rather than whatever order the injected
	// providers closure returned; it is read live on every album, so a settings
	// change is honored without a restart (AGENTS §9). Nil keeps the injected
	// providers' own order (unchanged behavior).
	order *metadata.ProviderOrder

	// optsFn, when set, is re-read on every tick and every provider call so a
	// config hot-reload changes pacing/batch without a restart. Nil falls back to
	// the static opts passed to New (AGENTS §9 — config is never cached). Register
	// it before Start; the engine's goroutine reads it without a lock.
	optsFn func() Options

	// now is a test seam; production leaves it as time.Now.
	now func() time.Time

	// onMaterialized, when set, is invoked after an album's provider tracklist
	// and provider link are successfully written. It is the minimal completion
	// hook the API wires to an SSE broadcast so the UI can invalidate; the
	// engine is otherwise unchanged by it. Register it before Start.
	onMaterialized func(albumID int64)

	// enabledFn, when set, is re-read before every drain so a config
	// hot-reload can disable materialization without a restart (AGENTS §9 —
	// config is never cached). Nil means enabled. Register it before Start.
	enabledFn func() bool

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}

	// inflightMu guards inflight, the keyed per-album in-flight coalescer. It is
	// kept separate from mu (lifecycle) so a long provider call can never block
	// Start/Stop. Concurrent refreshes of the same album share one provider
	// fetch instead of duplicating it and clobbering each other's metadata.
	inflightMu sync.Mutex
	inflight   map[int64]*albumCall

	// statusMu guards the observability fields below (the last drain's time and
	// size) so a Status read never blocks Start/Stop or the item loop. It is
	// separate from mu (lifecycle) and inflightMu (per-album coalescing).
	statusMu       sync.Mutex
	lastDrainAt    time.Time
	lastDrainCount int
}

// New wires the engine's dependencies. providers is called on each drain so a
// provider registered or configured after startup is picked up without a
// restart. log may be nil.
func New(store Store, lib Library, providers func() []discovery.Provider, cooldown Cooldown, log *slog.Logger, opts Options) *Service {
	if providers == nil {
		providers = func() []discovery.Provider { return nil }
	}
	return &Service{
		store:     store,
		lib:       lib,
		providers: providers,
		cooldown:  cooldown,
		log:       log,
		opts:      opts.withDefaults(),
		now:       time.Now,
		inflight:  make(map[int64]*albumCall),
	}
}

// SetOnMaterialized registers an optional callback invoked after an album is
// successfully materialized (its provider tracklist and provider link written).
// The API uses it to broadcast a completion event so the UI can invalidate the
// album's metadata. Register it before Start — the callback is read from the
// engine's goroutine; a nil fn clears the hook and a nil service is a no-op.
func (s *Service) SetOnMaterialized(fn func(albumID int64)) {
	if s == nil {
		return
	}
	s.onMaterialized = fn
}

// SetProviderOrder wires the one shared provider priority order (D-001 §3) so
// the engine tries providers in the configured order instead of the order the
// providers closure happened to return. It is the same closure-backed
// ProviderOrder every other path uses, read live on each album (AGENTS §9), so
// a settings hot-reload is honored without a restart. A nil order (or a nil
// service) keeps the injected providers' own order — unchanged behavior.
func (s *Service) SetProviderOrder(order *metadata.ProviderOrder) {
	if s == nil {
		return
	}
	s.order = order
}

// SetOptionsFunc registers a live options provider that is re-read on every
// tick and every provider call, so a config hot-reload is honored without a
// restart (AGENTS §9). It is the minimal seam that lets the composition root in
// cmd/groovearr pass the live config closure without this package ever importing
// config. A nil fn is ignored; a nil service is a no-op. Register it before
// Start — the provider is read from the engine's own goroutine.
func (s *Service) SetOptionsFunc(fn func() Options) {
	if s == nil || fn == nil {
		return
	}
	s.optsFn = fn
}

// options returns the effective options for this call: the live provider when
// one is registered, otherwise the static options passed to New. Defaults are
// applied to whatever is returned so a partial/zero live value never disables a
// field (Interval <= 0 would otherwise busy-loop).
func (s *Service) options() Options {
	if s.optsFn != nil {
		return s.optsFn().withDefaults()
	}
	return s.opts
}

// SetEnabledFunc registers a live gate consulted before every drain, so a config
// hot-reload can disable materialization without a restart (AGENTS §9 — config
// is never cached in a struct). A nil fn is ignored and a nil service is a
// no-op; register it before Start. A service with no gate is always enabled.
func (s *Service) SetEnabledFunc(fn func() bool) {
	if s == nil || fn == nil {
		return
	}
	s.enabledFn = fn
}

// enabled reports whether the engine may touch the queue/providers right now. A
// nil gate (or nil service) means enabled, preserving the pre-gate behavior for
// tests and minimal constructors.
func (s *Service) enabled() bool {
	if s == nil || s.enabledFn == nil {
		return true
	}
	return s.enabledFn()
}

// AlbumStaleAfter returns the effective soft TTL for album metadata: the live
// config value when one is registered, otherwise the engine default. The api
// read path uses it to decide when a warm read is stale and should enqueue a
// background refresh (Tier-1 lazy staleness). A nil service returns the default.
func (s *Service) AlbumStaleAfter() time.Duration {
	if s == nil {
		return DefaultOptions().StaleAfter
	}
	return s.options().StaleAfter
}

// Status is a read-only snapshot of the engine's configuration and recent
// activity, surfaced by GET /api/catalog/status for the settings status card.
// It describes the engine only; queue depth and materialized counts come from
// the catalog store.
type Status struct {
	// Running reports whether the drain loop goroutine is alive.
	Running bool
	// IntervalSeconds is the effective pause between drain ticks.
	IntervalSeconds int
	// BatchSize is the effective maximum rows claimed per tick.
	BatchSize int
	// LastDrainAt is when the most recent tick finished, or nil if none yet.
	LastDrainAt *time.Time
	// LastDrainCount is how many rows the most recent tick applied an outcome to.
	LastDrainCount int
}

// Status returns a snapshot of the engine for observability. It takes only the
// short lifecycle and status locks, so it never blocks Start/Stop or a drain. A
// nil service returns the zero value.
func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	o := s.options()

	s.mu.Lock()
	running := s.cancel != nil
	s.mu.Unlock()

	s.statusMu.Lock()
	lastAt := s.lastDrainAt
	lastCount := s.lastDrainCount
	s.statusMu.Unlock()

	st := Status{
		Running:         running,
		IntervalSeconds: int(o.Interval / time.Second),
		BatchSize:       o.BatchSize,
		LastDrainCount:  lastCount,
	}
	if !lastAt.IsZero() {
		at := lastAt
		st.LastDrainAt = &at
	}
	return st
}

// recordDrain stores the outcome of the most recent tick for Status. It is
// called only from the loop goroutine.
func (s *Service) recordDrain(applied int) {
	s.statusMu.Lock()
	s.lastDrainAt = s.now().UTC()
	s.lastDrainCount = applied
	s.statusMu.Unlock()
}

// Start launches the drain loop in its own goroutine. It returns immediately;
// the loop exits when ctx is cancelled or Stop is called. Starting an already
// running service is a no-op. A nil service is a no-op.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.loop(runCtx)
		// Clear the lifecycle handles when the loop exits without Stop (parent
		// ctx cancelled), so Status() reports not-running and a later Start can
		// restart. Stop() may have already cleared them; only clear the handles
		// still owned by this run.
		s.mu.Lock()
		if s.done == done {
			s.cancel = nil
			s.done = nil
		}
		s.mu.Unlock()
	}()
}

// Stop cancels the loop and blocks until its goroutine has exited, so it never
// delays shutdown (AGENTS §7). It is safe to call when the service was never
// started or already stopped.
func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// loop is the engine's body: wait Interval, drain one batch, repeat, exiting on
// ctx.Done. The tick is never queued, so a slow provider cannot build a backlog.
// The interval is re-read after every tick so a live options provider (config
// hot-reload) takes effect without a restart.
func (s *Service) loop(ctx context.Context) {
	timer := time.NewTimer(s.options().Interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			n, err := s.processBatch(ctx)
			// Record only ticks that could actually drain: a disabled catalog
			// returns (0, nil) without touching the queue, so stamping it would
			// show "0 items · Ns ago" activity that never happened.
			if s.enabled() {
				s.recordDrain(n)
			}
			if err != nil && ctx.Err() == nil {
				s.logWarn("refresh drain failed", "error", err)
			}
			timer.Reset(s.options().Interval)
		}
	}
}

// processBatch claims up to BatchSize due rows and processes each one. It
// returns how many rows had an outcome applied (materialized, rescheduled, or
// parked) — rows skipped as an unsupported entity kind are not counted. A store
// error claiming the batch is returned; a per-item error is logged and the
// batch continues so one bad row cannot stall the queue.
func (s *Service) processBatch(ctx context.Context) (int, error) {
	// A disabled catalog is a live-config state (AGENTS §9): skip the whole
	// drain so no queue row is claimed and no provider is called.
	if !s.enabled() {
		return 0, nil
	}
	items, err := s.store.ClaimDueRefresh(ctx, s.now(), s.options().BatchSize)
	if err != nil {
		return 0, fmt.Errorf("claim due refresh: %w", err)
	}
	handled := 0
	for _, item := range items {
		if ctx.Err() != nil {
			return handled, ctx.Err()
		}
		if item.EntityType != catalog.EntityAlbum {
			// Only albums are materialized today (artist metadata is a later
			// phase). Defer rather than busy-loop on an unsupported row.
			s.deferUnsupported(ctx, item)
			continue
		}
		if err := s.processItem(ctx, item); err != nil {
			s.logWarn("refresh item failed", "queue_id", item.ID, "album_id", item.EntityID, "error", err)
			continue
		}
		handled++
	}
	return handled, nil
}

// processItem materializes one queued album and applies the queue bookkeeping
// for its outcome. It returns an error only for a store write failure so the
// caller can log-and-continue.
func (s *Service) processItem(ctx context.Context, item catalog.RefreshItem) error {
	outcome, err := s.RefreshAlbum(ctx, item.EntityID)

	switch {
	case err == nil && outcome.Matched:
		return s.store.FinishRefresh(ctx, item.ID, catalog.StatusFresh)

	case err == nil:
		// No provider matched. album_metadata is already parked as unmatched;
		// retry on the long interval so the queue drains instead of churning.
		return s.store.RetryRefresh(ctx, item.ID, item.Attempts,
			s.now().Add(catalog.UnmatchedRetry), "no provider match")

	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// A cancelled parent is a clean stop, not a failure: leave the row
		// untouched so the next run re-claims it.
		return nil

	case errors.Is(err, metadata.ErrRateLimited),
		errors.Is(err, errAllCooling),
		errors.Is(err, ErrNoProviders):
		// Transient/self-healing: the cooldown (or a future provider) governs
		// the next attempt, so reschedule without counting a failure.
		return s.store.RetryRefresh(ctx, item.ID, item.Attempts,
			s.now().Add(s.options().CooldownRetry), err.Error())

	case errors.Is(err, ErrAlbumNotFound):
		// The album row is gone; drop the orphaned queue row as terminal.
		return s.store.FinishRefresh(ctx, item.ID, catalog.StatusFailed)

	default:
		return s.retryOrFail(ctx, item, err)
	}
}

// retryOrFail records a hard failure: increment the attempt count and either
// reschedule with exponential backoff or, once attempts are exhausted, park the
// album as StatusFailed and remove it from the queue (terminal).
func (s *Service) retryOrFail(ctx context.Context, item catalog.RefreshItem, cause error) error {
	attempts := item.Attempts + 1
	if attempts >= s.options().MaxAttempts {
		if err := s.recordAlbumFailure(ctx, item.EntityID, attempts, cause.Error(), catalog.StatusFailed); err != nil {
			return err
		}
		return s.store.FinishRefresh(ctx, item.ID, catalog.StatusFailed)
	}
	if err := s.recordAlbumFailure(ctx, item.EntityID, attempts, cause.Error(), catalog.StatusPending); err != nil {
		return err
	}
	return s.store.RetryRefresh(ctx, item.ID, attempts,
		s.now().Add(catalog.BackoffFor(attempts)), cause.Error())
}

// deferUnsupported reschedules an entity kind this engine does not yet handle
// (artist metadata is a later phase) on the long retry interval so the row does
// not hot-loop every tick.
func (s *Service) deferUnsupported(ctx context.Context, item catalog.RefreshItem) {
	err := s.store.RetryRefresh(ctx, item.ID, item.Attempts,
		s.now().Add(catalog.UnmatchedRetry), "unsupported entity type")
	if err != nil {
		s.logWarn("refresh defer unsupported failed", "queue_id", item.ID, "error", err)
	}
}

// albumCall is one in-flight materialization of a single album, shared by every
// caller that asked for the same album while it was running. done is closed once
// after the result is published so every waiter observes the same outcome.
type albumCall struct {
	done    chan struct{}
	outcome AlbumOutcome
	err     error
}

// RefreshAlbum materializes one album's provider tracklist and is the single
// entry point the cold fetcher, the engine loop, and the warm job all call. It
// coalesces concurrent refreshes of the same album into one in-flight call, so
// those callers share exactly one provider fetch (and one metadata write)
// instead of racing duplicate calls and clobbering each other's bookkeeping.
//
// The first caller runs the work on its own context; every caller that arrives
// while it is in flight waits for the shared result (or its own context ending,
// so a cold open can still fall back promptly). See refreshAlbum for the
// materialization logic.
func (s *Service) RefreshAlbum(ctx context.Context, albumID int64) (AlbumOutcome, error) {
	s.inflightMu.Lock()
	if s.inflight == nil {
		s.inflight = make(map[int64]*albumCall)
	}
	if call, ok := s.inflight[albumID]; ok {
		s.inflightMu.Unlock()
		return s.waitAlbum(ctx, call)
	}
	call := &albumCall{done: make(chan struct{})}
	s.inflight[albumID] = call
	s.inflightMu.Unlock()

	outcome, err := s.refreshAlbum(ctx, albumID)

	s.inflightMu.Lock()
	call.outcome, call.err = outcome, err
	close(call.done)
	delete(s.inflight, albumID)
	s.inflightMu.Unlock()
	return outcome, err
}

// waitAlbum blocks until the shared materialization completes or the caller's
// own context ends. A caller whose context ends first returns ctx.Err() promptly
// so it can fall back; the shared work keeps running and still publishes to any
// other waiter.
func (s *Service) waitAlbum(ctx context.Context, call *albumCall) (AlbumOutcome, error) {
	select {
	case <-call.done:
		return call.outcome, call.err
	case <-ctx.Done():
		return AlbumOutcome{}, ctx.Err()
	}
}

// refreshAlbum materializes one album's provider tracklist into the catalog and
// updates its provider link/bookkeeping.
//
// Steady state is a single provider call: when a stored provider link resolves
// to a live, non-cooling provider, only GetAlbumTracks is called. Otherwise the
// engine searches by "artist title" and picks a generic, case-insensitive exact
// match (artist+title, then title-only). The materialized rows and the metadata
// row are written in short per-album transactions — never across a provider
// call.
func (s *Service) refreshAlbum(ctx context.Context, albumID int64) (AlbumOutcome, error) {
	meta, err := s.store.GetAlbumMetadata(ctx, albumID)
	if err != nil {
		return AlbumOutcome{}, err
	}
	providers := metadata.Order(s.order, s.providers())
	if len(providers) == 0 {
		return AlbumOutcome{}, ErrNoProviders
	}

	// 1. Provider-link reuse: one call, no search.
	if meta != nil && meta.ProviderAlbumID != "" && meta.ProviderName != "" {
		if p := providerByName(providers, meta.ProviderName); p != nil && !s.coolingDown(p.Name()) {
			tracks, err := s.fetchTracks(ctx, p, meta.ProviderAlbumID)
			if err != nil {
				if errors.Is(err, metadata.ErrRateLimited) {
					// Rate limits are transient and handled by the shared
					// cooldown; the link is not at fault.
					return AlbumOutcome{}, err
				}
				// The stored link is dead (e.g. the provider 404s an album it
				// removed). Mark it stale by clearing it so a later retry falls
				// back to a fresh search instead of being stuck on the dead
				// link forever.
				if cerr := s.clearProviderLink(ctx, albumID, meta); cerr != nil {
					return AlbumOutcome{}, cerr
				}
				return AlbumOutcome{}, err
			}
			if err := s.materialize(ctx, albumID, meta, p.Name(), meta.ProviderAlbumID, tracks); err != nil {
				return AlbumOutcome{}, err
			}
			return AlbumOutcome{Matched: true, ProviderName: p.Name(), ProviderAlbumID: meta.ProviderAlbumID}, nil
		}
	}

	// 2. Resolve the local artist/title for a generic provider search.
	album, err := s.lib.GetAlbum(ctx, albumID)
	if err != nil {
		return AlbumOutcome{}, err
	}
	if album == nil {
		return AlbumOutcome{}, ErrAlbumNotFound
	}
	artistName := ""
	if album.ArtistID != 0 {
		artist, err := s.lib.GetArtist(ctx, album.ArtistID)
		if err != nil {
			return AlbumOutcome{}, err
		}
		if artist != nil {
			artistName = artist.Name
		}
	}
	query := strings.TrimSpace(artistName + " " + album.Title)

	// 3. Search each provider, skipping cooling ones, and materialize the first
	//    acceptable match.
	tried := 0
	var rateLimitedErr, searchErr, matchErr error
	for _, p := range providers {
		if ctx.Err() != nil {
			return AlbumOutcome{}, ctx.Err()
		}
		if s.coolingDown(p.Name()) {
			continue
		}
		results, err := s.searchAlbums(ctx, p, query)
		if err != nil {
			if errors.Is(err, metadata.ErrRateLimited) {
				rateLimitedErr = err
			} else {
				searchErr = err
			}
			continue
		}
		tried++
		best, ok := bestMatch(results, artistName, album.Title)
		if !ok {
			continue
		}
		tracks, err := s.fetchTracks(ctx, p, best.ProviderID)
		if err != nil {
			if errors.Is(err, metadata.ErrRateLimited) {
				rateLimitedErr = err
			} else {
				// A matched album whose tracklist call failed is a hard error,
				// not "unmatched": retry it with backoff.
				matchErr = err
			}
			continue
		}
		if err := s.materialize(ctx, albumID, meta, p.Name(), best.ProviderID, tracks); err != nil {
			return AlbumOutcome{}, err
		}
		return AlbumOutcome{Matched: true, ProviderName: p.Name(), ProviderAlbumID: best.ProviderID}, nil
	}

	// 4. Classify the no-match case.
	if rateLimitedErr != nil {
		return AlbumOutcome{}, rateLimitedErr
	}
	if matchErr != nil {
		return AlbumOutcome{}, matchErr
	}
	if tried == 0 {
		if searchErr != nil {
			return AlbumOutcome{}, searchErr
		}
		return AlbumOutcome{}, errAllCooling
	}
	// Genuinely unmatched: park the album with a long retry, queue drains.
	if err := s.markUnmatched(ctx, albumID, meta); err != nil {
		return AlbumOutcome{}, err
	}
	return AlbumOutcome{Matched: false}, nil
}

// materialize writes the provider tracklist and the refreshed metadata row in
// short per-album transactions. It preserves the last-accessed timestamp so a
// refresh does not erase the read-recency used for refresh ordering.
func (s *Service) materialize(ctx context.Context, albumID int64, prev *catalog.AlbumMetadata,
	providerName, providerAlbumID string, tracks []discovery.TrackInfo) error {
	now := s.now()
	keys := providerTrackKeys(tracks)
	rows := make([]catalog.AlbumTrack, 0, len(tracks))
	for i, t := range tracks {
		rows = append(rows, catalog.AlbumTrack{
			AlbumID:         albumID,
			DiscNumber:      keys[i][0],
			TrackNumber:     keys[i][1],
			Title:           t.Title,
			ArtistName:      t.ArtistName,
			DurationMs:      t.DurationMs,
			ISRC:            t.ISRC,
			ProviderName:    providerName,
			ProviderTrackID: t.ProviderID,
			Origin:          catalog.OriginProvider,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}
	if err := s.store.ReplaceAlbumTracks(ctx, albumID, rows); err != nil {
		return err
	}

	m := catalog.AlbumMetadata{
		AlbumID:         albumID,
		ProviderName:    providerName,
		ProviderAlbumID: providerAlbumID,
		RefreshStatus:   catalog.StatusFresh,
		LastRefreshedAt: now,
		UpdatedAt:       now,
	}
	if prev != nil {
		m.LastAccessedAt = prev.LastAccessedAt
	}
	if err := s.store.UpsertAlbumMetadata(ctx, m); err != nil {
		return err
	}
	s.notifyMaterialized(albumID)
	return nil
}

// providerTrackKeys assigns every provider track a unique (disc, track) key.
// Providers occasionally omit or repeat disc/track numbers, and the catalog's
// UNIQUE(album_id, disc_number, track_number) would then abort the whole album
// on insert. A missing disc defaults to 1; a missing track number is filled from
// the track's 1-based position; and a collision is resolved by probing forward
// for the next free number within the disc. The mapping is pure and stable for a
// given input, so a provider's repeated fetch always materializes the same rows.
func providerTrackKeys(tracks []discovery.TrackInfo) [][2]int {
	keys := make([][2]int, len(tracks))
	used := make(map[[2]int]struct{}, len(tracks))
	for i := range tracks {
		disc := tracks[i].DiscNumber
		if disc <= 0 {
			disc = 1
		}
		num := tracks[i].TrackNumber
		if num <= 0 {
			num = i + 1
		}
		for {
			key := [2]int{disc, num}
			if _, dup := used[key]; !dup {
				used[key] = struct{}{}
				keys[i] = key
				break
			}
			num++
		}
	}
	return keys
}

// notifyMaterialized fires the optional completion hook, nil-safe.
func (s *Service) notifyMaterialized(albumID int64) {
	if s.onMaterialized != nil {
		s.onMaterialized(albumID)
	}
}

// markUnmatched parks an album as StatusUnmatched, preserving any previous
// provider link so a later retry can still reuse it.
func (s *Service) markUnmatched(ctx context.Context, albumID int64, prev *catalog.AlbumMetadata) error {
	m := catalog.AlbumMetadata{}
	if prev != nil {
		m = *prev
	}
	m.AlbumID = albumID
	m.RefreshStatus = catalog.StatusUnmatched
	m.RefreshError = "no provider match"
	m.UpdatedAt = s.now()
	return s.store.UpsertAlbumMetadata(ctx, m)
}

// clearProviderLink marks a stored provider link stale by clearing the provider
// name/id on the album's metadata row, so a later refresh falls back to a fresh
// search. The rest of the bookkeeping (status, timestamps, failure count) is
// preserved.
func (s *Service) clearProviderLink(ctx context.Context, albumID int64, prev *catalog.AlbumMetadata) error {
	m := catalog.AlbumMetadata{}
	if prev != nil {
		m = *prev
	}
	m.AlbumID = albumID
	m.ProviderName = ""
	m.ProviderAlbumID = ""
	m.UpdatedAt = s.now()
	return s.store.UpsertAlbumMetadata(ctx, m)
}

// recordAlbumFailure loads the current metadata row, bumps the consecutive
// failure count, and stores the new status/error. Callers pass StatusPending
// (will retry) or StatusFailed (terminal).
func (s *Service) recordAlbumFailure(ctx context.Context, albumID int64, attempts int, lastErr string, status catalog.RefreshStatus) error {
	m := catalog.AlbumMetadata{}
	prev, err := s.store.GetAlbumMetadata(ctx, albumID)
	if err != nil {
		return err
	}
	if prev != nil {
		m = *prev
	}
	m.AlbumID = albumID
	m.RefreshStatus = status
	m.ConsecutiveFailures = attempts
	m.RefreshError = lastErr
	m.UpdatedAt = s.now()
	return s.store.UpsertAlbumMetadata(ctx, m)
}

// fetchTracks calls GetAlbumTracks under the per-call timeout and marks the
// shared cooldown on a rate-limit response.
func (s *Service) fetchTracks(ctx context.Context, p discovery.Provider, providerAlbumID string) ([]discovery.TrackInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, s.options().CallTimeout)
	defer cancel()
	tracks, err := p.GetAlbumTracks(cctx, providerAlbumID)
	if err != nil {
		s.markProviderError(p.Name(), err)
		return nil, err
	}
	return tracks, nil
}

// searchAlbums calls SearchAlbums under the per-call timeout and marks the
// shared cooldown on a rate-limit response.
func (s *Service) searchAlbums(ctx context.Context, p discovery.Provider, query string) ([]discovery.AlbumResult, error) {
	opts := s.options()
	cctx, cancel := context.WithTimeout(ctx, opts.CallTimeout)
	defer cancel()
	results, err := p.SearchAlbums(cctx, query, opts.SearchLimit)
	if err != nil {
		s.markProviderError(p.Name(), err)
		return nil, err
	}
	return results, nil
}

// markProviderError parks the provider app-wide when it reported a rate limit,
// honoring a server-requested Retry-After when the provider supplied one.
func (s *Service) markProviderError(name string, err error) {
	if s.cooldown == nil || !errors.Is(err, metadata.ErrRateLimited) {
		return
	}
	var retryAfter time.Duration
	var rl *metadata.RateLimitError
	if errors.As(err, &rl) {
		retryAfter = rl.RetryAfter
	}
	s.cooldown.MarkAfter(name, retryAfter)
}

// coolingDown is nil-safe: no cooldown wired means nothing is parked.
func (s *Service) coolingDown(name string) bool {
	return s.cooldown != nil && s.cooldown.CoolingDown(name)
}

// logWarn logs through the shared logger when one is wired; a nil logger is a
// no-op so tests and minimal constructors stay quiet.
func (s *Service) logWarn(msg string, args ...any) {
	if s.log == nil {
		return
	}
	s.log.Warn(msg, append(args, "component", "refresh")...)
}

// providerByName returns the first live provider with the canonical name, or
// nil. Kept generic: no provider is special-cased.
func providerByName(providers []discovery.Provider, name string) discovery.Provider {
	for _, p := range providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// bestMatch returns the FIRST acceptable match in the results from a single
// provider's search: a case-, punctuation- and version-insensitive artist+title
// match wins. It applies no cross-provider ranking — the caller owns provider
// order and materializes whichever provider this confirms first. A title match
// by an unrelated artist is a clash, not a match, and is rejected — but a
// collaborating credit ("Artist A", "Artist A & Artist B", "Artist A feat.
// Artist B") is accepted because the artists are compatible. A title-only match
// is still allowed when the provider omitted the artist. It applies no
// provider-specific logic (AGENTS §1).
func bestMatch(results []discovery.AlbumResult, artist, title string) (discovery.AlbumResult, bool) {
	wantTitle := normalizeTitle(title)
	if wantTitle == "" {
		return discovery.AlbumResult{}, false
	}
	wantArtist := normalize(artist)

	var unknownArtist *discovery.AlbumResult
	for i := range results {
		if normalizeTitle(results[i].Title) != wantTitle {
			continue
		}
		// No usable library artist: a title match is the strongest signal we have.
		if wantArtist == "" {
			return results[i], true
		}
		candArtist := normalize(results[i].ArtistName)
		if !artistCompatible(artist, results[i].ArtistName) {
			continue
		}
		// The provider omitted the artist: keep it as a weaker fallback rather
		// than treating "unknown" as a mismatch (and rather than letting it
		// shadow a later known-artist match).
		if candArtist == "" {
			if unknownArtist == nil {
				unknownArtist = &results[i]
			}
			continue
		}
		return results[i], true
	}
	if unknownArtist != nil {
		return *unknownArtist, true
	}
	return discovery.AlbumResult{}, false
}

// artistCompatible reports whether a provider artist credit can refer to the
// same act as the library artist. It accepts:
//
//   - an exact normalized credit;
//   - a collaboration containment where the shorter credit is the whole-token
//     prefix immediately before a space-delimited joiner
//     ("Artist A" vs "Artist A & Artist B");
//   - an article-only containment whose only added tokens are articles/stopwords
//     ("The Cure" vs "Cure");
//   - at least TWO shared significant tokens, so a reordered/reformatted
//     collaboration still matches ("Simon & Garfunkel" vs "Garfunkel & Simon").
//
// It rejects a bare containment ("Air" vs "Air Supply", "King" vs "King
// Crimson"), a single shared generic token, and an overlap that is only
// stopwords/joiners ("The Cure" vs "The Beatles"). Raw credits are compared for
// the collaboration branch because normalize() strips the "&", "," and "/"
// joiners that check relies on. It is provider-agnostic (AGENTS §1).
func artistCompatible(wantRaw, candRaw string) bool {
	want := normalize(wantRaw)
	cand := normalize(candRaw)
	if want == "" || cand == "" {
		return true
	}
	if want == cand {
		return true
	}
	if collabContains(wantRaw, candRaw) {
		return true
	}
	if articleOnlyContains(wantRaw, candRaw) {
		return true
	}
	return shareTwoSignificantTokens(want, cand)
}

// collabContains reports whether one raw credit is a collaboration of the
// other: the shorter credit is the WHOLE-TOKEN PREFIX immediately before the
// longer credit's first space-delimited multi-credit joiner. A joiner must be
// space-delimited (" & ", " / ", ", ") or a whitespace-delimited word joiner
// ("feat", "ft", "featuring", "with", "x"); a compact symbol inside a single
// act ("AC/DC") is NOT a joiner, so "AC/DC" does not bind to "AC". A
// containment that is not the prefix before a joiner is left to
// articleOnlyContains.
//
// Residual tolerance (intentional, not a bug): a short credit that happens to
// be the prefix before a space-delimited joiner still matches — for example
// "Earth" vs "Earth, Wind & Fire" and "Tyler" vs "Tyler, The Creator". This is
// an alias tolerance for the common "lead act, featured act" spelling.
func collabContains(aRaw, bRaw string) bool {
	a := normalize(aRaw)
	b := normalize(bRaw)
	switch {
	case containsTokenSeq(a, b):
		return collabPrefixIs(aRaw, b)
	case containsTokenSeq(b, a):
		return collabPrefixIs(bRaw, a)
	}
	return false
}

// collabPrefixIs reports whether shorterNorm is exactly the normalized credit
// preceding longerRaw's first space-delimited joiner. A longerRaw with no such
// joiner never qualifies.
func collabPrefixIs(longerRaw, shorterNorm string) bool {
	idx := firstCreditJoinerIndex(longerRaw)
	if idx < 0 {
		return false
	}
	return normalize(longerRaw[:idx]) == shorterNorm
}

// creditJoinerWords are whitespace-delimited tokens that join two credits.
var creditJoinerWords = map[string]struct{}{
	"feat": {}, "ft": {}, "featuring": {}, "with": {}, "x": {},
}

// hasCreditJoiner reports whether a raw credit carries a SPACE-DELIMITED
// multi-credit joiner: " & ", " / ", a comma followed by a space (", "), or a
// whitespace-delimited word joiner token ("feat", "ft", "featuring", "with",
// "x"). A compact "/" or "&" inside one act ("AC/DC") is NOT a joiner.
func hasCreditJoiner(raw string) bool {
	return firstCreditJoinerIndex(raw) >= 0
}

// firstCreditJoinerIndex returns the byte index at which the first
// space-delimited multi-credit joiner begins in raw, or -1 when raw has none.
// Word joiners match as whole whitespace-delimited tokens; symbol joiners only
// when surrounded by whitespace (" & ", " / ") or, for a comma, when followed
// by a space.
func firstCreditJoinerIndex(raw string) int {
	idx := -1
	for i := 0; i < len(raw); {
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		start := i
		for i < len(raw) && raw[i] != ' ' && raw[i] != '\t' {
			i++
		}
		if start >= i {
			break
		}
		if _, ok := creditJoinerWords[normalize(raw[start:i])]; ok && (idx < 0 || start < idx) {
			idx = start
		}
	}
	for _, sep := range []string{" & ", " / ", ", "} {
		if p := strings.Index(raw, sep); p >= 0 && (idx < 0 || p < idx) {
			idx = p
		}
	}
	return idx
}

// containsTokenSeq reports whether haystack contains needle as a contiguous run
// of whole tokens, so "artist a" matches "artist a artist b" but not "artist ab".
func containsTokenSeq(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(" "+haystack+" ", " "+needle+" ")
}

// articleOnlyContains reports whether one normalized credit contains the other
// as a whole-token run and the LONGER credit adds only articles/stopwords
// ("The Cure" ⊃ "Cure"). It requires the longer RAW credit to carry no
// collaboration joiner, so this branch never overlaps collabContains. A longer
// credit that adds a real word ("Air Supply" ⊅ "Air", "New Order" ⊅ "Order") is
// a different act and is rejected.
func articleOnlyContains(aRaw, bRaw string) bool {
	a := normalize(aRaw)
	b := normalize(bRaw)
	switch {
	case containsTokenSeq(a, b):
		return !hasCreditJoiner(aRaw) && stopwordExtrasOnly(a, b)
	case containsTokenSeq(b, a):
		return !hasCreditJoiner(bRaw) && stopwordExtrasOnly(b, a)
	}
	return false
}

// stopwordExtrasOnly reports whether dropping every stopword from longer leaves
// exactly shorter's tokens — i.e. longer = shorter + articles/stopwords only.
func stopwordExtrasOnly(longer, shorter string) bool {
	return equalTokens(dropStopwords(longer), strings.Fields(shorter))
}

// dropStopwords returns s's tokens with every artist stopword removed.
func dropStopwords(s string) []string {
	var out []string
	for _, t := range strings.Fields(s) {
		if _, stop := artistStopwords[t]; stop {
			continue
		}
		out = append(out, t)
	}
	return out
}

// equalTokens reports whether two token slices are equal element-wise.
func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// artistStopwords are credit words with no identifying power: articles,
// prepositions and collaboration joiners. A shared token from this set must
// never make two different artists compatible. normalize() strips the "&"
// symbol, so it never appears as a token here; it is handled as a raw joiner in
// hasCreditJoiner.
var artistStopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "and": {}, "of": {}, "for": {}, "with": {},
	"feat": {}, "ft": {}, "featuring": {}, "vs": {}, "de": {}, "la": {}, "le": {},
	"los": {}, "las": {}, "el": {}, "di": {}, "da": {},
}

// shareTwoSignificantTokens reports whether the two normalized credits share at
// least TWO distinctive tokens. Two shared significant tokens are strong
// evidence of the same act even when reordered ("Simon & Garfunkel" vs
// "Garfunkel & Simon"). A single shared token is deliberately NOT enough:
// generic words bind different acts ("King" vs "King Crimson", "Rock" vs
// "Rock Band"). Tokens are deduplicated on both sides so a repeated word cannot
// inflate the shared count.
func shareTwoSignificantTokens(a, b string) bool {
	seen := make(map[string]struct{})
	for _, t := range significantTokens(a) {
		seen[t] = struct{}{}
	}
	shared := 0
	for _, t := range significantTokens(b) {
		if _, ok := seen[t]; !ok {
			continue
		}
		shared++
		if shared >= 2 {
			return true
		}
	}
	return false
}

// significantTokens returns the distinct distinctive tokens of a normalized
// credit: tokens that are at least four runes and not stopwords.
func significantTokens(s string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, t := range strings.Fields(s) {
		if !significantArtistToken(t) {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// significantArtistToken reports whether a normalized token is distinctive
// enough to identify an act: at least four RUNES (not bytes) and not a
// stopword. Rune count keeps multi-byte credits consistent with ASCII ones.
func significantArtistToken(t string) bool {
	if utf8.RuneCountInString(t) < 4 {
		return false
	}
	_, stop := artistStopwords[t]
	return !stop
}

// normalize reduces a name to a comparison key that ignores case and every
// non-alphanumeric character (quotes, brackets, punctuation), collapsing the
// result into space-separated tokens. `"WOW"` and `WOW` both become "wow";
// `AC/DC` becomes "ac dc". It carries no provider knowledge (AGENTS §1).
func normalize(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// titleVersionTokens are generic SAME-CONTENT edition/format markers providers
// append to a title. Matching tolerates them so `Album` and `Album (Deluxe
// Edition)` are treated as the same release. They are deliberately limited to
// markers that do not change the materialized tracklist: performance/format
// variants (live, acoustic, instrumental, karaoke, mono, stereo), a single, or
// a different track listing are NOT here, because collapsing them would bind a
// library album to the wrong provider release. They are generic music terms,
// never provider names (AGENTS §1).
var titleVersionTokens = map[string]bool{
	"deluxe": true, "edition": true, "expanded": true, "anniversary": true,
	"remaster": true, "remastered": true,
	"bonus": true, "explicit": true, "clean": true,
}

// normalizeTitle is the title-specific comparison key: it drops a DELIMITED
// trailing edition descriptor and a featuring credit, then normalizes (case-,
// quote-, bracket- and punctuation-insensitive), so a library title with quotes
// or a delimited version suffix matches the bare provider title. It never
// reduces a non-empty title to an empty key.
func normalizeTitle(s string) string {
	base := normalize(stripEditionSuffix(s))
	if base == "" {
		return ""
	}
	trimmed := stripFeaturing(base)
	if trimmed == "" {
		return base
	}
	return trimmed
}

// stripFeaturing drops a "feat"/"ft"/"featuring" credit and everything after it.
func stripFeaturing(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		if f == "feat" || f == "ft" || f == "featuring" {
			return strings.Join(fields[:i], " ")
		}
	}
	return s
}

// stripEditionSuffix removes a trailing edition/format descriptor only when it
// was DELIMITED in the original title: a trailing parenthetical/bracket group
// ("Album (Deluxe Edition)", "Album [Deluxe]", "Album (2020 Remaster)") or a
// suffix after the last dash/colon separator ("Album - Remastered"). A bare
// trailing word in a plain title ("New Edition") is never stripped, so a
// legitimate title is not collapsed to a shorter, different one. It never
// returns an empty base.
func stripEditionSuffix(s string) string {
	for {
		if next, ok := stripTrailingBracketGroup(s); ok {
			s = next
			continue
		}
		if next, ok := stripSeparatorSuffix(s); ok {
			s = next
			continue
		}
		return s
	}
}

// stripTrailingBracketGroup removes a trailing "(...)" or "[...]" group whose
// inner tokens are all edition markers (or bare years) and include at least one
// real edition marker. It returns the base title and true only when such a
// group is present and a non-empty base remains.
func stripTrailingBracketGroup(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return s, false
	}
	var open byte
	switch s[len(s)-1] {
	case ')':
		open = '('
	case ']':
		open = '['
	default:
		return s, false
	}
	idx := strings.LastIndexByte(s, open)
	if idx < 0 {
		return s, false
	}
	if !isEditionGroup(s[idx+1 : len(s)-1]) {
		return s, false
	}
	base := strings.TrimSpace(s[:idx])
	if base == "" {
		return s, false
	}
	return base, true
}

// stripSeparatorSuffix removes a suffix after the last dash/colon separator
// whose tokens form an edition group, e.g. "Album - Remastered". If the last
// separator's suffix is not an edition group, the title is returned unchanged
// (so "Spider-Man" and "Album - Live" are untouched).
func stripSeparatorSuffix(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for i := len(s) - 1; i > 0; i-- {
		if s[i] != '-' && s[i] != ':' {
			continue
		}
		if !isEditionGroup(s[i+1:]) {
			return s, false
		}
		base := strings.TrimSpace(s[:i])
		if base == "" {
			return s, false
		}
		return base, true
	}
	return s, false
}

// isEditionGroup reports whether every whitespace/punctuation-separated token
// is an edition marker or a bare year, and at least one is an edition marker.
// A lone performance/format word ("live", "single") or a bare year fails, so
// "Album (Live)", "Album (Single)" and "Album (2020)" are not collapsed.
func isEditionGroup(s string) bool {
	tokens := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(tokens) == 0 {
		return false
	}
	marker := false
	for _, t := range tokens {
		switch {
		case titleVersionTokens[t]:
			marker = true
		case isYearToken(t):
		default:
			return false
		}
	}
	return marker
}

// isYearToken reports whether t is a plausible 4-digit year (1900-2099), the
// only non-marker token allowed inside a delimited edition group so
// "(2020 Remaster)" collapses.
func isYearToken(t string) bool {
	if len(t) != 4 {
		return false
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return false
		}
	}
	n := int(t[0]-'0')*1000 + int(t[1]-'0')*100 + int(t[2]-'0')*10 + int(t[3]-'0')
	return n >= 1900 && n <= 2099
}
