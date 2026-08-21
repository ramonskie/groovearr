package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/tagging"
)

// enrichmentStore is the subset of library.Store needed by MetadataEnrichmentHandler.
type enrichmentStore interface {
	GetTrack(ctx context.Context, id int64) (*domain.Track, error)
	GetArtist(ctx context.Context, id int64) (*domain.Artist, error)
	SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error
	GetAlbum(ctx context.Context, id int64) (*domain.Album, error)
	GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error)
	UpsertTrack(ctx context.Context, track *domain.Track) (int64, error)
	UpsertAlbum(ctx context.Context, album *domain.Album) (int64, error)
}

// MetadataEnrichmentHandler runs registered metadata providers against
// newly imported tracks to enrich them with ISRC, genres, cover art,
// release dates, and external IDs.
type MetadataEnrichmentHandler struct {
	log           *slog.Logger
	registry      *metadata.Registry
	discoveryReg  *discovery.Registry
	providerOrder *metadata.ProviderOrder // shared live source for provider priority
	libStore      enrichmentStore
	httpClient    *http.Client
	tagger        *tagging.Tagger

	imageMu        sync.Mutex
	imageAttempted map[int64]time.Time // artistID → last image attempt, TTL-gated
}

// artistImageRetryTTL bounds how often a given artist's image is searched on
// the discovery providers (Deezer, Spotify, etc.) during routine imports, so
// repeated imports of the same artist don't hammer the provider APIs. Explicit
// bulk jobs reset the window via ResetBulk and therefore re-attempt each
// artist every run.
const artistImageRetryTTL = 24 * time.Hour

// trackProviderTimeout bounds the per-track provider pass in a bulk
// enrichment run. A track that no provider can fill (obscure release) would
// otherwise walk every configured provider — including rate-limited MusicBrainz
// and Discogs — for minutes. The deadline caps that while persistence below
// still runs on the caller's context, so whatever was resolved before the
// deadline is saved. Downloads (bulk=false) keep the unbounded loop. Exposed
// as a var so tests can shrink it.
var trackProviderTimeout = 2 * time.Minute

// NewMetadataEnrichmentHandler creates a handler that queries all configured
// metadata providers and applies their results to the library.
// discoveryReg provides discovery providers for artist image enrichment
// (Deezer, Spotify, etc.). libStore can be any implementation satisfying
// the enrichmentStore interface (e.g., library.Store from internal/library).
func NewMetadataEnrichmentHandler(registry *metadata.Registry, discoveryReg *discovery.Registry, libStore enrichmentStore, logger *slog.Logger) *MetadataEnrichmentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MetadataEnrichmentHandler{
		log:            logger,
		registry:       registry,
		discoveryReg:   discoveryReg,
		libStore:       libStore,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		tagger:         tagging.New(logger),
		imageAttempted: make(map[int64]time.Time),
	}
}

// SetProviderOrder configures the shared live source for the metadata provider
// priority order. Read on each query so runtime config changes apply without
// a restart. Pass nil to fall back to registration order.
func (h *MetadataEnrichmentHandler) SetProviderOrder(order *metadata.ProviderOrder) {
	h.providerOrder = order
}

// EnrichLibraryTrack runs the full metadata enrichment for an existing library
// track, without a download record. Used by the background enrichment job to
// enrich a scanned library. Missing metadata fields are filled in; existing
// values are preserved.
func (h *MetadataEnrichmentHandler) EnrichLibraryTrack(ctx context.Context, trackID int64) error {
	return h.enrichTrack(ctx, &Record{LibraryTrackID: trackID}, true)
}

// ResetBulk clears the artist-image retry dedup state. Call once at the
// start of each bulk enrichment job: a bulk run is an explicit full-library
// refresh, so every artist is retried rather than waiting out the TTL.
func (h *MetadataEnrichmentHandler) ResetBulk() {
	h.imageMu.Lock()
	defer h.imageMu.Unlock()
	h.imageAttempted = make(map[int64]time.Time)
}

