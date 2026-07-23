package metadata

import (
	"context"
	"log/slog"

	"github.com/ramonskie/groovearr/internal/domain"
)

// MetadataResolver enriches partial track metadata at queue time
// by querying configured metadata providers (MusicBrainz, CoverArtArchive, etc.).
// It is non-fatal: errors are logged as warnings and enrichment failures
// leave fields empty rather than blocking the queue pipeline.
type MetadataResolver struct {
	registry *Registry
	log      *slog.Logger
}

// NewMetadataResolver creates a resolver that uses configured metadata providers
// from the given registry. The registry must already be populated via InitAll.
func NewMetadataResolver(registry *Registry, logger *slog.Logger) *MetadataResolver {
	return &MetadataResolver{
		registry: registry,
		log:      logger,
	}
}

// EnrichMetadata completes partial metadata by querying configured providers.
// When album is empty, uses metadata providers to find the album name from
// artist+title before searching for cover art.
//
// Enrichment logic:
//   - If album is empty, queries each configured provider for the album name.
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

	providers := r.registry.Configured()

	// Phase 1: find album name if missing.
	if album == "" {
		for _, p := range providers {
			if found := p.SearchAlbum(ctx, artist, title); found != "" {
				result.Album = found
				break
			}
		}
	}

	// Phase 2: find cover art (requires artist + album).
	if result.Album == "" {
		return result, nil
	}

	for _, p := range providers {
		cover, err := p.SearchCover(ctx, artist, result.Album)
		if err != nil {
			r.log.Warn("metadata resolver: cover search failed, trying next provider",
				"provider", p.Name(), "artist", artist, "album", result.Album, "error", err,
			)
			continue
		}
		if cover != nil && cover.ImageURL != "" {
			result.CoverURL = cover.ImageURL
			return result, nil
		}
	}

	return result, nil
}
