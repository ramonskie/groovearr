package metadata

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// Cooldown is the narrow rate-limit surface the resolver needs: the app's
// single shared *ProviderCooldown satisfies it (AGENTS §8). The interface is
// declared here so the resolver depends on the capability, not the concrete
// bucket, which also makes it trivially testable with a fake.
type Cooldown interface {
	// CoolingDown reports whether name is currently parked after a rate-limit.
	CoolingDown(name string) bool
	// MarkAfter parks name for at least d (0 = provider default), honoring a
	// server-requested backoff when it is longer.
	MarkAfter(name string, d time.Duration)
}

// MetadataResolver enriches partial track metadata at queue time
// by querying configured metadata providers (MusicBrainz, CoverArtArchive, etc.).
// It is non-fatal: errors are logged as warnings and enrichment failures
// leave fields empty rather than blocking the queue pipeline.
type MetadataResolver struct {
	registry      *Registry
	providerOrder *ProviderOrder // shared live source for provider priority
	cooldown      Cooldown       // shared rate-limit bucket (AGENTS §8); nil = no gating
	log           *slog.Logger
}

// NewMetadataResolver creates a resolver that uses configured metadata providers
// from the given registry. The registry must already be populated via InitAll.
// providerOrder specifies the priority order for querying providers (e.g., ["deezer-meta", "musicbrainz"]).
// Providers not listed appear after listed ones. Pass nil to use registration order.
func NewMetadataResolver(registry *Registry, logger *slog.Logger) *MetadataResolver {
	return &MetadataResolver{
		registry: registry,
		log:      logger,
	}
}

// SetProviderOrder configures the shared live source for the metadata provider
// priority order. Read on each query so runtime config changes apply without
// a restart. Pass nil to fall back to registration order.
func (r *MetadataResolver) SetProviderOrder(order *ProviderOrder) {
	r.providerOrder = order
}

// SetCooldown wires the app's single shared provider rate-limit bucket so the
// resolver skips a cooled-down provider and parks one that returns
// metadata.ErrRateLimited (AGENTS §8 — no caller keeps its own rate-limit
// state and no code goes around the bucket). Pass nil to disable gating.
func (r *MetadataResolver) SetCooldown(c Cooldown) {
	r.cooldown = c
}

// coolingDown reports whether name is currently parked after a rate-limit. A
// nil bucket (no gating wired) always returns false.
func (r *MetadataResolver) coolingDown(name string) bool {
	return r.cooldown != nil && r.cooldown.CoolingDown(name)
}

// markRateLimited parks name on the shared bucket when err wraps
// metadata.ErrRateLimited, honoring a server-requested Retry-After. It returns
// true when err was a rate-limit, so the caller can stop probing that provider
// for this track instead of immediately re-hammering it. No-op (false) for a
// nil bucket, nil err, or a non-rate-limit error.
func (r *MetadataResolver) markRateLimited(name string, err error) bool {
	if r.cooldown == nil || err == nil {
		return false
	}
	var rle *RateLimitError
	if errors.As(err, &rle) {
		r.cooldown.MarkAfter(name, rle.RetryAfter)
		return true
	}
	if errors.Is(err, ErrRateLimited) {
		r.cooldown.MarkAfter(name, 0)
		return true
	}
	return false
}

// OrderedProviders returns configured providers sorted by providerOrder. It
// delegates to the shared metadata.Order helper so the priority rule lives in
// one place (order.go): providers named in the order come first in that order;
// every remaining provider follows in registration order. A nil ProviderOrder
// or empty order yields registration order unchanged.
func (r *MetadataResolver) OrderedProviders() []Provider {
	return Order(r.providerOrder, r.registry.Available())
}

// EnrichMetadata completes partial metadata by querying configured providers.
// When album is empty, uses metadata providers to find the album name from
// artist+title before searching for cover art.
//
// Enrichment logic:
//   - If album is empty, queries each configured provider for the album name.
//     Tries full artist first, then primary artist (before first comma) as fallback.
//   - If album is non-empty (after lookup), queries providers for cover art.
//   - Providers parked by the shared rate-limit bucket (AGENTS §8) are skipped,
//     and a provider that returns metadata.ErrRateLimited is parked there.
//   - Enrichment is best-effort: a provider that fails is skipped (a cover
//     failure is also logged at warn; album-lookup providers log their own
//     failures) rather than blocking the queue pipeline.
func (r *MetadataResolver) EnrichMetadata(ctx context.Context, artist, title, album string, year int) (*domain.TrackMetadata, error) {
	result := &domain.TrackMetadata{
		Artist: artist,
		Title:  title,
		Album:  album,
		Year:   year,
	}

	if artist == "" || title == "" {
		return result, nil
	}

	providers := r.OrderedProviders()

	// Phase 1: find album name if missing.
	// Spotify free mode returns comma-separated artist strings like
	// "The Moon, DJ Ghost" or "Yeah Yeah Yeahs, A-Trak".
	// Try full artist first, then primary artist as fallback.
	if album == "" {
		for _, p := range providers {
			// AGENTS §8: never re-probe a provider the shared bucket has parked.
			if r.coolingDown(p.Name()) {
				continue
			}
			found, err := p.SearchAlbum(ctx, artist, title)
			if r.markRateLimited(p.Name(), err) {
				continue // rate-limited — skip the primary-artist retry, try the next provider
			}
			// Fallback: try just the primary artist (before first comma).
			if found == "" {
				if primary := library.PrimaryArtistName(artist); primary != artist {
					found, err = p.SearchAlbum(ctx, primary, title)
					if r.markRateLimited(p.Name(), err) {
						continue
					}
				}
			}
			if found != "" {
				result.Album = found
				break
			}
		}
	}

	// Phase 2: find cover art (requires artist + album).
	// Try full artist first, then primary artist fallback.
	if result.Album == "" {
		return result, nil
	}

	for _, p := range providers {
		// AGENTS §8: never re-probe a provider the shared bucket has parked.
		if r.coolingDown(p.Name()) {
			continue
		}
		cover, err := p.SearchCover(ctx, artist, result.Album)
		if r.markRateLimited(p.Name(), err) {
			continue // rate-limited — park and try the next provider
		}
		if err != nil {
			r.log.Warn("metadata resolver: cover search failed, trying next provider",
				"provider", p.Name(), "artist", artist, "album", result.Album, "error", err,
			)
			// Fallback: try primary artist.
			if primary := library.PrimaryArtistName(artist); primary != artist {
				cover2, err2 := p.SearchCover(ctx, primary, result.Album)
				if r.markRateLimited(p.Name(), err2) {
					continue
				}
				if err2 == nil && cover2 != nil && cover2.ImageURL != "" {
					result.CoverURL = cover2.ImageURL
					return result, nil
				}
			}
			continue
		}
		if cover != nil && cover.ImageURL != "" {
			result.CoverURL = cover.ImageURL
			return result, nil
		}
		// Cover search returned nil — try primary artist fallback.
		if primary := library.PrimaryArtistName(artist); primary != artist {
			cover2, err2 := p.SearchCover(ctx, primary, result.Album)
			if r.markRateLimited(p.Name(), err2) {
				continue
			}
			if err2 == nil && cover2 != nil && cover2.ImageURL != "" {
				result.CoverURL = cover2.ImageURL
				return result, nil
			}
		}
	}

	return result, nil
}
