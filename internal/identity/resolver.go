// Package identity resolves parked unmatched albums from track identity
// (provider track id / ISRC / title+artist+duration) instead of album-title
// search. It is the identity-first counterpart to the album-search refresh
// engine.
//
// The resolver is provider-neutral: it consumes the optional
// discovery.TrackIdentityProvider capability and the shared provider order and
// cooldown bucket. High-confidence releases are materialized through the
// refresh engine's MaterializeResolved write path; ambiguous candidates stay
// parked for the review UI.
package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// Library is the slice of the music library the resolver reads and the one
// duration write it performs. Declared here (not imported as library.Store) so
// identity depends on behavior, not a concrete store (AGENTS §3).
type Library interface {
	GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error)
	UpdateTrackDuration(ctx context.Context, id, durationMs int64) error
}

// Catalog is the parked-album read and queue cleanup the resolver needs.
// *catalog/sqlite.Store satisfies it.
type Catalog interface {
	ListUnmatchedAlbums(ctx context.Context, limit int) ([]catalog.UnmatchedAlbum, error)
	GetUnmatchedAlbum(ctx context.Context, albumID int64) (*catalog.UnmatchedAlbum, error)
	GetAlbumMetadata(ctx context.Context, albumID int64) (*catalog.AlbumMetadata, error)
	DeleteRefreshByEntity(ctx context.Context, entityType catalog.EntityType, entityID int64) error
}

// Materializer writes a resolved release through the refresh engine's normal
// catalog write path. *refresh.Service satisfies it.
type Materializer interface {
	MaterializeResolved(ctx context.Context, albumID int64, providerName, providerAlbumID string, tracks []discovery.TrackInfo, partial bool) (applied bool, err error)
}

// Cooldown is the shared per-provider rate-limit bucket (AGENTS §8).
// *metadata.ProviderCooldown satisfies it.
type Cooldown interface {
	CoolingDown(name string) bool
	MarkAfter(name string, retryAfter time.Duration)
}

// DurationProbe reads a file's duration. library.ProbeDuration is the
// production implementation; tests inject a fake.
type DurationProbe func(ctx context.Context, path string) (time.Duration, error)

// Defaults for one resolve pass.
const (
	defaultLimit             = 100000
	defaultDurationTolerance = 5 * time.Second
	defaultCallTimeout       = 30 * time.Second
)

// Options configures one resolver. Zero values fall back to the defaults.
type Options struct {
	// Limit caps the parked albums processed in one pass.
	Limit int
	// DurationTolerance is the caller-side tolerance for "same recording".
	DurationTolerance time.Duration
	// CallTimeout bounds each provider call (AGENTS §7).
	CallTimeout time.Duration
	// ProbeDuration overrides library.ProbeDuration (tests).
	ProbeDuration DurationProbe
}

func (o Options) withDefaults() Options {
	if o.Limit <= 0 {
		o.Limit = defaultLimit
	}
	if o.DurationTolerance <= 0 {
		o.DurationTolerance = defaultDurationTolerance
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = defaultCallTimeout
	}
	if o.ProbeDuration == nil {
		o.ProbeDuration = library.ProbeDuration
	}
	return o
}

// Progress is one album completion event for the job runner.
type Progress struct {
	Done     int
	Total    int
	Resolved int
	Parked   int
	AlbumID  int64
}

// Result summarizes one pass.
type Result struct {
	Total      int
	Resolved   int
	Parked     int
	NoIdentity int
	Errors     int
	// Skipped counts albums that were no longer unmatched by the time the
	// resolver reached them (the refresh engine resolved them concurrently).
	Skipped int
	// CoolingDown is true when every provider with the identity capability is
	// currently parked on the shared cooldown bucket. Nothing was processed.
	CoolingDown bool
}

// ErrNoTrackProviders means no configured discovery provider exposes the
// TrackIdentityProvider capability, so an identity pass cannot run at all.
var ErrNoTrackProviders = errors.New("identity: no track identity providers available")

