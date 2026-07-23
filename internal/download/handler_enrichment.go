package download

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/tagging"
)

// enrichmentStore is the subset of library.Store needed by MetadataEnrichmentHandler.
type enrichmentStore interface {
	GetTrack(ctx context.Context, id int64) (*domain.Track, error)
	GetArtist(ctx context.Context, id int64) (*domain.Artist, error)
	GetAlbum(ctx context.Context, id int64) (*domain.Album, error)
	GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error)
	UpsertTrack(ctx context.Context, track *domain.Track) (int64, error)
	UpsertAlbum(ctx context.Context, album *domain.Album) (int64, error)
}

// MetadataEnrichmentHandler runs registered metadata providers against
// newly imported tracks to enrich them with ISRC, genres, cover art,
// release dates, and external IDs.
type MetadataEnrichmentHandler struct {
	log        *slog.Logger
	registry   *metadata.Registry
	libStore   enrichmentStore
	httpClient *http.Client
	tagger     *tagging.Tagger
}

// NewMetadataEnrichmentHandler creates a handler that queries all configured
// metadata providers and applies their results to the library.
// libStore can be any implementation satisfying the enrichmentStore interface
// (e.g., library.Store from internal/library).
func NewMetadataEnrichmentHandler(registry *metadata.Registry, libStore enrichmentStore, logger *slog.Logger) *MetadataEnrichmentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MetadataEnrichmentHandler{
		log:        logger,
		registry:   registry,
		libStore:   libStore,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		tagger:     tagging.New(logger),
	}
}

// Handle enriches a library track with metadata from configured providers.
// Requires the track to already be in the library (LibraryTrackID > 0).
// Pre-library tracks (LibraryTrackID == 0) are skipped — metadata is now
// resolved at queue time by the webhook/indexer pipeline.
// Failures are non-fatal — the import continues with whatever metadata
// was successfully enriched.
func (h *MetadataEnrichmentHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.LibraryTrackID == 0 || record.FilePath == "" {
		return nil
	}

	// Post-library mode: enrich existing library records.

	track, err := h.libStore.GetTrack(ctx, record.LibraryTrackID)
	if err != nil || track == nil {
		h.log.Warn("track not found, skipping", "track_id", record.LibraryTrackID, "component", "enrichment")
		return nil
	}

	artist, err := h.libStore.GetArtist(ctx, track.ArtistID)
	if err != nil || artist == nil {
		h.log.Warn("artist not found, skipping", "artist_id", track.ArtistID, "track_id", record.LibraryTrackID, "component", "enrichment")
		return nil
	}

	album, err := h.libStore.GetAlbum(ctx, track.AlbumID)
	if err != nil || album == nil {
		h.log.Warn("album not found, skipping", "album_id", track.AlbumID, "track_id", record.LibraryTrackID, "component", "enrichment")
		return nil
	}

	providers := h.registry.Configured()
	if len(providers) == 0 {
		return nil
	}

	trackModified := false
	albumModified := false

	for _, p := range providers {
		// ── Cover art (artist+album search) ────────────────────
		if cover, err := p.SearchCover(ctx, artist.Name, album.Title); err == nil && cover != nil {
			if downloaded := h.downloadCoverIfMissing(ctx, album, cover); downloaded {
				albumModified = true
				album.ThumbURL = "cover.jpg"
			}
		}

		// ── Cover art (MBID-based, e.g. Cover Art Archive) ────
		if caa, ok := p.(metadata.CoverArtArchiveProvider); ok {
			mbid := track.ExternalIDs["musicbrainz_release"]
			if mbid == "" {
				mbid = album.ExternalIDs["musicbrainz_release"]
			}
			if mbid != "" {
				if cover, err := caa.SearchCoverByMBID(ctx, mbid); err == nil && cover != nil {
					if downloaded := h.downloadCoverIfMissing(ctx, album, cover); downloaded {
						albumModified = true
						album.ThumbURL = "cover.jpg"
					}
				}
			}
		}

		// ── Track enrichment ──────────────────────────────────
		meta, err := p.EnrichTrack(ctx, track)
		if err != nil {
			h.log.Warn("enrich track error", "provider", p.Name(), "error", err, "component", "enrichment")
			continue
		}
		if meta == nil {
			continue
		}

		if meta.ISRC != "" && track.ISRC == "" {
			track.ISRC = meta.ISRC
			trackModified = true
		}
		if len(meta.Genres) > 0 && len(album.Genres) == 0 {
			album.Genres = meta.Genres
			albumModified = true
		}
		if meta.ReleaseDate != "" && album.ReleaseDate == "" {
			album.ReleaseDate = meta.ReleaseDate
			albumModified = true
		}
		if meta.Label != "" {
			if album.ExternalIDs == nil {
				album.ExternalIDs = make(map[string]string)
			}
			if _, exists := album.ExternalIDs["label"]; !exists {
				album.ExternalIDs["label"] = meta.Label
				albumModified = true
			}
		}

		// Merge external IDs (MusicBrainz MBIDs, etc.).
		if len(meta.ExternalIDs) > 0 {
			if track.ExternalIDs == nil {
				track.ExternalIDs = make(map[string]string)
			}
			for k, v := range meta.ExternalIDs {
				if _, exists := track.ExternalIDs[k]; !exists {
					track.ExternalIDs[k] = v
					trackModified = true
				}
			}
		}
	}

	// Persist enriched data.
	if trackModified {
		if _, err := h.libStore.UpsertTrack(ctx, track); err != nil {
			h.log.Error("upsert track failed", "track_id", track.ID, "error", err, "component", "enrichment")
		}
	}
	if albumModified {
		if _, err := h.libStore.UpsertAlbum(ctx, album); err != nil {
			h.log.Error("upsert album failed", "album_id", album.ID, "error", err, "component", "enrichment")
		}
	}

	// Re-write tags if track or album metadata changed.
	if trackModified || albumModified {
		coverPath := filepath.Join(filepath.Dir(track.FilePath), "cover.jpg")
		if _, err := os.Stat(coverPath); err != nil {
			coverPath = "" // no cover to embed
		}
		if err := h.tagger.WriteTags(track.FilePath, artist.Name, album.Title, track.Title, coverPath); err != nil {
			h.log.Warn("re-tag failed", "file", track.FilePath, "error", err, "component", "enrichment")
		}
	}

	return nil
}

