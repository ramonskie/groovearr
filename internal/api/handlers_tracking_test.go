package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/sse"
	"github.com/ramonskie/groovearr/internal/tracking"
)

// ─── Fake tracking service ───────────────────────────────────────────

// fakeTrackingService implements trackingService and jobs.TrackedRefresher so
// one value can back both the handler layer and the job runners the handlers
// start. Errors are injected per method; a nil injected error falls through.
type fakeTrackingService struct {
	artists []domain.TrackedArtist
	artist  *domain.TrackedArtist
	albums  []domain.TrackedAlbum
	wanted  []domain.TrackedAlbum

	listArtistsErr error
	getArtistErr   error
	addErr         error
	monitorErr     error
	deleteErr      error
	listAlbumsErr  error
	albumMonErr    error
	albumStatusErr error
	wantedErr      error

	addCalls      int
	addName       string
	addProvider   string
	addProviderID string
	addOpts       tracking.AddArtistOptions

	deleteCalls    int
	monitorCalls   int
	getArtistCalls int
	lastMonitorID  int64
	lastMonitored  bool
	lastMode       domain.MonitorMode

	albumMonCalls    int
	lastAlbumID      int64
	lastAlbumMon     bool
	albumStatusCalls int
	lastAlbumStatus  domain.AlbumStatus
}

var (
	_ trackingService       = (*fakeTrackingService)(nil)
	_ jobs.TrackedRefresher = (*fakeTrackingService)(nil)
)

func (f *fakeTrackingService) ListTrackedArtists(context.Context) ([]domain.TrackedArtist, error) {
	if f.listArtistsErr != nil {
		return nil, f.listArtistsErr
	}
	return f.artists, nil
}

func (f *fakeTrackingService) GetTrackedArtist(context.Context, int64) (*domain.TrackedArtist, error) {
	f.getArtistCalls++
	if f.getArtistErr != nil {
		return nil, f.getArtistErr
	}
	return f.artist, nil
}

func (f *fakeTrackingService) AddArtist(_ context.Context, providerName, providerArtistID, name string, opts tracking.AddArtistOptions) (*domain.TrackedArtist, error) {
	f.addCalls++
	f.addProvider = providerName
	f.addProviderID = providerArtistID
	f.addName = name
	f.addOpts = opts
	if f.addErr != nil {
		return nil, f.addErr
	}
	if f.artist != nil {
		return f.artist, nil
	}
	return &domain.TrackedArtist{ID: 1, Name: name, ProviderName: providerName, ProviderArtistID: providerArtistID}, nil
}

func (f *fakeTrackingService) SetArtistMonitor(_ context.Context, artistID int64, monitored bool, mode domain.MonitorMode) error {
	f.monitorCalls++
	f.lastMonitorID = artistID
	f.lastMonitored = monitored
	f.lastMode = mode
	if f.monitorErr == nil && f.artist != nil {
		f.artist.Monitored = monitored
		f.artist.MonitorMode = mode
		// Simulate the store refreshing updated_at so the handler's post-write
		// re-read proves freshness.
		f.artist.UpdatedAt = time.Now().UTC()
	}
	return f.monitorErr
}

func (f *fakeTrackingService) DeleteTrackedArtist(context.Context, int64) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakeTrackingService) ListAlbums(context.Context, int64) ([]domain.TrackedAlbum, error) {
	if f.listAlbumsErr != nil {
		return nil, f.listAlbumsErr
	}
	return f.albums, nil
}

func (f *fakeTrackingService) SetAlbumMonitored(_ context.Context, albumID int64, monitored bool) error {
	f.albumMonCalls++
	f.lastAlbumID = albumID
	f.lastAlbumMon = monitored
	return f.albumMonErr
}

func (f *fakeTrackingService) SetAlbumStatus(_ context.Context, albumID int64, status domain.AlbumStatus) error {
	f.albumStatusCalls++
	f.lastAlbumStatus = status
	return f.albumStatusErr
}

