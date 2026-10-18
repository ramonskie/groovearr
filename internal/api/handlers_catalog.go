package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/identity"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/refresh"
)

// unmatchedReviewer is the admin review surface over the identity resolver.
// The api package depends on behavior, not the concrete resolver (AGENTS §3).
type unmatchedReviewer interface {
	ReviewAlbum(ctx context.Context, albumID int64) (identity.AlbumReview, error)
	ApplyTrackMatch(ctx context.Context, albumID, localTrackID int64, provider, providerTrackID string) (bool, error)
	ApplyReleaseMatch(ctx context.Context, albumID int64, provider, providerAlbumID string) (int, bool, error)
}

// errUnmatchedReviewUnavailable is the defensive 503 when app.go did not wire
// the reviewer (tests / a Server built without one).
var (
	errUnmatchedReviewUnavailable = errors.New("unmatched review not available")
	errProviderRateLimited        = errors.New("provider rate limited")
)

// catalogReviewTimeout bounds the provider-backed review read. The server's
// global write timeout is 30s; leaving headroom keeps the response intact.
const catalogReviewTimeout = 25 * time.Second

// catalogMatchTimeout bounds one manual apply (a provider lookup or release
// fetch plus the catalog materialization).
const catalogMatchTimeout = 25 * time.Second

// refreshStatusReader is the narrow slice of the refresh engine the status
// handler reads. The engine is a service-owned loop (AGENTS §2), not a job, so a
// read-only snapshot is its status surface; the parked-row listing reads the
// catalog store directly. Wired via Server.SetRefreshStatus. The catalog side
// depends on catalog.StatsStore (the Server field holds it), so only the engine
// needs a port here.
type refreshStatusReader interface {
	Status() refresh.Status
}

// errCatalogStatsUnavailable is returned (503) when no catalog store is wired.
// app.go always wires one; this is the defensive guard for a Server built
// without it (e.g. in tests), so the endpoint degrades cleanly.
var errCatalogStatsUnavailable = errors.New("catalog stats not available")

// catalogStatusReadTimeout bounds the read-only store reads behind the status
// endpoint (AGENTS §7 — every I/O operation carries a per-operation timeout).
const catalogStatusReadTimeout = 5 * time.Second

// catalogUnmatchedLimit caps one unmatched-listing response. The parked set is
// bounded by library size; the cap only keeps a pathological library from
// returning an unbounded body.
const catalogUnmatchedLimit = 1000

// ─── Catalog status (read-only) ──────────────────────────────────────

// catalogStatusResponse is the read-only status payload for the settings card.
type catalogStatusResponse struct {
	Enabled bool                `json:"enabled"`
	Queue   catalogQueueStatus  `json:"queue"`
	Albums  map[string]int      `json:"albums"`
	Artists catalogArtistStatus `json:"artists"`
	Engine  catalogEngineStatus `json:"engine"`
}

type catalogQueueStatus struct {
	Queued    int `json:"queued"`
	DueNow    int `json:"due_now"`
	Scheduled int `json:"scheduled"`
}

type catalogArtistStatus struct {
	Materialized int `json:"materialized"`
}

type catalogEngineStatus struct {
	Running         bool       `json:"running"`
	IntervalSeconds int        `json:"interval_seconds"`
	BatchSize       int        `json:"batch_size"`
	LastDrainAt     *time.Time `json:"last_drain_at"`
	LastDrainCount  int        `json:"last_drain_count"`
}