// UnmatchedResolver is the behavior the recheck-unmatched job drives.
type UnmatchedResolver interface {
	ResolveUnmatched(ctx context.Context, report func(Progress)) (Result, error)
}

// Resolver is the identity-first unmatched-album resolver.
type Resolver struct {
	lib          Library
	catalog      Catalog
	materializer Materializer
	providers    func() []discovery.Provider
	order        *metadata.ProviderOrder
	cooldown     Cooldown
	log          *slog.Logger
	opts         Options
}

// New constructs a resolver. providers is called on every pass so a provider
// configured after startup is picked up without a restart. log may be nil.
func New(lib Library, cat Catalog, materializer Materializer, providers func() []discovery.Provider,
	order *metadata.ProviderOrder, cooldown Cooldown, log *slog.Logger, opts Options) *Resolver {
	if providers == nil {
		providers = func() []discovery.Provider { return nil }
	}
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{
		lib:          lib,
		catalog:      cat,
		materializer: materializer,
		providers:    providers,
		order:        order,
		cooldown:     cooldown,
		log:          log,
		opts:         opts.withDefaults(),
	}
}

// ResolveUnmatched processes every parked unmatched album. Per-album provider
// failures are counted and skipped; the pass returns an error only for a
// catalog read failure or context cancellation.
func (r *Resolver) ResolveUnmatched(ctx context.Context, report func(Progress)) (Result, error) {
	albums, err := r.catalog.ListUnmatchedAlbums(ctx, r.opts.Limit)
	if err != nil {
		return Result{}, fmt.Errorf("identity: list unmatched albums: %w", err)
	}
	result := Result{Total: len(albums)}
	if len(albums) == 0 {
		return result, nil
	}
	providers, capable := r.trackProviders()
	if capable == 0 {
		return result, ErrNoTrackProviders
	}
	if len(providers) == 0 {
		result.Parked = len(albums)
		result.CoolingDown = true
		r.log.Warn("identity resolve skipped: every identity provider is cooling down", "albums", len(albums), "component", "identity")
		return result, nil
	}

	probeEnabled := true
	for i, album := range albums {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		outcome, err := r.resolveAlbum(ctx, album, providers, &probeEnabled)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		switch {
		case err != nil:
			// A canceled parent context is a pass cancellation, not an
			// album-level failure; surface it so the job reports canceled.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			result.Errors++
			r.log.Warn("identity resolve album failed", "album_id", album.AlbumID, "error", err, "component", "identity")
		case outcome.resolved:
			result.Resolved++
		case outcome.skipped:
			result.Skipped++
		default:
			result.Parked++
			if !outcome.hadCandidate {
				result.NoIdentity++
			}
		}
		if report != nil {
			report(Progress{
				Done:     i + 1,
				Total:    len(albums),
				Resolved: result.Resolved,
				Parked:   result.Parked,
				AlbumID:  album.AlbumID,
			})
		}
	}
	return result, nil
}

// trackProviders returns the ordered providers that expose the identity
// capability and are not currently cooling down, plus the number of capable
// providers regardless of cooldown (so the caller can distinguish "not
// configured" from "temporarily parked").
func (r *Resolver) trackProviders() ([]discovery.TrackIdentityProvider, int) {
	ordered := metadata.Order(r.order, r.providers())
	out := make([]discovery.TrackIdentityProvider, 0, len(ordered))
	capable := 0
	for _, p := range ordered {
		tp, ok := p.(discovery.TrackIdentityProvider)
		if !ok {
			continue
		}
		if avail, ok := tp.(discovery.IdentityAvailability); ok && !avail.IdentityAvailable() {
			continue
		}
		capable++
		if r.cooldown != nil && r.cooldown.CoolingDown(p.Name()) {
			continue
		}
		out = append(out, tp)
	}
	return out, capable
}

type albumOutcome struct {
	resolved     bool
	hadCandidate bool
	skipped      bool
}