func (f *fakeTrackingService) ListAllWanted(context.Context) ([]domain.TrackedAlbum, error) {
	if f.wantedErr != nil {
		return nil, f.wantedErr
	}
	return f.wanted, nil
}

// RefreshArtist / SearchMissing satisfy jobs.TrackedRefresher so the job
// endpoints can start a runner that completes cleanly in tests.
func (f *fakeTrackingService) RefreshArtist(context.Context, int64) (*tracking.RefreshResult, error) {
	return &tracking.RefreshResult{}, nil
}

func (f *fakeTrackingService) SearchMissing(context.Context, int64) (*tracking.SearchResult, error) {
	return &tracking.SearchResult{}, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────

func newTrackingServer(svc trackingService) *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := jobs.RunnerDeps{Log: logger}
	if tr, ok := svc.(jobs.TrackedRefresher); ok {
		deps.Tracking = tr
	}
	return &Server{
		log:         logger,
		jobs:        jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		runners:     jobs.NewRunners(deps),
		trackingSvc: svc,
	}
}

func doTracking(t *testing.T, s *Server, method, target, body string, pathVals map[string]string, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range pathVals {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// ─── GET /api/tracking/artists ───────────────────────────────────────

func TestHandleListTrackedArtists(t *testing.T) {
	tests := []struct {
		name     string
		svc      trackingService
		wantCode int
	}{
		{"ok", &fakeTrackingService{artists: []domain.TrackedArtist{{ID: 1, Name: "Tool"}}}, http.StatusOK},
		{"empty returns an array", &fakeTrackingService{}, http.StatusOK},
		{"service error", &fakeTrackingService{listArtistsErr: errors.New("boom")}, http.StatusInternalServerError},
		{"nil service is unavailable", nil, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodGet, "/api/tracking/artists", "", nil, s.handleListTrackedArtists)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusOK && strings.TrimSpace(rec.Body.String()) == "null" {
				t.Fatalf("empty list must encode as [], got null")
			}
		})
	}
}

// ─── POST /api/tracking/artists ──────────────────────────────────────

func TestHandleAddTrackedArtist(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		svc      *fakeTrackingService
		wantCode int
	}{
		{
			name:     "ok creates and returns 201",
			body:     `{"provider_name":"deezer","provider_artist_id":"art1","name":"Tool","monitor_mode":"all"}`,
			svc:      &fakeTrackingService{},
			wantCode: http.StatusCreated,
		},
		{
			name:     "missing provider_name is rejected",
			body:     `{"provider_artist_id":"art1"}`,
			svc:      &fakeTrackingService{},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing provider_artist_id is rejected",
			body:     `{"provider_name":"deezer"}`,
			svc:      &fakeTrackingService{},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "unknown monitor_mode is rejected",
			body:     `{"provider_name":"deezer","provider_artist_id":"art1","monitor_mode":"weekly"}`,
			svc:      &fakeTrackingService{},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "service error",
			body:     `{"provider_name":"deezer","provider_artist_id":"art1"}`,
			svc:      &fakeTrackingService{addErr: errors.New("boom")},
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "unregistered provider maps to 400",
			body:     `{"provider_name":"nope","provider_artist_id":"art1"}`,
			svc:      &fakeTrackingService{addErr: tracking.ErrProviderNotRegistered},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "cooling provider maps to 503",
			body:     `{"provider_name":"deezer","provider_artist_id":"art1"}`,
			svc:      &fakeTrackingService{addErr: tracking.ErrProviderCoolingDown},
			wantCode: http.StatusServiceUnavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodPost, "/api/tracking/artists", tc.body, nil, s.handleAddTrackedArtist)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusCreated {
				if tc.svc.addCalls != 1 {
					t.Fatalf("AddArtist calls = %d, want 1", tc.svc.addCalls)
				}
				if tc.svc.addOpts.MonitorMode != domain.MonitorModeAll {
					t.Fatalf("monitor_mode = %q, want all", tc.svc.addOpts.MonitorMode)
				}
				var got domain.TrackedArtist
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("bad JSON: %v", err)
				}
				if got.ProviderName != "deezer" || got.ProviderArtistID != "art1" {
					t.Fatalf("unexpected artist: %+v", got)
				}
			}
		})
	}
}