// handleCatalogStatus reports the materialized metadata catalog and refresh
// engine state for the Settings → Jobs card. It is thin by contract (AGENTS §4):
// a few read-only store reads plus one engine snapshot -> writeJSON. The engine
// runs its own loop, so nothing is started here and no goroutine is spawned.
func (s *Server) handleCatalogStatus(w http.ResponseWriter, r *http.Request) {
	if s.catalogStats == nil {
		writeError(w, http.StatusServiceUnavailable, errCatalogStatsUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), catalogStatusReadTimeout)
	defer cancel()

	st, err := s.catalogStats.QueueStats(ctx, time.Now().UTC())
	if err != nil {
		s.catalogStatusError(w, err)
		return
	}
	counts, err := s.catalogStats.AlbumStatusCounts(ctx)
	if err != nil {
		s.catalogStatusError(w, err)
		return
	}
	artists, err := s.catalogStats.CountArtistMetadata(ctx)
	if err != nil {
		s.catalogStatusError(w, err)
		return
	}

	resp := catalogStatusResponse{
		Enabled: s.isCatalogEnabled(),
		Queue: catalogQueueStatus{
			Queued:    st.Queued,
			DueNow:    st.DueNow,
			Scheduled: st.Scheduled,
		},
		Albums:  albumStatusJSON(counts),
		Artists: catalogArtistStatus{Materialized: artists},
	}
	if s.refreshStatus != nil {
		es := s.refreshStatus.Status()
		resp.Engine = catalogEngineStatus{
			Running:         es.Running,
			IntervalSeconds: es.IntervalSeconds,
			BatchSize:       es.BatchSize,
			LastDrainAt:     es.LastDrainAt,
			LastDrainCount:  es.LastDrainCount,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// catalogUnmatchedAlbum is one parked album in the unmatched listing.
type catalogUnmatchedAlbum struct {
	AlbumID   int64                 `json:"album_id"`
	ArtistID  int64                 `json:"artist_id"`
	Artist    string                `json:"artist"`
	Title     string                `json:"title"`
	Status    catalog.RefreshStatus `json:"status"`
	Error     string                `json:"error,omitempty"`
	UpdatedAt time.Time             `json:"updated_at"`
}

// handleCatalogUnmatched lists the albums parked with refresh_status=unmatched.
// It is the read-only diagnostic behind the status card's unmatched count: the
// count alone cannot tell an operator which albums are parked. Thin by contract
// (AGENTS §4): timeout -> store read -> writeJSON, no provider calls.
func (s *Server) handleCatalogUnmatched(w http.ResponseWriter, r *http.Request) {
	if s.catalogStats == nil {
		writeError(w, http.StatusServiceUnavailable, errCatalogStatsUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), catalogStatusReadTimeout)
	defer cancel()

	// Fetch one extra row so a full page can be reported as truncated without a
	// second COUNT query. The response stays a plain array; the header is the
	// signal that the 1000-row cap clipped it.
	rows, err := s.catalogStats.ListUnmatchedAlbums(ctx, catalogUnmatchedLimit+1)
	if err != nil {
		s.catalogUnmatchedError(w, err)
		return
	}
	if len(rows) > catalogUnmatchedLimit {
		rows = rows[:catalogUnmatchedLimit]
		w.Header().Set("X-Result-Truncated", "true")
	}
	resp := make([]catalogUnmatchedAlbum, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, catalogUnmatchedAlbum{
			AlbumID:   row.AlbumID,
			ArtistID:  row.ArtistID,
			Artist:    row.Artist,
			Title:     row.Title,
			Status:    row.Status,
			Error:     row.Error,
			UpdatedAt: row.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCatalogUnmatchedReview returns one parked album's local tracks and, for
// a one-track release, the provider identity candidates. It never applies a
// match; the admin chooses one explicitly via the match endpoint.
func (s *Server) handleCatalogUnmatchedReview(w http.ResponseWriter, r *http.Request) {
	if s.unmatchedReviewer == nil {
		writeError(w, http.StatusServiceUnavailable, errUnmatchedReviewUnavailable)
		return
	}
	albumID, err := strconv.ParseInt(r.PathValue("albumID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("unmatched album not found"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), catalogReviewTimeout)
	defer cancel()
	review, err := s.unmatchedReviewer.ReviewAlbum(ctx, albumID)
	if err != nil {
		s.catalogReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

// catalogUnmatchedMatchRequest is the manual-match payload. A track match sets
// local_track_id + provider_track_id; a release match sets provider_album_id.
type catalogUnmatchedMatchRequest struct {
	LocalTrackID    int64  `json:"local_track_id"`
	Provider        string `json:"provider"`
	ProviderTrackID string `json:"provider_track_id"`
	ProviderAlbumID string `json:"provider_album_id"`
}

// handleCatalogUnmatchedMatch applies one admin-chosen candidate through the
// identity resolver (which calls refresh.MaterializeResolved). The handler is
// thin: parse -> validate shape -> service call -> JSON (AGENTS §4).
func (s *Server) handleCatalogUnmatchedMatch(w http.ResponseWriter, r *http.Request) {
	if s.unmatchedReviewer == nil {
		writeError(w, http.StatusServiceUnavailable, errUnmatchedReviewUnavailable)
		return
	}
	albumID, err := strconv.ParseInt(r.PathValue("albumID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("unmatched album not found"))
		return
	}
	// This is a small, admin-only mutation payload; cap it so a malformed or
	// hostile body cannot allocate unbounded strings.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req catalogUnmatchedMatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid match payload"))
		return
	}
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	}
	providerTrackID := strings.TrimSpace(req.ProviderTrackID)
	providerAlbumID := strings.TrimSpace(req.ProviderAlbumID)
	if (providerTrackID == "") == (providerAlbumID == "") {
		writeError(w, http.StatusBadRequest, errors.New("exactly one of provider_track_id or provider_album_id is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), catalogMatchTimeout)
	defer cancel()

	if providerTrackID != "" {
		if req.LocalTrackID <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("local_track_id is required for a track match"))
			return
		}
		applied, err := s.unmatchedReviewer.ApplyTrackMatch(ctx, albumID, req.LocalTrackID, provider, providerTrackID)
		if err != nil {
			s.catalogMatchError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": "track", "applied": applied})
		return
	}
	tracks, applied, err := s.unmatchedReviewer.ApplyReleaseMatch(ctx, albumID, provider, providerAlbumID)
	if err != nil {
		s.catalogMatchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": "release", "applied": applied, "tracks": tracks})
}

// catalogReviewError maps review failures to stable client-visible statuses.
// Unknown errors are logged and reduced to a generic body, never echoing
// provider/SQL text (AGENTS §10).
func (s *Server) catalogReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		// The client disconnected; there is no response to write.
		return
	case errors.Is(err, identity.ErrAlbumNotUnmatched):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, identity.ErrProviderCoolingDown):
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, identity.ErrProviderCoolingDown)
	case errors.Is(err, metadata.ErrRateLimited):
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, errProviderRateLimited)
	case errors.Is(err, identity.ErrProviderUnavailable):
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, identity.ErrCandidateNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, errors.New("unmatched review timed out"))
	default:
		if s.log != nil {
			s.log.Error("catalog unmatched review failed", "component", "api", "error", err)
		}
		writeError(w, http.StatusInternalServerError, errors.New("unmatched review unavailable"))
	}
}

// catalogMatchError maps manual-apply failures to stable statuses. A resolved
// album is a conflict; a wrong provider/candidate is a client error; an empty
// release or the single/multi guard is a bad request.
func (s *Server) catalogMatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		// The client disconnected; there is no response to write.
		return
	case errors.Is(err, identity.ErrAlbumNotUnmatched):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, identity.ErrProviderCoolingDown):
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, identity.ErrProviderCoolingDown)
	case errors.Is(err, metadata.ErrRateLimited):
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, errProviderRateLimited)
	case errors.Is(err, identity.ErrProviderUnavailable):
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, identity.ErrCandidateNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, identity.ErrReleaseEmpty),
		errors.Is(err, identity.ErrReleaseTooSmall),
		errors.Is(err, identity.ErrReleaseMatchRequired),
		errors.Is(err, identity.ErrTrackMatchRequired):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, errors.New("unmatched match timed out"))
	default:
		if s.log != nil {
			s.log.Error("catalog unmatched match failed", "component", "api", "error", err)
		}
		writeError(w, http.StatusInternalServerError, errors.New("unmatched match unavailable"))
	}
}