func (r *Resolver) resolveAlbum(ctx context.Context, album catalog.UnmatchedAlbum,
	providers []discovery.TrackIdentityProvider, probeEnabled *bool) (albumOutcome, error) {
	meta, err := r.catalog.GetAlbumMetadata(ctx, album.AlbumID)
	if err != nil {
		return albumOutcome{}, err
	}
	if meta != nil && meta.RefreshStatus != catalog.StatusUnmatched {
		return albumOutcome{skipped: true}, nil
	}
	tracks, err := r.lib.GetTracksByAlbum(ctx, album.AlbumID)
	if err != nil {
		return albumOutcome{}, err
	}
	if len(tracks) == 0 {
		return albumOutcome{skipped: true}, nil
	}
	if *probeEnabled {
		if err := r.probeDurations(ctx, tracks, probeEnabled); err != nil {
			return albumOutcome{}, err
		}
	}
	if len(tracks) == 1 {
		return r.resolveSingle(ctx, album, tracks[0], providers)
	}
	return r.resolveRelease(ctx, album, tracks, providers)
}

// probeDurations fills the missing duration on the album's tracks only. A
// missing ffprobe disables probing for the rest of the pass (one warning), not
// the whole resolve run.
func (r *Resolver) probeDurations(ctx context.Context, tracks []domain.Track, enabled *bool) error {
	for i := range tracks {
		if !*enabled {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if tracks[i].Duration > 0 || strings.TrimSpace(tracks[i].FilePath) == "" {
			continue
		}
		d, err := r.opts.ProbeDuration(ctx, tracks[i].FilePath)
		// A per-file ffprobe timeout is not a pass cancellation; only the
		// parent context ending aborts the pass. Check it explicitly so a
		// canceled job is reported as canceled, not as "everything parked".
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if errors.Is(err, library.ErrDurationUnavailable) {
				*enabled = false
				r.log.Warn("identity duration probe unavailable; duration stays unknown", "component", "identity")
				return nil
			}
			r.log.Debug("identity duration probe failed", "track_id", tracks[i].ID, "error", err, "component", "identity")
			continue
		}
		ms := d.Milliseconds()
		if ms <= 0 {
			continue
		}
		if err := r.lib.UpdateTrackDuration(ctx, tracks[i].ID, ms); err != nil {
			r.log.Warn("identity duration write failed", "track_id", tracks[i].ID, "error", err, "component", "identity")
			continue
		}
		tracks[i].Duration = ms
	}
	return nil
}

func (r *Resolver) resolveSingle(ctx context.Context, album catalog.UnmatchedAlbum,
	track domain.Track, providers []discovery.TrackIdentityProvider) (albumOutcome, error) {
	candidates := r.candidates(ctx, album, track, 1, providers)
	if len(candidates) == 0 {
		return albumOutcome{}, nil
	}
	eligible := candidates[:0]
	for _, c := range candidates {
		if autoAllowed(c) {
			eligible = append(eligible, c)
		}
	}
	best := pickBest(eligible)
	if best == nil {
		return albumOutcome{hadCandidate: true}, nil
	}
	id := best.identity
	// A 1-track local release keeps exactly one provider row. Mark it partial
	// only when the provider release is known to have more than one track, so a
	// true single (or an unknown count) stays refreshable.
	partial := id.AlbumTrackCount > 1
	applied, err := r.materializer.MaterializeResolved(ctx, album.AlbumID, id.ProviderName, id.ProviderAlbumID,
		[]discovery.TrackInfo{trackInfoFromIdentity(id)}, partial)
	if err != nil {
		return albumOutcome{}, err
	}
	if !applied {
		return albumOutcome{skipped: true, hadCandidate: true}, nil
	}
	r.cleanupQueue(ctx, album.AlbumID)
	r.log.Info("identity resolved single", "album_id", album.AlbumID, "artist", album.Artist, "title", album.Title,
		"provider", id.ProviderName, "provider_track_id", id.ProviderTrackID, "partial", partial, "component", "identity")
	return albumOutcome{resolved: true, hadCandidate: true}, nil
}

// groupKey identifies one provider release.
type groupKey struct {
	provider string
	albumID  string
}

