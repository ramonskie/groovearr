package metadata

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
)

// MetadataResolver enriches partial track metadata at queue time
// by querying configured metadata providers (MusicBrainz, CoverArtArchive, etc.).
// It is non-fatal: errors are logged as warnings and enrichment failures
// leave fields empty rather than blocking the queue pipeline.
type MetadataResolver struct {
	registry      *Registry
	providerOrder []string // priority order for provider queries
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

// SetProviderOrder configures the priority order for metadata provider queries.
func (r *MetadataResolver) SetProviderOrder(order []string) {
	r.providerOrder = order
}

// orderedProviders returns configured providers sorted by providerOrder.
func (r *MetadataResolver) orderedProviders() []Provider {
	providers := r.registry.Available()
	if len(r.providerOrder) == 0 {
		return providers
	}
	// Index providers by name.
	byName := make(map[string]Provider, len(providers))
	for _, p := range providers {
		byName[p.Name()] = p
	}
	// Build ordered list.
	var ordered []Provider
	seen := make(map[string]bool)
	for _, name := range r.providerOrder {
		if p, ok := byName[name]; ok && !seen[name] {
			ordered = append(ordered, p)
			seen[name] = true
		}
	}
	// Append any remaining providers (not in the order list).
	for _, p := range providers {
		if !seen[p.Name()] {
			ordered = append(ordered, p)
		}
	}
	return ordered
}

// EnrichMetadata completes partial metadata by querying configured providers.
// When album is empty, uses metadata providers to find the album name from
// artist+title before searching for cover art.
//
// Enrichment logic:
//   - If album is empty, queries each configured provider for the album name.
//     Tries full artist first, then primary artist (before first comma) as fallback.
//   - If album is non-empty (after lookup), queries providers for cover art.
//   - All provider errors are logged at warn level — best-effort enrichment.
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

	providers := r.orderedProviders()

	// Phase 1: find album name if missing.
	// Spotify free mode returns comma-separated artist strings like
	// "The Moon, DJ Ghost" or "Yeah Yeah Yeahs, A-Trak".
	// Try full artist first, then primary artist as fallback.
	if album == "" {
		for _, p := range providers {
			if found := p.SearchAlbum(ctx, artist, title); found != "" {
				result.Album = found
				break
			}
			// Fallback: try just the primary artist (before first comma).
			if primary := primaryArtist(artist); primary != artist {
				if found := p.SearchAlbum(ctx, primary, title); found != "" {
					result.Album = found
					break
				}
			}
		}
	}

	// Phase 2: find cover art (requires artist + album).
	// Try full artist first, then primary artist fallback.
	if result.Album == "" {
		return result, nil
	}

	for _, p := range providers {
		cover, err := p.SearchCover(ctx, artist, result.Album)
		if err != nil {
			r.log.Warn("metadata resolver: cover search failed, trying next provider",
				"provider", p.Name(), "artist", artist, "album", result.Album, "error", err,
			)
			// Fallback: try primary artist.
			if primary := primaryArtist(artist); primary != artist {
				if cover2, err2 := p.SearchCover(ctx, primary, result.Album); err2 == nil && cover2 != nil && cover2.ImageURL != "" {
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
		if primary := primaryArtist(artist); primary != artist {
			if cover2, err2 := p.SearchCover(ctx, primary, result.Album); err2 == nil && cover2 != nil && cover2.ImageURL != "" {
				result.CoverURL = cover2.ImageURL
				return result, nil
			}
		}
	}

	return result, nil
}

// primaryArtist returns the first segment before a comma (e.g., "The Moon, DJ Ghost" → "The Moon").
func primaryArtist(artist string) string {
	if idx := strings.Index(artist, ","); idx > 0 {
		return strings.TrimSpace(artist[:idx])
	}
	return artist
}
