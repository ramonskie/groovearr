package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// DuplicateScanStore is the persisted cache of duplicate-group canonical names,
// populated by the duplicates job and read by the merge handler.
type DuplicateScanStore interface {
	GetDuplicateCanonical(ctx context.Context, groupKey string) (string, bool, error)
	ListDuplicateCanonicals(ctx context.Context) (map[string]string, error)
	UpsertDuplicateCanonical(ctx context.Context, groupKey, canonical string) error
	ClearDuplicateCanonicals(ctx context.Context) error
	DeleteDuplicateCanonical(ctx context.Context, groupKey string) error
}

// Duplicates scans the library for case-insensitive duplicate artists and
// resolves each group's canonical provider spelling, persisting results to the
// duplicate_scan cache. Lookups run sequentially so MusicBrainz's 1 req/sec
// rate limit paces them naturally — no timeouts, no bursting. A cancelled or
// failed run leaves whatever groups were already resolved in the cache.
func (r *Runners) Duplicates() Runner {
	return func(ctx context.Context, report func(Report)) error {
		dss, ok := r.deps.Store.(DuplicateScanStore)
		if !ok {
			return nil
		}

		byLower := map[string][]domain.Artist{}
		for off := 0; ; off += 200 {
			artists, err := r.deps.Store.ListArtists(ctx, off, 200)
			if err != nil {
				return err
			}
			if len(artists) == 0 {
				break
			}
			for _, a := range artists {
				// Group by normalized key so both spelling variants ("Tiësto"
				// vs "Tiesto", curly apostrophes) and feat-marked rows ("2Pac
				// feat. X" vs "2Pac") of the same artist land in one group and
				// get resolved against the canonical provider spelling.
				key := library.NormalizeArtistKey(library.IdentityArtistName(a.Name))
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
			report(Report{Done: 0, Total: 1, Message: "no duplicate artists found"})
			return nil
		}

		for i, t := range targets {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			canonical, err := r.LookupCanonicalArtist(ctx, t.repName)
			if err != nil {
				// The shared cooldown handles rate-limited providers (marked
				// inside LookupCanonicalArtist, which also skips providers
				// currently cooling down). The group is persisted with an
				// empty canonical and re-attempted on the next run.
				r.deps.Log.Warn("duplicates scan: canonical lookup failed", "group", t.key, "error", err, "component", "jobs")
			}
			if err := dss.UpsertDuplicateCanonical(ctx, t.key, canonical); err != nil {
				r.deps.Log.Warn("duplicates scan: persist failed", "group", t.key, "error", err, "component", "jobs")
			}
			report(Report{Done: i + 1, Total: total, Message: t.key})
		}
		report(Report{
			Done:    total,
			Total:   total,
			Message: fmt.Sprintf("checked %d duplicate artist groups", total),
		})
		return nil
	}
}

// LookupCanonicalArtist resolves the canonical spelling for an artist using
// the first configured provider that implements ArtistNameProvider. Used by
// the duplicates job and the merge handler. Consults and marks the shared
// provider cooldown: a provider currently cooling down is skipped (no probe
// that could re-arm a longer ban), and a rate-limit response parks the
// provider app-wide for the backoff window.
func (r *Runners) LookupCanonicalArtist(ctx context.Context, name string) (string, error) {
	var lastErr error
	var rateLimitedErr error
	if r.deps.Metadata == nil {
		return "", nil
	}
	for _, p := range r.deps.Metadata.Available() {
		anp, ok := p.(metadata.ArtistNameProvider)
		if !ok {
			continue
		}
		if r.deps.RateLimit != nil && r.deps.RateLimit.CoolingDown(p.Name()) {
			continue
		}
		got, err := anp.CanonicalArtistName(ctx, name)
		if err != nil {
			r.deps.Log.Warn("artist name lookup failed", "artist", name, "provider", p.Name(), "error", err, "component", "jobs")
			if errors.Is(err, metadata.ErrRateLimited) {
				if r.deps.RateLimit != nil {
					var rl *metadata.RateLimitError
					var retryAfter time.Duration
					if errors.As(err, &rl) {
						retryAfter = rl.RetryAfter
					}
					r.deps.RateLimit.MarkAfter(p.Name(), retryAfter)
				}
				rateLimitedErr = err
			} else {
				lastErr = err
			}
			continue
		}
		if got != "" {
			return got, nil
		}
	}
	if rateLimitedErr != nil {
		return "", rateLimitedErr
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}