// catalogStatusError logs the wrapped store error server-side and returns a
// generic 500, so internal SQL/driver text is not echoed to the client.
func (s *Server) catalogStatusError(w http.ResponseWriter, err error) {
	if s.log != nil {
		s.log.Error("catalog status read failed", "component", "api", "error", err)
	}
	writeError(w, http.StatusInternalServerError, errors.New("catalog status unavailable"))
}

// catalogUnmatchedError mirrors catalogStatusError for the unmatched listing.
func (s *Server) catalogUnmatchedError(w http.ResponseWriter, err error) {
	if s.log != nil {
		s.log.Error("catalog unmatched read failed", "component", "api", "error", err)
	}
	writeError(w, http.StatusInternalServerError, errors.New("catalog unmatched unavailable"))
}

// albumStatusJSON renders the per-status album counts with every known status
// present (zero-filled) so the card renders a stable set of rows instead of
// shifting as statuses appear and disappear.
func albumStatusJSON(counts map[catalog.RefreshStatus]int) map[string]int {
	return map[string]int{
		string(catalog.StatusPending):   counts[catalog.StatusPending],
		string(catalog.StatusFresh):     counts[catalog.StatusFresh],
		string(catalog.StatusStale):     counts[catalog.StatusStale],
		string(catalog.StatusUnmatched): counts[catalog.StatusUnmatched],
		string(catalog.StatusFailed):    counts[catalog.StatusFailed],
	}
}
