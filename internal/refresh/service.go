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
	providers := s.providers()
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

// bestMatch picks the best generic match from search results: an exact
// case-insensitive artist+title match wins, then a title-only match. It applies
// no provider-specific logic.
func bestMatch(results []discovery.AlbumResult, artist, title string) (discovery.AlbumResult, bool) {
	wantTitle := normalize(title)
	if wantTitle == "" {
		return discovery.AlbumResult{}, false
	}
	wantArtist := normalize(artist)

	var titleOnly *discovery.AlbumResult
	for i := range results {
		if normalize(results[i].Title) != wantTitle {
			continue
		}
		if wantArtist != "" && normalize(results[i].ArtistName) == wantArtist {
			return results[i], true
		}
		if titleOnly == nil {
			titleOnly = &results[i]
		}
	}
	if titleOnly != nil {
		return *titleOnly, true
	}
	return discovery.AlbumResult{}, false
}

// normalize lowercases and trims a name for case-insensitive comparison.
func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
