package refresh

import (
	"context"
	"errors"
	"sync"
	"time"
)

// defaultColdTimeout is the hard ceiling on one cold fetch when the service is
// constructed without an explicit override. It bounds the whole materialization
// (a provider search plus its tracklist lookup), not a single provider call, so
// a cold album open can never pin an HTTP request behind a multi-provider
// search.
const defaultColdTimeout = 15 * time.Second

// errColdUnavailable is returned when the fetcher was built without a refresh
// service. The caller treats it like any other fetch failure and falls back to
// local tracks.
var errColdUnavailable = errors.New("refresh: cold fetcher not available")

// errColdDisabled is returned when the live catalog config has materialization
// disabled. The caller treats it like any other fetch failure and falls back to
// local tracks; no provider call is made.
var errColdDisabled = errors.New("refresh: cold fetch disabled")

// coldCall is one in-flight cold materialization, shared by every caller that
// opened the same album while it was running. done is closed once, after the
// result is published, so every waiter observes the same outcome.
type coldCall struct {
	done    chan struct{}
	outcome AlbumOutcome
	err     error
}

// ColdFetcher performs the Option-A cold read: the first open of an album that
// has no materialized tracklist triggers exactly one provider fetch
// (Service.RefreshAlbum), materializes the album, and returns the outcome to
// every concurrent open of that same album.
//
// Deduplication is an in-package map+mutex — golang.org/x/sync/singleflight is
// not a dependency. The fetch runs in a service-owned goroutine bounded by a
// hard timeout, so neither a slow provider nor a client disconnect can leak an
// unbounded provider call (AGENTS §7). Because it delegates to
// Service.RefreshAlbum, it inherits the shared cooldown check/mark and reaches
// providers only through discovery.Provider (AGENTS §1, §8).
type ColdFetcher struct {
	svc *Service

	// timeout is the hard ceiling on one fetch. It defaults in NewColdFetcher
	// and is a test seam.
	timeout time.Duration

	mu       sync.Mutex
	inflight map[int64]*coldCall

	// lifecycle context + wait group: when Start is called the fetcher's fetch
	// goroutines are tied to it so Stop can cancel and await them at shutdown.
	// A fetcher never started falls back to a background context, exactly as
	// before. stopped rejects fetches after Stop so no goroutine is added after
	// Stop begins waiting.
	ctx     context.Context
	cancel  context.CancelFunc
	stopped bool
	wg      sync.WaitGroup
}

// NewColdFetcher wires a cold fetcher around the refresh service. A nil service
// yields a fetcher whose Fetch reports errColdUnavailable, so a Server built
// without one degrades cleanly. The hard timeout defaults to defaultColdTimeout;
// a positive service CallTimeout raises it to the engine's own per-call budget
// when that is longer, so a cold fetch is never stricter than a single call.
func NewColdFetcher(svc *Service) *ColdFetcher {
	timeout := defaultColdTimeout
	if svc != nil && svc.opts.CallTimeout > timeout {
		timeout = svc.opts.CallTimeout
	}
	return &ColdFetcher{
		svc:      svc,
		timeout:  timeout,
		inflight: make(map[int64]*coldCall),
	}
}

// Start binds the fetcher's in-flight goroutines to ctx so Stop can cancel and
// await them. It is optional: a fetcher that is never started runs its cold
// fetches on a background context, exactly as before. Starting twice supersedes
// the previous lifecycle. A nil receiver is a no-op.
func (c *ColdFetcher) Start(ctx context.Context) {
	if c == nil {
		return
	}
	lctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.ctx = lctx
	c.cancel = cancel
	c.stopped = false
	c.mu.Unlock()
}

// Stop cancels the lifecycle context, rejects further fetches, and waits for
// every in-flight cold fetch goroutine to exit, so cold work cannot outlive the
// store it writes through (AGENTS §7). It is safe to call when the fetcher was
// never started or already stopped; a nil receiver is a no-op.
func (c *ColdFetcher) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.ctx = nil
	c.stopped = true
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
}

// Fetch runs — or joins — one cold materialization for albumID. The first
// caller starts the fetch; every caller that arrives while it is in flight
// shares the single result instead of issuing a second provider call, so a
// thundering herd of concurrent opens produces exactly one provider fetch.
//
// The shared fetch is bounded by the fetcher's hard timeout. A caller whose own
// context ends first returns ctx.Err() promptly so it can fall back to local
// tracks; that does not cancel the shared work, whose result is still published
// to any other waiter.
func (c *ColdFetcher) Fetch(ctx context.Context, albumID int64) (AlbumOutcome, error) {
	if c == nil || c.svc == nil {
		return AlbumOutcome{}, errColdUnavailable
	}
	// A disabled catalog is a live-config state (AGENTS §9): skip the provider
	// fetch entirely so the handler falls back to local tracks.
	if !c.svc.enabled() {
		return AlbumOutcome{}, errColdDisabled
	}

	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return AlbumOutcome{}, errColdUnavailable
	}
	if call, ok := c.inflight[albumID]; ok {
		c.mu.Unlock()
		return c.wait(ctx, call)
	}
	base := c.ctx
	if base == nil {
		base = context.Background()
	}
	call := &coldCall{done: make(chan struct{})}
	c.inflight[albumID] = call
	c.wg.Add(1)
	c.mu.Unlock()

	go c.run(base, albumID, call)
	return c.wait(ctx, call)
}

// FetchMatched is the api-facing projection of Fetch: it reports only whether a
// provider album was resolved and materialized, so the api package can depend
// on a narrow interface that names no refresh type (AGENTS §3).
func (c *ColdFetcher) FetchMatched(ctx context.Context, albumID int64) (bool, error) {
	outcome, err := c.Fetch(ctx, albumID)
	return outcome.Matched, err
}

// run performs the shared fetch in a service-owned goroutine, detached from any
// single caller's context and bounded by the hard timeout. It runs on the
// fetcher's lifecycle context (base) so Stop can cancel it, and is tracked by the
// wait group so Stop can await it. It publishes the result before removing the
// in-flight entry, so a caller that races the completion either joins the
// published result or starts a fresh fetch — never a duplicate call for the same
// in-flight open.
func (c *ColdFetcher) run(base context.Context, albumID int64, call *coldCall) {
	defer c.wg.Done()
	fctx, cancel := context.WithTimeout(base, c.timeout)
	defer cancel()

	outcome, err := c.svc.RefreshAlbum(fctx, albumID)

	c.mu.Lock()
	call.outcome, call.err = outcome, err
	close(call.done)
	delete(c.inflight, albumID)
	c.mu.Unlock()
}

// wait blocks until the shared fetch completes or the caller's own context
// ends. A context deadline/cancellation returns promptly so the caller can fall
// back; the shared fetch keeps running to its own bound.
func (c *ColdFetcher) wait(ctx context.Context, call *coldCall) (AlbumOutcome, error) {
	select {
	case <-call.done:
		return call.outcome, call.err
	case <-ctx.Done():
		return AlbumOutcome{}, ctx.Err()
	}
}
