package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// ReleaseResolver is a provider capability that resolves an album's expected
// tracklist to a concrete release. It lets an album-oriented caller (e.g. a
// torrent indexer) obtain an authoritative track listing WITHOUT importing the
// metadata provider's concrete package — the caller depends only on this
// interface (AGENTS §1: no provider→provider concrete imports).
//
// It sits beside Provider: a provider that can resolve release tracklists
// implements this in addition to (or instead of) Provider. Registry lookup
// still happens by plugin name/capability; a caller type-asserts a plugin to
// this interface to reach the resolver.
type ReleaseResolver interface {
	plugin.BasePlugin

	// ResolveTracksForCount returns the expected tracklist for an album.
	//
	// fileCount and torrentTitle are optional hints: when fileCount > 0 the
	// resolver picks the release whose track count is closest, using word
	// overlap with torrentTitle to break ties. When fileCount <= 0 it falls
	// back to a default release.
	//
	// The returned MBID identifies the resolved release (not release group)
	// and is empty when unresolved. Tracks are neutral discovery.TrackInfo
	// values so no provider-specific type crosses this boundary.
	ResolveTracksForCount(ctx context.Context, artist, album string, fileCount int, torrentTitle string) ([]TrackInfo, string, error)
}

// ReleaseResolverConsumer is implemented by a plugin that needs an injected
// ReleaseResolver. The composition root (app.go) resolves one resolver from the
// configured plugins and injects it into every consumer AFTER construction, so
// provider build order cannot leave a consumer without a resolver and the
// factory never has to name a sibling provider (AGENTS §1).
type ReleaseResolverConsumer interface {
	SetReleaseResolver(ReleaseResolver)
}

// Cooldown is the narrow rate-limit surface the release-resolver decorator
// needs: the app's single shared *metadata.ProviderCooldown satisfies it
// (AGENTS §8). The interface is declared here so discovery depends on the
// capability, not the concrete bucket, which also makes the decorator
// trivially testable with a fake.
type Cooldown interface {
	// CoolingDown reports whether name is currently parked after a rate-limit.
	CoolingDown(name string) bool
	// MarkAfter parks name for at least retryAfter (0 = provider default),
	// honoring a server-requested backoff when it is longer.
	MarkAfter(name string, d time.Duration)
}

// cooldownReleaseResolver decorates a ReleaseResolver with the shared
// per-provider cooldown. Without it, the album-import → prowlarr → MusicBrainz
// ResolveTracksForCount path called the provider with no CoolingDown gate and
// never marked a 429 app-wide, so a rate-limited provider kept being hit
// (D-001 §4 / AGENTS §8). The decorator closes that gap without the resolver
// implementation knowing the bucket exists.
type cooldownReleaseResolver struct {
	inner    ReleaseResolver
	cooldown Cooldown
}

// NewCooldownReleaseResolver wraps inner so every resolve consults the shared
// cooldown: a provider already cooling down is skipped with a clear error (so
// the caller moves on instead of hammering it), and a resolve that returns
// metadata.ErrRateLimited parks the provider for the server's Retry-After (or
// the default) via MarkAfter. A nil cooldown makes it a transparent
// pass-through, so callers that do not wire a bucket keep working. A nil inner
// yields a nil resolver, matching the "no resolver available" contract.
func NewCooldownReleaseResolver(inner ReleaseResolver, cooldown Cooldown) ReleaseResolver {
	if inner == nil {
		return nil
	}
	return &cooldownReleaseResolver{inner: inner, cooldown: cooldown}
}

// Name delegates to the wrapped resolver so the shared bucket keys on the real
// provider name (e.g. "musicbrainz"), never the decorator.
func (d *cooldownReleaseResolver) Name() string { return d.inner.Name() }

func (d *cooldownReleaseResolver) DisplayName() string { return d.inner.DisplayName() }
func (d *cooldownReleaseResolver) IsConfigured() bool  { return d.inner.IsConfigured() }
func (d *cooldownReleaseResolver) Connected() bool     { return d.inner.Connected() }

func (d *cooldownReleaseResolver) CapabilityStatus() map[string]string {
	return d.inner.CapabilityStatus()
}

func (d *cooldownReleaseResolver) CheckConnection(ctx context.Context) error {
	return d.inner.CheckConnection(ctx)
}

// ResolveTracksForCount consults the shared cooldown before delegating and
// marks it after a rate-limited result. A cooled-down provider is short-
// circuited with an error rather than a provider call, so a bulk album import
// cannot re-probe a throttled API.
func (d *cooldownReleaseResolver) ResolveTracksForCount(ctx context.Context, artist, album string, fileCount int, torrentTitle string) ([]TrackInfo, string, error) {
	name := d.inner.Name()
	if d.cooldown != nil && d.cooldown.CoolingDown(name) {
		return nil, "", fmt.Errorf("discovery: release resolver %q is rate-limited (cooling down), skipping track resolution", name)
	}
	tracks, mbid, err := d.inner.ResolveTracksForCount(ctx, artist, album, fileCount, torrentTitle)
	if err != nil {
		markRateLimited(d.cooldown, name, err)
	}
	return tracks, mbid, err
}

// markRateLimited parks name on the shared cooldown when err is (or wraps)
// metadata.ErrRateLimited. It reads the structured RetryAfter via errors.As so
// a server-requested backoff is honored, falling back to the default cooldown
// for a bare sentinel.
func markRateLimited(cooldown Cooldown, name string, err error) {
	if cooldown == nil {
		return
	}
	var rle *metadata.RateLimitError
	if errors.As(err, &rle) {
		cooldown.MarkAfter(name, rle.RetryAfter)
		return
	}
	if errors.Is(err, metadata.ErrRateLimited) {
		cooldown.MarkAfter(name, 0)
	}
}
