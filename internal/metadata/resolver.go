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
// It returns a domain.TrackMetadata populated with whatever could be discovered
// plus the caller-supplied values. Fields that cannot be resolved are left empty.
//
// Enrichment logic:
//   - If album is non-empty, queries each configured provider for cover art.
//   - If album is empty, skips cover lookup (album name is required for search).
//   - All provider errors are logged at warn level and do not cause EnrichMetadata
//     to return an error — best-effort enrichment.
func (r *MetadataResolver) EnrichMetadata(ctx context.Context, artist, title, album string, year int) (*domain.TrackMetadata, error) {
	result := &domain.TrackMetadata{
		Artist: artist,
		Title:  title,
		Album:  album,
		Year:   year,
	}

	// Cover lookup requires at least artist + album.
	if album == "" || artist == "" {
		return result, nil
	}

	// Try each configured provider until one returns a cover URL.
	providers := r.registry.Configured()
	for _, p := range providers {
		cover, err := p.SearchCover(ctx, artist, album)
		if err != nil {
			r.log.Warn("metadata resolver: cover search failed, trying next provider",
				"provider", p.Name(),
				"artist", artist,
				"album", album,
				"error", err,
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