// imageAttemptedRecently reports whether the artist's image was searched
// within the retry window (read-only).
func (h *MetadataEnrichmentHandler) imageAttemptedRecently(artistID int64, now time.Time) bool {
	h.imageMu.Lock()
	defer h.imageMu.Unlock()
	last, ok := h.imageAttempted[artistID]
	return ok && now.Sub(last) < artistImageRetryTTL
}

// markImageAttemptDue atomically checks the retry window and, when the
// artist's image may be attempted now, records the attempt and returns true.
func (h *MetadataEnrichmentHandler) markImageAttemptDue(artistID int64, now time.Time) bool {
	h.imageMu.Lock()
	defer h.imageMu.Unlock()
	if last, ok := h.imageAttempted[artistID]; ok && now.Sub(last) < artistImageRetryTTL {
		return false
	}
	h.imageAttempted[artistID] = now
	return true
}

// orderedProviders returns configured providers sorted by providerOrder.
func (h *MetadataEnrichmentHandler) orderedProviders() []metadata.Provider {
	providers := h.registry.Available()
	order := h.providerOrder.Current()
	if len(order) == 0 {
		return providers
	}
	byName := make(map[string]metadata.Provider, len(providers))
	for _, p := range providers {
		byName[p.Name()] = p
	}
	var ordered []metadata.Provider
	seen := make(map[string]bool)
	for _, name := range order {
		if p, ok := byName[name]; ok && !seen[name] {
			ordered = append(ordered, p)
			seen[name] = true
		}
	}
	for _, p := range providers {
		if !seen[p.Name()] {
			ordered = append(ordered, p)
		}
	}
	return ordered
}

// Handle enriches a library track with metadata from configured providers.
// Requires the track to already be in the library (LibraryTrackID > 0).
// Pre-library tracks (LibraryTrackID == 0) are skipped — metadata is now
// resolved at queue time by the webhook/indexer pipeline.
// Failures are non-fatal — the import continues with whatever metadata
// was successfully enriched.
func (h *MetadataEnrichmentHandler) Handle(ctx context.Context, record *Record) error {
	// Enrichment failures are non-fatal for downloads — the record must never
	// be marked failed because a provider lookup failed. enrichTrack already
	// logs the failure; the returned error is only surfaced to the bulk job.
	_ = h.enrichTrack(ctx, record, false)
	return nil
}