func (r *Resolver) resolveRelease(ctx context.Context, album catalog.UnmatchedAlbum,
	tracks []domain.Track, providers []discovery.TrackIdentityProvider) (albumOutcome, error) {
	type group struct {
		key     groupKey
		rank    int
		byTrack map[int64]scoredCandidate
	}
	groupMinScore := func(g *group) int {
		min := 101
		for _, c := range g.byTrack {
			if c.score < min {
				min = c.score
			}
		}
		return min
	}
	// groupTrackCount returns the release track count reported by every
	// candidate in the group. Zero (unknown) or inconsistent counts are not
	// equivalence evidence: a standard and a deluxe release must not be merged
	// just because one provider omitted the count.
	groupTrackCount := func(g *group) int {
		count := 0
		for _, c := range g.byTrack {
			n := c.identity.AlbumTrackCount
			if n <= 0 {
				continue
			}
			if count == 0 {
				count = n
				continue
			}
			if count != n {
				return -1
			}
		}
		return count
	}
	equivalentGroups := func(a, b *group) bool {
		if len(a.byTrack) != len(b.byTrack) {
			return false
		}
		// A standard release and a deluxe release can cover the same local
		// tracks but are not the same release. Both providers must report the
		// same known track count; unknown or inconsistent counts stay parked.
		caCount, cbCount := groupTrackCount(a), groupTrackCount(b)
		if caCount <= 0 || cbCount <= 0 || caCount != cbCount {
			return false
		}
		for trackID, ca := range a.byTrack {
			cb, ok := b.byTrack[trackID]
			if !ok || !equivalentRecordings(ca.identity, cb.identity) {
				return false
			}
		}
		return true
	}
	groups := make(map[groupKey]*group)
	anyCandidate := false

	for _, track := range tracks {
		candidates := r.candidates(ctx, album, track, len(tracks), providers)
		if len(candidates) == 0 {
			return albumOutcome{hadCandidate: anyCandidate}, nil
		}
		anyCandidate = true
		for _, c := range candidates {
			if !autoAllowed(c) || c.identity.ProviderAlbumID == "" {
				continue
			}
			key := groupKey{provider: c.identity.ProviderName, albumID: c.identity.ProviderAlbumID}
			g := groups[key]
			if g == nil {
				g = &group{key: key, rank: c.rank, byTrack: make(map[int64]scoredCandidate, len(tracks))}
				groups[key] = g
			} else if c.rank < g.rank {
				g.rank = c.rank
			}
			// Keep the strongest candidate for this local track within the
			// provider release.
			if prev, ok := g.byTrack[track.ID]; !ok || c.score > prev.score {
				g.byTrack[track.ID] = c
			}
		}
	}

	// Only complete groups are eligible. Sort by the weakest link (score), then
	// by provider order. Equal-score groups are accepted only when every local
	// track is the same recording across both releases; otherwise the tie is a
	// review case.
	complete := make([]*group, 0, len(groups))
	for _, g := range groups {
		if len(g.byTrack) == len(tracks) {
			complete = append(complete, g)
		}
	}
	if len(complete) == 0 {
		return albumOutcome{hadCandidate: anyCandidate}, nil
	}
	sort.SliceStable(complete, func(i, j int) bool {
		si, sj := groupMinScore(complete[i]), groupMinScore(complete[j])
		if si != sj {
			return si > sj
		}
		if complete[i].rank != complete[j].rank {
			return complete[i].rank < complete[j].rank
		}
		return complete[i].key.albumID < complete[j].key.albumID
	})
	best := complete[0]
	bestMin := groupMinScore(best)
	for _, g := range complete[1:] {
		if groupMinScore(g) != bestMin {
			break
		}
		if !equivalentGroups(best, g) {
			// Two complete releases are equally plausible but describe
			// different recordings; leave it for the review UI.
			return albumOutcome{hadCandidate: anyCandidate}, nil
		}
	}

	provider := findProvider(providers, best.key.provider)
	if provider == nil || best.key.albumID == "" {
		return albumOutcome{hadCandidate: anyCandidate}, nil
	}
	if r.cooldown != nil && r.cooldown.CoolingDown(best.key.provider) {
		// The provider was parked after this album's candidates were scored;
		// do not re-probe a throttled API just to fetch the tracklist.
		return albumOutcome{hadCandidate: anyCandidate}, nil
	}
	full, err := r.fetchAlbumTracks(ctx, provider, best.key.albumID)
	if err != nil {
		r.handleProviderError(best.key.provider, err)
		return albumOutcome{hadCandidate: anyCandidate}, nil
	}
	if len(full) == 0 {
		return albumOutcome{hadCandidate: anyCandidate}, nil
	}
	applied, err := r.materializer.MaterializeResolved(ctx, album.AlbumID, best.key.provider, best.key.albumID, full, false)
	if err != nil {
		return albumOutcome{}, err
	}
	if !applied {
		return albumOutcome{skipped: true, hadCandidate: true}, nil
	}
	r.cleanupQueue(ctx, album.AlbumID)
	r.log.Info("identity resolved release", "album_id", album.AlbumID, "artist", album.Artist, "title", album.Title,
		"provider", best.key.provider, "provider_album_id", best.key.albumID, "tracks", len(full), "component", "identity")
	return albumOutcome{resolved: true, hadCandidate: true}, nil
}