// TestHandleAddTrackedArtistSearchOnAdd proves the optional search_on_add body
// field is forwarded into AddArtistOptions.SearchOnAdd, and that omitting it
// defaults to false (Lidarr's default).
func TestHandleAddTrackedArtistSearchOnAdd(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "true is forwarded",
			body: `{"provider_name":"deezer","provider_artist_id":"art1","search_on_add":true}`,
			want: true,
		},
		{
			name: "omitted defaults false",
			body: `{"provider_name":"deezer","provider_artist_id":"art1"}`,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeTrackingService{}
			s := newTrackingServer(svc)
			rec := doTracking(t, s, http.MethodPost, "/api/tracking/artists", tc.body, nil, s.handleAddTrackedArtist)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
			}
			if svc.addCalls != 1 {
				t.Fatalf("AddArtist calls = %d, want 1", svc.addCalls)
			}
			if svc.addOpts.SearchOnAdd != tc.want {
				t.Fatalf("SearchOnAdd = %v, want %v", svc.addOpts.SearchOnAdd, tc.want)
			}
		})
	}
}

func TestHandleAddTrackedArtistNilService(t *testing.T) {
	s := newTrackingServer(nil)
	rec := doTracking(t, s, http.MethodPost, "/api/tracking/artists", `{}`, nil, s.handleAddTrackedArtist)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// ─── GET /api/tracking/artists/{artistID} ────────────────────────────

func TestHandleGetTrackedArtist(t *testing.T) {
	artist := &domain.TrackedArtist{ID: 7, Name: "Tool"}
	tests := []struct {
		name     string
		id       string
		svc      trackingService
		wantCode int
	}{
		{"ok returns artist and albums", "7", &fakeTrackingService{artist: artist, albums: []domain.TrackedAlbum{{ID: 1, Title: "Lateralus"}}}, http.StatusOK},
		{"not found", "7", &fakeTrackingService{}, http.StatusNotFound},
		{"non-numeric id", "abc", &fakeTrackingService{artist: artist}, http.StatusBadRequest},
		{"service error", "7", &fakeTrackingService{getArtistErr: errors.New("boom")}, http.StatusInternalServerError},
		{"nil service", "7", nil, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodGet, "/api/tracking/artists/"+tc.id, "", map[string]string{"artistID": tc.id}, s.handleGetTrackedArtist)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusOK {
				var got struct {
					Artist *domain.TrackedArtist `json:"artist"`
					Albums []domain.TrackedAlbum `json:"albums"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("bad JSON: %v", err)
				}
				if got.Artist == nil || got.Artist.ID != 7 {
					t.Fatalf("artist = %+v, want id 7", got.Artist)
				}
				if len(got.Albums) != 1 {
					t.Fatalf("albums = %+v, want 1", got.Albums)
				}
			}
		})
	}
}

// ─── PATCH /api/tracking/artists/{artistID} ──────────────────────────

func TestHandleUpdateTrackedArtist(t *testing.T) {
	artist := &domain.TrackedArtist{ID: 7, Name: "Tool", Monitored: true, MonitorMode: domain.MonitorModeAll}
	tests := []struct {
		name          string
		body          string
		svc           trackingService
		wantCode      int
		wantMonitored bool
		wantMode      domain.MonitorMode
	}{
		{"ok sets monitored", `{"monitored":false}`, &fakeTrackingService{artist: artist}, http.StatusOK, false, domain.MonitorModeAll},
		{"none mode forces unmonitored", `{"monitor_mode":"none"}`, &fakeTrackingService{artist: artist}, http.StatusOK, false, domain.MonitorModeNone},
		{"future mode keeps explicit monitored", `{"monitored":true,"monitor_mode":"future"}`, &fakeTrackingService{artist: artist}, http.StatusOK, true, domain.MonitorModeFuture},
		{"bad mode", `{"monitor_mode":"weekly"}`, &fakeTrackingService{artist: artist}, http.StatusBadRequest, false, ""},
		{"nothing to update", `{}`, &fakeTrackingService{artist: artist}, http.StatusBadRequest, false, ""},
		{"not found", `{"monitored":false}`, &fakeTrackingService{}, http.StatusNotFound, false, ""},
		{"service error", `{"monitored":false}`, &fakeTrackingService{getArtistErr: errors.New("boom")}, http.StatusInternalServerError, false, ""},
		{"nil service", `{"monitored":false}`, nil, http.StatusServiceUnavailable, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodPatch, "/api/tracking/artists/7", tc.body, map[string]string{"artistID": "7"}, s.handleUpdateTrackedArtist)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			fake, ok := tc.svc.(*fakeTrackingService)
			if !ok || fake.monitorCalls != 1 {
				t.Fatalf("SetArtistMonitor calls = %v, want 1", fake)
			}
			if fake.lastMonitored != tc.wantMonitored || fake.lastMode != tc.wantMode {
				t.Fatalf("monitor = (%v,%q), want (%v,%q)", fake.lastMonitored, fake.lastMode, tc.wantMonitored, tc.wantMode)
			}
		})
	}
}

// ─── DELETE /api/tracking/artists/{artistID} ─────────────────────────

func TestHandleDeleteTrackedArtist(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		svc      trackingService
		wantCode int
		wantCall bool
	}{
		{"ok", "7", &fakeTrackingService{}, http.StatusOK, true},
		{"non-numeric id", "abc", &fakeTrackingService{}, http.StatusBadRequest, false},
		{"service error", "7", &fakeTrackingService{deleteErr: errors.New("boom")}, http.StatusInternalServerError, true},
		{"nil service", "7", nil, http.StatusServiceUnavailable, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodDelete, "/api/tracking/artists/"+tc.id, "", map[string]string{"artistID": tc.id}, s.handleDeleteTrackedArtist)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if fake, ok := tc.svc.(*fakeTrackingService); ok && tc.wantCall && fake.deleteCalls != 1 {
				t.Fatalf("DeleteTrackedArtist calls = %d, want 1", fake.deleteCalls)
			}
		})
	}
}

// ─── GET /api/tracking/artists/{artistID}/albums ─────────────────────

func TestHandleListTrackedAlbums(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		svc      trackingService
		wantCode int
	}{
		{"ok", "7", &fakeTrackingService{albums: []domain.TrackedAlbum{{ID: 1, Title: "Lateralus"}}}, http.StatusOK},
		{"empty returns an array", "7", &fakeTrackingService{}, http.StatusOK},
		{"non-numeric id", "abc", &fakeTrackingService{}, http.StatusBadRequest},
		{"service error", "7", &fakeTrackingService{listAlbumsErr: errors.New("boom")}, http.StatusInternalServerError},
		{"nil service", "7", nil, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodGet, "/api/tracking/artists/"+tc.id+"/albums", "", map[string]string{"artistID": tc.id}, s.handleListTrackedAlbums)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusOK && strings.TrimSpace(rec.Body.String()) == "null" {
				t.Fatalf("empty list must encode as [], got null")
			}
		})
	}
}

// ─── GET /api/tracking/wanted ────────────────────────────────────────

func TestHandleListAllWanted(t *testing.T) {
	tests := []struct {
		name     string
		svc      trackingService
		wantCode int
	}{
		{"ok", &fakeTrackingService{wanted: []domain.TrackedAlbum{{ID: 1, Title: "Lateralus"}}}, http.StatusOK},
		{"empty returns an array", &fakeTrackingService{}, http.StatusOK},
		{"service error", &fakeTrackingService{wantedErr: errors.New("boom")}, http.StatusInternalServerError},
		{"nil service", nil, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodGet, "/api/tracking/wanted", "", nil, s.handleListAllWanted)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusOK && strings.TrimSpace(rec.Body.String()) == "null" {
				t.Fatalf("empty list must encode as [], got null")
			}
		})
	}
}

// ─── PATCH /api/tracking/albums/{albumID} ────────────────────────────

func TestHandleUpdateTrackedAlbum(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		body       string
		svc        trackingService
		wantCode   int
		wantAlbum  bool
		wantMonVal bool
	}{
		{"ok", "5", `{"monitored":true}`, &fakeTrackingService{}, http.StatusOK, true, true},
		{"missing monitored", "5", `{}`, &fakeTrackingService{}, http.StatusBadRequest, false, false},
		{"non-numeric id", "abc", `{"monitored":true}`, &fakeTrackingService{}, http.StatusBadRequest, false, false},
		{"service error", "5", `{"monitored":false}`, &fakeTrackingService{albumMonErr: errors.New("boom")}, http.StatusInternalServerError, true, false},
		{"nil service", "5", `{"monitored":true}`, nil, http.StatusServiceUnavailable, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodPatch, "/api/tracking/albums/"+tc.id, tc.body, map[string]string{"albumID": tc.id}, s.handleUpdateTrackedAlbum)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if fake, ok := tc.svc.(*fakeTrackingService); ok && tc.wantAlbum {
				if fake.albumMonCalls != 1 || fake.lastAlbumMon != tc.wantMonVal {
					t.Fatalf("SetAlbumMonitored = (%d,%v), want (1,%v)", fake.albumMonCalls, fake.lastAlbumMon, tc.wantMonVal)
				}
			}
		})
	}
}

// TestHandleUpdateTrackedAlbumStatus proves the PATCH album handler accepts a
// status field: wanted|ignored reach SetAlbumStatus, an invalid status is a 400
// and an unknown album a 404 (both via service sentinels), and the response
// carries album_status.
func TestHandleUpdateTrackedAlbumStatus(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		svc         trackingService
		wantCode    int
		wantStatus  domain.AlbumStatus
		wantCalls   int
		wantMonCall int
	}{
		{"ignored is applied", `{"status":"ignored"}`, &fakeTrackingService{}, http.StatusOK, domain.AlbumStatusIgnored, 1, 0},
		{"wanted is applied", `{"status":"wanted"}`, &fakeTrackingService{}, http.StatusOK, domain.AlbumStatusWanted, 1, 0},
		{"monitored and status together", `{"monitored":true,"status":"wanted"}`, &fakeTrackingService{}, http.StatusOK, domain.AlbumStatusWanted, 1, 1},
		{"invalid status maps to 400", `{"status":"downloaded"}`, &fakeTrackingService{albumStatusErr: tracking.ErrAlbumStatusInvalid}, http.StatusBadRequest, "", 1, 0},
		{"unknown album maps to 404", `{"status":"ignored"}`, &fakeTrackingService{albumStatusErr: tracking.ErrAlbumNotFound}, http.StatusNotFound, "", 1, 0},
		{"service error maps to 500", `{"status":"ignored"}`, &fakeTrackingService{albumStatusErr: errors.New("boom")}, http.StatusInternalServerError, "", 1, 0},
		{"nothing to update", `{}`, &fakeTrackingService{}, http.StatusBadRequest, "", 0, 0},
		{"nil service", `{"status":"ignored"}`, nil, http.StatusServiceUnavailable, "", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			rec := doTracking(t, s, http.MethodPatch, "/api/tracking/albums/5", tc.body, map[string]string{"albumID": "5"}, s.handleUpdateTrackedAlbum)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if fake, ok := tc.svc.(*fakeTrackingService); ok {
				if fake.albumStatusCalls != tc.wantCalls {
					t.Fatalf("SetAlbumStatus calls = %d, want %d", fake.albumStatusCalls, tc.wantCalls)
				}
				if fake.albumMonCalls != tc.wantMonCall {
					t.Fatalf("SetAlbumMonitored calls = %d, want %d", fake.albumMonCalls, tc.wantMonCall)
				}
				if tc.wantCode == http.StatusOK && fake.lastAlbumStatus != tc.wantStatus {
					t.Fatalf("status forwarded = %q, want %q", fake.lastAlbumStatus, tc.wantStatus)
				}
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			var resp struct {
				Status      string `json:"status"`
				AlbumStatus string `json:"album_status"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("bad JSON: %v", err)
			}
			if resp.AlbumStatus != string(tc.wantStatus) {
				t.Fatalf("album_status = %q, want %q", resp.AlbumStatus, tc.wantStatus)
			}
		})
	}
}