// downloadCoverIfMissing downloads a cover image from result.ThumbURL
// (or ImageURL as fallback) to cover.jpg in the album directory.
// Returns true if a new cover was downloaded.
func (h *MetadataEnrichmentHandler) downloadCoverIfMissing(ctx context.Context, album *domain.Album, cover *metadata.CoverResult) bool {
	if cover == nil {
		return false
	}

	// Determine album directory from existing tracks.
	tracks, err := h.libStore.GetTracksByAlbum(ctx, album.ID)
	if err != nil || len(tracks) == 0 {
		return false
	}

	albumDir := filepath.Dir(tracks[0].FilePath)
	coverPath := filepath.Join(albumDir, "cover.jpg")

	// Don't overwrite existing covers.
	if _, err := os.Stat(coverPath); err == nil {
		return false
	}

	url := cover.ThumbURL
	if url == "" {
		url = cover.ImageURL
	}
	if url == "" {
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.log.Error("create cover request failed", "url", url, "error", err, "component", "enrichment")
		return false
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		h.log.Warn("fetch cover failed", "url", url, "error", err, "component", "enrichment")
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	f, err := os.Create(coverPath)
	if err != nil {
		h.log.Warn("create cover file failed", "error", err, "component", "enrichment")
		return false
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		h.log.Warn("write cover failed", "error", err, "component", "enrichment")
		os.Remove(coverPath) // clean up partial file
		return false
	}

	return true
}

// Compile-time interface check.
var _ ImportHandler = (*MetadataEnrichmentHandler)(nil)
