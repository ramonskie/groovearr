package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/tracking"
)

// trackingService is the contract the api server needs from the tracking
// service: the subset the HTTP handlers call plus jobs.TrackedRefresher, which
// the job Manager runs for refresh/search-missing so long provider work never
// blocks an HTTP handler (AGENTS §2 flow discipline). It is declared here (not
// as the concrete *tracking.Service) so the api package depends on behavior
// rather than construction (AGENTS §3 dependency inversion). *tracking.Service
// satisfies it; it is wired in cmd/groovearr/app.go.
type trackingService interface {
	// Embedded so the one wired *tracking.Service both serves the handlers
	// below and is passed to the job Manager as a jobs.TrackedRefresher.
	// api already imports jobs, and tracking does not import api, so this
	// introduces no cycle.
	jobs.TrackedRefresher

	AddArtist(ctx context.Context, providerName, providerArtistID, name string, opts tracking.AddArtistOptions) (*domain.TrackedArtist, error)
	SetArtistMonitor(ctx context.Context, artistID int64, monitored bool, mode domain.MonitorMode) error
	DeleteTrackedArtist(ctx context.Context, artistID int64) error
	ListAlbums(ctx context.Context, artistID int64) ([]domain.TrackedAlbum, error)
	SetAlbumMonitored(ctx context.Context, albumID int64, monitored bool) error
	SetAlbumStatus(ctx context.Context, albumID int64, status domain.AlbumStatus) error
	ListAllWanted(ctx context.Context) ([]domain.TrackedAlbum, error)
}

// Compile-time guarantee that the concrete tracking service satisfies the
// handler contract, so wiring it in cmd/groovearr/app.go (subtask 09) stays
// type-safe. tracking does not import api, so this introduces no cycle.
var _ trackingService = (*tracking.Service)(nil)

// Compile-time guarantee that the wired service is also a jobs.TrackedRefresher,
// so RunnerDeps.Tracking (the tracked-refresh job) stays type-safe. jobs does
// not import api, so this introduces no cycle.
var _ jobs.TrackedRefresher = (*tracking.Service)(nil)

// errTrackingUnavailable is returned (503) when no tracking service is wired.
// app.go always wires one, so this is a defensive guard for a Server built
// without it (e.g. in tests); every tracking route then degrades cleanly.
var errTrackingUnavailable = errors.New("tracking service not available")

// validTrackingMonitorMode reports whether mode is one of the three supported
// monitor modes. The handler validates at the boundary so an unknown mode is a
// 400 before any service call (AGENTS §4, §10).
func validTrackingMonitorMode(mode domain.MonitorMode) bool {
	switch mode {
	case domain.MonitorModeAll, domain.MonitorModeFuture, domain.MonitorModeNone:
		return true
	default:
		return false
	}
}

// parseTrackingID parses a numeric path ID, returning the same "invalid ID"
// 400 the existing handlers use on a non-numeric value.
func parseTrackingID(raw, label string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s ID", label)
	}
	return id, nil
}