// TestHandleUpdateTrackedArtistRefreshesUpdatedAt proves the handler re-reads
// the artist after the write so the response carries the store-refreshed
// updated_at instead of the stale pre-update snapshot.
func TestHandleUpdateTrackedArtistRefreshesUpdatedAt(t *testing.T) {
	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	artist := &domain.TrackedArtist{ID: 7, Name: "Tool", Monitored: true, MonitorMode: domain.MonitorModeAll, UpdatedAt: stale}
	svc := &fakeTrackingService{artist: artist}
	s := newTrackingServer(svc)

	rec := doTracking(t, s, http.MethodPatch, "/api/tracking/artists/7", `{"monitored":false}`, map[string]string{"artistID": "7"}, s.handleUpdateTrackedArtist)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if svc.getArtistCalls != 2 {
		t.Fatalf("GetTrackedArtist calls = %d, want 2 (resolve + re-read)", svc.getArtistCalls)
	}
	var got domain.TrackedArtist
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if !got.UpdatedAt.After(stale) {
		t.Fatalf("updated_at = %v, want refreshed after %v", got.UpdatedAt, stale)
	}
}

// ─── Job-backed endpoints ────────────────────────────────────────────
func TestTrackingJobEndpoints(t *testing.T) {
	artist := &domain.TrackedArtist{ID: 7, Name: "Tool", ProviderName: "deezer"}

	tests := []struct {
		name      string
		id        string
		svc       trackingService
		handler   func(*Server) http.HandlerFunc
		wantCode  int
		wantType  string
		wantStart bool
	}{
		{"refresh one artist starts tracked-refresh", "7", &fakeTrackingService{artist: artist}, func(s *Server) http.HandlerFunc { return s.handleRefreshTrackedArtist }, http.StatusAccepted, "tracked-refresh", true},
		{"search missing starts tracked-search", "7", &fakeTrackingService{artist: artist}, func(s *Server) http.HandlerFunc { return s.handleSearchMissingArtist }, http.StatusAccepted, "tracked-search", true},
		{"refresh all starts tracked-refresh", "", &fakeTrackingService{}, func(s *Server) http.HandlerFunc { return s.handleRefreshAllTracked }, http.StatusAccepted, "tracked-refresh", true},
		{"refresh one artist rejects non-numeric id", "abc", &fakeTrackingService{artist: artist}, func(s *Server) http.HandlerFunc { return s.handleRefreshTrackedArtist }, http.StatusBadRequest, "", false},
		{"search missing rejects non-numeric id", "abc", &fakeTrackingService{artist: artist}, func(s *Server) http.HandlerFunc { return s.handleSearchMissingArtist }, http.StatusBadRequest, "", false},
		{"nil service is unavailable", "", nil, func(s *Server) http.HandlerFunc { return s.handleRefreshAllTracked }, http.StatusServiceUnavailable, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTrackingServer(tc.svc)
			pathVals := map[string]string{}
			target := "/api/tracking/refresh"
			if tc.id != "" {
				pathVals["artistID"] = tc.id
				target = "/api/tracking/artists/" + tc.id + "/refresh"
			}
			rec := doTracking(t, s, http.MethodPost, target, "", pathVals, tc.handler(s))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode != http.StatusAccepted {
				return
			}
			var resp struct {
				Job     *jobs.Job `json:"job"`
				Started bool      `json:"started"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("bad JSON: %v", err)
			}
			if resp.Job == nil || resp.Job.Type != tc.wantType {
				t.Fatalf("job = %+v, want type %q", resp.Job, tc.wantType)
			}
			if resp.Started != tc.wantStart {
				t.Fatalf("started = %v, want %v", resp.Started, tc.wantStart)
			}
			// Let the started job settle so it does not outlive the test.
			waitJobState(t, s, jobs.StateCompleted)
		})
	}
}
