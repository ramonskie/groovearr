package api

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/download"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/user"
)

// stubImportPlaylistSource is a minimal playlist.Source for handler tests.
type stubImportPlaylistSource struct {
	playlists []playlist.PlaylistInfo
}

func (s *stubImportPlaylistSource) Name() string        { return "mock" }
func (s *stubImportPlaylistSource) DisplayName() string { return "Mock" }
func (s *stubImportPlaylistSource) IsConfigured() bool  { return true }

func (s *stubImportPlaylistSource) GetUserPlaylists(context.Context) ([]playlist.PlaylistInfo, error) {
	return s.playlists, nil
}

func (s *stubImportPlaylistSource) GetPlaylistTracks(context.Context, string) ([]playlist.TrackInfo, string, error) {
	return nil, "Mix", nil
}

// newImportPlaylistServer wires a real playlist.Service over a temp SQLite store
// so the handler→service→store attribution path is exercised end to end.
func newImportPlaylistServer(t *testing.T, playlistID string) (*Server, *libsqlite.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	libStore, err := libsqlite.New(t.TempDir()+"/test.db", logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	reg := playlist.NewRegistry()
	if err := reg.Register(&stubImportPlaylistSource{
		playlists: []playlist.PlaylistInfo{{SourceID: playlistID, Name: "Mix"}},
	}); err != nil {
		t.Fatalf("register source: %v", err)
	}

	svc := playlist.NewService(reg, libStore, download.NewRegistry(), nil,
		func() config.Config { return config.Config{} }, nil, nil, logger)

	return &Server{playlistSvc: svc, log: logger}, libStore
}

func newImportRequest(t *testing.T, playlistID, body string) *http.Request {
	t.Helper()
	if body == "" {
		body = `{"source":"mock","playlist_id":"` + playlistID + `"}`
	}
	return httptest.NewRequest(http.MethodPost, "/api/playlists/import", strings.NewReader(body))
}

// TestHandleImportPlaylistStampsImporter verifies the authenticated caller's
// identity is threaded from the request context onto the created playlist row.
func TestHandleImportPlaylistStampsImporter(t *testing.T) {
	s, libStore := newImportPlaylistServer(t, "pl-1")
	defer libStore.Close()

	req := newImportRequest(t, "pl-1", "")
	req = req.WithContext(contextWithIdentity(req.Context(), Identity{
		UserID:   7,
		Username: "alice",
		Role:     user.RoleUser,
	}))

	rec := httptest.NewRecorder()
	s.handleImportPlaylist(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var uid sql.NullInt64
	var name string
	if err := libStore.DB().QueryRow(
		`SELECT added_by_user_id, added_by_username FROM playlists WHERE source='mock' AND source_playlist_id='pl-1'`,
	).Scan(&uid, &name); err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if !uid.Valid || uid.Int64 != 7 || name != "alice" {
		t.Errorf("attribution = (%v, %q), want (7, alice)", uid, name)
	}
}

// TestHandleImportPlaylistSystemAttributionBlank verifies a request with no
// identity (e.g. auth.method="none" / API key) leaves attribution blank.
func TestHandleImportPlaylistSystemAttributionBlank(t *testing.T) {
	s, libStore := newImportPlaylistServer(t, "pl-2")
	defer libStore.Close()

	rec := httptest.NewRecorder()
	s.handleImportPlaylist(rec, newImportRequest(t, "pl-2", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var uid sql.NullInt64
	var name string
	if err := libStore.DB().QueryRow(
		`SELECT added_by_user_id, added_by_username FROM playlists WHERE source='mock' AND source_playlist_id='pl-2'`,
	).Scan(&uid, &name); err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if uid.Valid || name != "" {
		t.Errorf("system attribution = (%v, %q), want (NULL, \"\")", uid, name)
	}
}