func findProvider(providers []discovery.TrackIdentityProvider, name string) discovery.TrackIdentityProvider {
	for _, p := range providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

func (r *Resolver) getTrackIdentity(ctx context.Context, p discovery.TrackIdentityProvider, id string) (*discovery.TrackIdentity, error) {
	cctx, cancel := context.WithTimeout(ctx, r.opts.CallTimeout)
	defer cancel()
	return p.GetTrackIdentity(cctx, id)
}

func (r *Resolver) lookupTrackByISRC(ctx context.Context, p discovery.TrackIdentityProvider, isrc string) (*discovery.TrackIdentity, error) {
	cctx, cancel := context.WithTimeout(ctx, r.opts.CallTimeout)
	defer cancel()
	return p.LookupTrackByISRC(cctx, isrc)
}

// fetchAlbumTracks only needs the base discovery capability: GetAlbumTracks is
// part of discovery.Provider, so a release match works even for providers that
// do not expose track-identity lookups (the manual search fallback).
func (r *Resolver) fetchAlbumTracks(ctx context.Context, p discovery.Provider, albumID string) ([]discovery.TrackInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, r.opts.CallTimeout)
	defer cancel()
	return p.GetAlbumTracks(cctx, albumID)
}

// handleProviderError parks a rate-limited provider on the shared bucket and
// logs the rest at debug; a per-provider failure must not fail the whole pass.
// It reports whether the provider is now parked (so the caller can skip the
// rest of that provider's lookups for this track).
func (r *Resolver) handleProviderError(name string, err error) bool {
	if err == nil {
		return false
	}
	var rle *metadata.RateLimitError
	if errors.As(err, &rle) {
		if r.cooldown != nil {
			r.cooldown.MarkAfter(name, rle.RetryAfter)
		}
		r.log.Warn("identity provider rate limited", "provider", name, "retry_after", rle.RetryAfter, "component", "identity")
		return true
	}
	if errors.Is(err, metadata.ErrRateLimited) {
		if r.cooldown != nil {
			r.cooldown.MarkAfter(name, 0)
		}
		r.log.Warn("identity provider rate limited", "provider", name, "component", "identity")
		return true
	}
	r.log.Debug("identity provider lookup failed", "provider", name, "error", err, "component", "identity")
	return false
}

func (r *Resolver) cleanupQueue(ctx context.Context, albumID int64) {
	if err := r.catalog.DeleteRefreshByEntity(ctx, catalog.EntityAlbum, albumID); err != nil {
		r.log.Warn("identity queue cleanup failed", "album_id", albumID, "error", err, "component", "identity")
	}
}
