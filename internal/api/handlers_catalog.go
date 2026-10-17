package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/refresh"
)

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