// enrichTrack is the shared implementation of Handle. When bulk is true (the
// bulk library job), already-enriched tracks are skipped entirely and the
// artist-image refresh is attempted at most once per artist per run (tracked
// in bulkArtists) — so a failed image fetch for one artist never causes every
// subsequent track of that artist to re-hit the metadata providers.
func (h *MetadataEnrichmentHandler) enrichTrack(ctx context.Context, record *Record, bulk bool) error {
	if record.LibraryTrackID == 0 {
		return nil
	}

	// Post-library mode: enrich existing library records.

	track, err := h.libStore.GetTrack(ctx, record.LibraryTrackID)
	if err != nil {
		h.log.Error("track lookup failed", "track_id", record.LibraryTrackID, "error", err, "component", "enrichment")
		return fmt.Errorf("track lookup: %w", err)
	}
	if track == nil {
		h.log.Warn("track not found, skipping", "track_id", record.LibraryTrackID, "component", "enrichment")
		return fmt.Errorf("track %d not found", record.LibraryTrackID)
	}
	if record.FilePath == "" {
		record.FilePath = track.FilePath
	}
	if record.FilePath == "" {
		return fmt.Errorf("track %d has no file path", record.LibraryTrackID)
	}

	artist, err := h.libStore.GetArtist(ctx, track.ArtistID)
	if err != nil {
		h.log.Error("artist lookup failed", "artist_id", track.ArtistID, "track_id", record.LibraryTrackID, "error", err, "component", "enrichment")
		return fmt.Errorf("artist lookup: %w", err)
	}
	if artist == nil {
		h.log.Warn("artist not found, skipping", "artist_id", track.ArtistID, "track_id", record.LibraryTrackID, "component", "enrichment")
		return fmt.Errorf("artist %d not found", track.ArtistID)
	}

	album, err := h.libStore.GetAlbum(ctx, track.AlbumID)
	if err != nil {
		h.log.Error("album lookup failed", "album_id", track.AlbumID, "track_id", record.LibraryTrackID, "error", err, "component", "enrichment")
		return fmt.Errorf("album lookup: %w", err)
	}
	if album == nil {
		h.log.Warn("album not found, skipping", "album_id", track.AlbumID, "track_id", record.LibraryTrackID, "component", "enrichment")
		return fmt.Errorf("album %d not found", track.AlbumID)
	}

	// Bulk mode: skip tracks whose metadata and cover are already complete —
	// re-running the providers for them is pure waste. The artist image is
	// handled separately (below): attempted at most once per artist per retry
	// window, so a failed fetch doesn't re-trigger per-track metadata enrichment.
	skipArtistImage := false
	if bulk {
		hasCover := library.HasCoverFile(library.AlbumDirFromTrack(track.FilePath))
		metadataComplete := track.ISRC != "" && len(track.ExternalIDs) > 0 &&
			len(album.Genres) > 0 && album.ReleaseDate != "" && hasCover

		// Whether this artist's image was already attempted within the retry
		// window; the shared gate below records the attempt.
		skipArtistImage = h.imageAttemptedRecently(track.ArtistID, time.Now())

		if metadataComplete {
			// Metadata and cover present — only a missing artist portrait may
			// remain, fetched at most once per artist per retry window.
			if artist.ThumbURL == "" && !skipArtistImage && h.discoveryReg != nil &&
				h.markImageAttemptDue(track.ArtistID, time.Now()) {
				h.enrichArtistImage(ctx, artist, track)
			}
			return nil
		}
	}

	providers := h.orderedProviders()
	if len(providers) == 0 {
		return nil
	}

	// Bulk enrichment: bound the provider pass per track so a slow or
	// un-fillable track can't pin a worker slot for minutes. Persistence below
	// still runs on the caller's context so partial results are saved even when
	// the deadline fires.
	provCtx := ctx
	var provCancel context.CancelFunc
	if bulk {
		provCtx, provCancel = context.WithTimeout(ctx, trackProviderTimeout)
		defer provCancel()
	}

	trackModified := false
	albumModified := false

	// Sync album MBID from the download record (resolved by AlbumImportHandler).
	if record.AlbumMBID != "" && album.ExternalIDs["musicbrainz_release"] == "" {
		if album.ExternalIDs == nil {
			album.ExternalIDs = make(map[string]string)
		}
		album.ExternalIDs["musicbrainz_release"] = record.AlbumMBID
		albumModified = true
	}

	for _, p := range providers {
		// Stop iterating once the per-track deadline (or a job cancellation)
		// fires — calling further providers on an expired context only yields
		// stale warn logs per provider.
		if err := provCtx.Err(); err != nil {
			break
		}
		if tMod, aMod := h.enrichFromProvider(provCtx, p, artist, album, track, record); tMod || aMod {
			if tMod {
				trackModified = true
			}
			if aMod {
				albumModified = true
			}
		}

		// Bulk job only: stop once the track and album are fully populated.
		// With the metadata provider order configured (e.g. Spotify/Tidal on
		// top), the first provider fills everything and the lower-priority
		// providers are never called — this is what makes whole-library
		// enrichment tractable. The per-download path intentionally runs every
		// provider so fields like MusicBrainz MBIDs and label aren't lost.
		if bulk && track.ISRC != "" && len(track.ExternalIDs) > 0 &&
			len(album.Genres) > 0 && album.ReleaseDate != "" &&
			library.HasCoverFile(library.AlbumDirFromTrack(track.FilePath)) {
			h.log.Debug("bulk enrichment complete", "provider", p.Name(), "track_id", track.ID, "album_id", album.ID, "component", "enrichment")
			break
		}
	}

	// Decide the timeout outcome right after the provider pass: the per-track
	// deadline is about bounding provider work, not the fast persistence/tag
	// tail below. A track whose providers finished just under the deadline must
	// not be mislabeled as a timeout because the DB write or tag re-write
	// crossed it.
	timedOut := false
	if bulk {
		if err := provCtx.Err(); errors.Is(err, context.DeadlineExceeded) {
			timedOut = true
		}
	}

	// Enrich artist image from discovery providers (Deezer, Spotify, etc.).
	// Attempted at most once per artist per retry window so repeated imports
	// of the same artist don't hammer the provider APIs; existing images are
	// still refreshed when the window elapses. Skipped when the provider pass
	// already timed out so a slow image fetch can't extend a dead track. The
	// image shares the per-track deadline: a fetch cut by it is dropped for
	// this run (no partial file is left behind) and retried on the next bulk
	// run via ResetBulk. It does not turn a track whose provider pass
	// completed into a timeout — the timeout outcome reflects the provider
	// pass only.
	if !timedOut && h.discoveryReg != nil && !skipArtistImage && h.markImageAttemptDue(track.ArtistID, time.Now()) {
		h.enrichArtistImage(provCtx, artist, track)
	}

	// ── Sync thumb_url with on-disk cover (run once after all providers) ─
	// The CoverArtHandler (step 3) may have already downloaded cover art,
	// but album didn't exist in the library yet at that point. Ensure
	// thumb_url is set if any cover image exists on disk.
	if album.ThumbURL == "" {
		if tracks, err := h.libStore.GetTracksByAlbum(ctx, album.ID); err == nil && len(tracks) > 0 {
			if library.HasCoverFile(library.AlbumDirFromTrack(tracks[0].FilePath)) {
				album.ThumbURL = "cover.jpg"
				albumModified = true
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
		coverPath := library.CoverFilePath(library.AlbumDirFromTrack(track.FilePath))
		if err := h.tagger.WriteTags(track.FilePath, artist.Name, album.Title, track.Title, coverPath); err != nil {
			h.log.Warn("re-tag failed", "file", track.FilePath, "error", err, "component", "enrichment")
		}
	}

	// Report the captured timeout outcome (decided right after the provider
	// pass, above). Partial results were persisted, so nothing is lost on
	// timeout. A job-wide cancellation is not a timeout — propagate it so the
	// runner records the in-flight track as cancelled rather than completed.
	if timedOut {
		h.log.Warn("enrich track timed out", "track_id", track.ID, "album_id", album.ID, "error", provCtx.Err(), "component", "enrichment")
		return fmt.Errorf("track enrichment timed out: %w", provCtx.Err())
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	return nil
}

// downloadCoverIfMissing downloads album cover art from cover.ImageURL
// (or ThumbURL as fallback) to cover.jpg in the album directory.
func (h *MetadataEnrichmentHandler) downloadCoverIfMissing(ctx context.Context, album *domain.Album, cover *metadata.CoverResult) {
	if cover == nil {
		return
	}

	// Determine album directory from existing tracks.
	tracks, err := h.libStore.GetTracksByAlbum(ctx, album.ID)
	if err != nil || len(tracks) == 0 {
		return
	}

	albumDir := library.AlbumDirFromTrack(tracks[0].FilePath)

	// Don't overwrite existing covers (any format).
	if library.HasCoverFile(albumDir) {
		return
	}

	coverPath := filepath.Join(albumDir, "cover.jpg")

	url := cover.ImageURL
	if url == "" {
		url = cover.ThumbURL
	}
	if url == "" {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.log.Error("create cover request failed", "url", url, "error", err, "component", "enrichment")
		return
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		h.log.Warn("fetch cover failed", "url", url, "error", err, "component", "enrichment")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	f, err := os.Create(coverPath)
	if err != nil {
		h.log.Warn("create cover file failed", "error", err, "component", "enrichment")
		return
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		h.log.Warn("write cover failed", "error", err, "component", "enrichment")
		os.Remove(coverPath) // clean up partial file
		return
	}

	return
}

// downloadArtistImage downloads an artist image from result.ImageURL
// (or ThumbURL as fallback) to artist.jpg in the artist directory.
// Always refreshes so re-imports correct stale images. Caching headers
// (must-revalidate + ETag) ensure browsers pick up the new image.
func (h *MetadataEnrichmentHandler) downloadArtistImage(ctx context.Context, artist *domain.Artist, track *domain.Track, result *metadata.ArtistImageResult) {
	if result == nil {
		return
	}

	// Determine artist directory (parent of album directory, disc-aware).
	artistDir := library.ArtistDirFromTrack(track.FilePath)
	artistPath := filepath.Join(artistDir, "artist.jpg")

	url := result.ImageURL
	if url == "" {
		url = result.ThumbURL
	}
	if url == "" {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.log.Error("create artist image request failed", "url", url, "error", err, "component", "enrichment")
		return
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		h.log.Warn("fetch artist image failed", "artist", artist.Name, "url", url, "error", err, "component", "enrichment")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	f, err := os.Create(artistPath)
	if err != nil {
		h.log.Warn("create artist image file failed", "path", artistPath, "error", err, "component", "enrichment")
		return
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		h.log.Warn("write artist image failed", "path", artistPath, "error", err, "component", "enrichment")
		os.Remove(artistPath)
		return
	}

	// Persist the thumb URL so downstream consumers know the image exists.
	if err := h.libStore.SetArtistThumbURL(ctx, artist.ID, "artist.jpg"); err != nil {
		h.log.Warn("set artist thumb url failed", "artist_id", artist.ID, "error", err, "component", "enrichment")
		os.Remove(artistPath)
		return
	}
	artist.ThumbURL = "artist.jpg"
}

// enrichArtistImage tries to find and download an artist image from discovery
// providers (Deezer, Spotify, etc.). Runs after metadata enrichment so wrong or
// stale images get corrected on re-import. Callers gate this via the TTL-based
// markImageAttemptDue, so it runs at most once per artist per retry window.
func (h *MetadataEnrichmentHandler) enrichArtistImage(ctx context.Context, artist *domain.Artist, track *domain.Track) {
	providers := h.discoveryReg.Any()
	if len(providers) == 0 {
		return
	}

	namesToTry := []string{artist.Name}
	if primary := primaryArtist(artist.Name); primary != "" && primary != artist.Name {
		namesToTry = append(namesToTry, primary)
	}

	for _, tryName := range namesToTry {
		for _, p := range providers {
			results, err := p.SearchArtists(ctx, tryName, 1)
			if err != nil {
				h.log.Debug("discovery artist search failed",
					"provider", p.Name(), "artist", tryName, "error", err, "component", "enrichment")
				continue
			}
			if len(results) == 0 || results[0].ImageURL == "" {
				continue
			}
			// Filter out known placeholder images (e.g., Deezer's MD5-of-empty-string).
			if isPlaceholderImage(results[0].ImageURL) {
				continue
			}
			if !artistNameMatches(results[0].Name, tryName) {
				continue
			}

			imgResult := &metadata.ArtistImageResult{
				ImageURL: results[0].ImageURL,
				Source:   p.Name(),
			}
			h.downloadArtistImage(ctx, artist, track, imgResult)
			if artist.ThumbURL != "" {
				return // found and persisted
			}
		}
	}
}

// Compile-time interface check.
var _ ImportHandler = (*MetadataEnrichmentHandler)(nil)

// enrichFromProvider runs album title resolution, cover art download, and track
// metadata enrichment for a single provider. Returns whether track or album was
// modified so the caller can persist changes.
func (h *MetadataEnrichmentHandler) enrichFromProvider(
	ctx context.Context,
	p metadata.Provider,
	artist *domain.Artist,
	album *domain.Album,
	track *domain.Track,
	record *Record,
) (trackModified, albumModified bool) {
	// Album title resolution (when missing).
	// Try full artist first, then primary artist fallback for comma-separated
	// Spotify co-artist strings (e.g., "The Moon, DJ Ghost").
	if album.Title == "" {
		if found := p.SearchAlbum(ctx, artist.Name, track.Title); found != "" {
			album.Title = found
			albumModified = true
		} else if primary := primaryArtist(artist.Name); primary != artist.Name {
			if found := p.SearchAlbum(ctx, primary, track.Title); found != "" {
				album.Title = found
				albumModified = true
			}
		}
	}

	// Cover art (artist+album search) — skipped when a cover already exists on
	// disk (e.g. extracted by the scanner), so no network call is wasted.
	if album.Title == "" {
		return // can't search for cover without an album name
	}
	if !library.HasCoverFile(library.AlbumDirFromTrack(track.FilePath)) {
		if cover, err := p.SearchCover(ctx, artist.Name, album.Title); err == nil && cover != nil {
			h.downloadCoverIfMissing(ctx, album, cover)
		} else if primary := primaryArtist(artist.Name); primary != artist.Name {
			if cover2, err2 := p.SearchCover(ctx, primary, album.Title); err2 == nil && cover2 != nil {
				h.downloadCoverIfMissing(ctx, album, cover2)
			}
		}

		// Cover art (MBID-based, e.g. Cover Art Archive).
		if caa, ok := p.(metadata.CoverArtArchiveProvider); ok {
			mbid := track.ExternalIDs["musicbrainz_release"]
			if mbid == "" {
				mbid = album.ExternalIDs["musicbrainz_release"]
			}
			if mbid == "" {
				mbid = record.AlbumMBID
			}
			if mbid != "" {
				if cover, err := caa.SearchCoverByMBID(ctx, mbid); err == nil && cover != nil {
					h.downloadCoverIfMissing(ctx, album, cover)
				}
			}
		}
	}

	// Track enrichment.
	meta, err := p.EnrichTrack(ctx, track)
	if err != nil {
		h.log.Warn("enrich track error", "provider", p.Name(), "error", err, "component", "enrichment")
		return
	}
	if meta == nil {
		return
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

	return
}

// primaryArtist returns the primary artist name by stripping featured/collaboration
// suffixes. Handles non-breaking spaces (\u00a0) commonly found in audio file metadata
// and multiple separator patterns: ", ", " & ", " feat. ", " vs. ", " x ".
// Returns the original name if no separator is found.
func primaryArtist(artist string) string {
	// Normalize non-breaking spaces.
	name := strings.ReplaceAll(artist, "\u00a0", " ")
	for _, sep := range []string{", ", " & ", " feat. ", " vs. ", " x "} {
		if idx := strings.Index(name, sep); idx > 0 {
			return strings.TrimSpace(name[:idx])
		}
	}
	return artist
}

// isPlaceholderImage returns true for known empty/default placeholder URLs
// from providers like Deezer (MD5 of empty string).
func isPlaceholderImage(url string) bool {
	return strings.Contains(url, "/artist/d41d8cd98f00b204e9800998ecf8427e/")
}

// artistNameMatches checks whether a search result name matches the query name.
// Uses word-level matching to avoid false positives from substring matches
// (e.g., "The Moon" should not match "The Moonlight").
func artistNameMatches(resultName, queryName string) bool {
	if strings.EqualFold(resultName, queryName) {
		return true
	}
	queryLower := strings.ToLower(queryName)
	resultLower := strings.ToLower(resultName)
	// Check if all query words appear as complete words in the result name.
	for _, qw := range strings.Fields(queryLower) {
		found := false
		for _, rw := range strings.Fields(resultLower) {
			if rw == qw {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