// handleListTrackedArtists returns every tracked artist.
func (s *Server) handleListTrackedArtists(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	artists, err := s.trackingSvc.ListTrackedArtists(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artists == nil {
		artists = []domain.TrackedArtist{}
	}
	writeJSON(w, http.StatusOK, artists)
}

// handleAddTrackedArtist starts tracking an artist and returns the persisted
// artist immediately (201). AddArtist is synchronous by design: it reconciles
// the initial discography, and the UI needs the created row to render.
func (s *Server) handleAddTrackedArtist(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	var req struct {
		ProviderName     string `json:"provider_name"`
		ProviderArtistID string `json:"provider_artist_id"`
		Name             string `json:"name"`
		Monitored        *bool  `json:"monitored"`
		MonitorMode      string `json:"monitor_mode"`
		AutoRefresh      *bool  `json:"auto_refresh"`
		SearchOnAdd      bool   `json:"search_on_add"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.ProviderName == "" || req.ProviderArtistID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("provider_name and provider_artist_id are required"))
		return
	}
	mode := domain.MonitorMode(req.MonitorMode)
	if mode != "" && !validTrackingMonitorMode(mode) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("monitor_mode must be one of all|future|none"))
		return
	}

	artist, err := s.trackingSvc.AddArtist(r.Context(), req.ProviderName, req.ProviderArtistID, req.Name, tracking.AddArtistOptions{
		Monitored:   req.Monitored,
		MonitorMode: mode,
		AutoRefresh: req.AutoRefresh,
		SearchOnAdd: req.SearchOnAdd,
	})
	if err != nil {
		writeTrackingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, artist)
}

// writeTrackingError maps the tracking service's exported sentinels to the
// HTTP status the caller should see (AGENTS §10). An unregistered provider is
// a client error (400) and a cooling-down provider is a transient unavailability
// (503); anything else stays a 500.
func writeTrackingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tracking.ErrProviderNotRegistered):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, tracking.ErrProviderCoolingDown):
		writeError(w, http.StatusServiceUnavailable, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// handleGetTrackedArtist returns one tracked artist together with its
// discovered albums ({artist, albums}).
func (s *Server) handleGetTrackedArtist(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	id, err := parseTrackingID(r.PathValue("artistID"), "artist")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	artist, err := s.trackingSvc.GetTrackedArtist(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("tracked artist not found"))
		return
	}
	albums, err := s.trackingSvc.ListAlbums(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.TrackedAlbum{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"artist": artist, "albums": albums})
}

// handleUpdateTrackedArtist sets the monitored flag and monitor mode of a
// tracked artist. Either field may be omitted; omitted fields keep the
// artist's current value. An unknown monitor_mode is a 400.
func (s *Server) handleUpdateTrackedArtist(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	id, err := parseTrackingID(r.PathValue("artistID"), "artist")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		Monitored   *bool   `json:"monitored"`
		MonitorMode *string `json:"monitor_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Monitored == nil && req.MonitorMode == nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("monitored or monitor_mode required"))
		return
	}
	var mode domain.MonitorMode
	if req.MonitorMode != nil {
		mode = domain.MonitorMode(*req.MonitorMode)
		if !validTrackingMonitorMode(mode) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("monitor_mode must be one of all|future|none"))
			return
		}
	}

	ctx := r.Context()
	artist, err := s.trackingSvc.GetTrackedArtist(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("tracked artist not found"))
		return
	}
	if mode == "" {
		mode = artist.MonitorMode
	}
	monitored := artist.Monitored
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	// mode "none" always wins: the artist is tracked without acquisition.
	if mode == domain.MonitorModeNone {
		monitored = false
	}
	if err := s.trackingSvc.SetArtistMonitor(ctx, id, monitored, mode); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Re-read after the write so updated_at (and the applied monitor fields) in
	// the response are current rather than echoing the pre-update snapshot.
	updated, err := s.trackingSvc.GetTrackedArtist(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if updated == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("tracked artist not found"))
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteTrackedArtist removes a tracked artist and (via store cascade)
// its discovered albums.
func (s *Server) handleDeleteTrackedArtist(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	id, err := parseTrackingID(r.PathValue("artistID"), "artist")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.trackingSvc.DeleteTrackedArtist(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleListTrackedAlbums returns every album discovered for a tracked artist.
func (s *Server) handleListTrackedAlbums(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	id, err := parseTrackingID(r.PathValue("artistID"), "artist")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	albums, err := s.trackingSvc.ListAlbums(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.TrackedAlbum{}
	}
	writeJSON(w, http.StatusOK, albums)
}

// handleUpdateTrackedAlbum sets the monitored flag and/or acquisition status
// of a single tracked album. At least one of monitored|status is required;
// status accepts only wanted|ignored (400 otherwise) and an unknown album is a
// 404. The response keeps status/id and adds monitored (when set) and
// album_status (when status was set).
func (s *Server) handleUpdateTrackedAlbum(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	id, err := parseTrackingID(r.PathValue("albumID"), "album")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		Monitored *bool   `json:"monitored"`
		Status    *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Monitored == nil && req.Status == nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("monitored or status required"))
		return
	}
	ctx := r.Context()
	// Status is validated by the service (wanted|ignored only); the service
	// returns ErrAlbumStatusInvalid (400) or ErrAlbumNotFound (404).
	if req.Status != nil {
		if err := s.trackingSvc.SetAlbumStatus(ctx, id, domain.AlbumStatus(*req.Status)); err != nil {
			writeTrackingAlbumError(w, err)
			return
		}
	}
	if req.Monitored != nil {
		if err := s.trackingSvc.SetAlbumMonitored(ctx, id, *req.Monitored); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Preserve the existing response shape and add album_status when the caller
	// set one.
	resp := map[string]any{"status": "updated", "id": id}
	if req.Monitored != nil {
		resp["monitored"] = *req.Monitored
	}
	if req.Status != nil {
		resp["album_status"] = *req.Status
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeTrackingAlbumError maps the album-status service sentinels to HTTP:
// an invalid status is a 400 and an unknown album a 404 (AGENTS §10); anything
// else stays a 500.
func writeTrackingAlbumError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tracking.ErrAlbumStatusInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, tracking.ErrAlbumNotFound):
		writeError(w, http.StatusNotFound, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// handleListAllWanted returns every wanted/downloading album across all tracked
// artists (the global wanted view).
func (s *Server) handleListAllWanted(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	albums, err := s.trackingSvc.ListAllWanted(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.TrackedAlbum{}
	}
	writeJSON(w, http.StatusOK, albums)
}

// ─── Job-backed actions ──────────────────────────────────────────────
//
// Refresh and search-missing do provider I/O, so they run through the job
// Manager (single-flight, cancellable, SSE progress, persisted) rather than in
// the request goroutine (AGENTS §2). The handlers only pick a runner.

// handleRefreshTrackedArtist starts a refresh of one artist's discography.
func (s *Server) handleRefreshTrackedArtist(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil || s.runners == nil || s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	id, err := parseTrackingID(r.PathValue("artistID"), "artist")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.startJob(w, "tracked-refresh", s.runners.RefreshTrackedArtist(id))
}

// handleSearchMissingArtist starts a search for one artist's missing albums.
func (s *Server) handleSearchMissingArtist(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil || s.runners == nil || s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	id, err := parseTrackingID(r.PathValue("artistID"), "artist")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.startJob(w, "tracked-search", s.runners.SearchMissingArtist(id))
}

// handleRefreshAllTracked starts a refresh of every auto-refresh artist.
func (s *Server) handleRefreshAllTracked(w http.ResponseWriter, r *http.Request) {
	if s.trackingSvc == nil || s.runners == nil || s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, errTrackingUnavailable)
		return
	}
	s.startJob(w, "tracked-refresh", s.runners.RefreshTracked())
}

// StartTrackedRefreshScheduler runs the periodic tracked-artist refresh through
// the existing job Manager: every interval it starts the same "tracked-refresh"
// job the HTTP endpoint uses (runners.RefreshTracked()) via runPersistedJob, so
// single-flight, SSE progress, cancellation, and boot persistence all apply —
// scheduled runs are persisted and restored exactly like HTTP-triggered ones.
// Because scheduled and HTTP-triggered runs share the one "tracked-refresh" job
// type, they also share the Manager's single-flight slot: a scheduled tick
// during a user-triggered refresh (or vice versa) is skipped. The tick is never
// queued, so a slow refresh cannot build an unbounded backlog. The loop stops
// on ctx.Done() and blocks nothing at exit, so it never delays Shutdown().
// interval <= 0 (or a nil server) is a no-op.
func (s *Server) StartTrackedRefreshScheduler(ctx context.Context, interval time.Duration) {
	if s == nil || s.jobs == nil || s.runners == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.jobs.Start("tracked-refresh", s.runPersistedJob("tracked-refresh", s.runners.RefreshTracked())); err != nil {
					if errors.Is(err, jobs.ErrBusy) {
						s.log.Debug("tracked refresh skipped: a job is already running", "component", "api")
						continue
					}
					s.log.Warn("tracked refresh scheduler failed to start job", "error", err, "component", "api")
				}
			}
		}
	}()
}
