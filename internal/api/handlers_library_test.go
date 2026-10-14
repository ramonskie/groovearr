package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

func testAPILogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubLibraryStore embeds the nil library.Store interface and overrides only
// the methods the handlers under test call. Calling any other Store method
// panics, which is fine for these narrow handler tests.
type stubLibraryStore struct {
	library.Store
	artists      []domain.Artist
	tracks       map[int64][]domain.Track
	searchTracks []domain.Track
	searchAlbums []domain.Album
	artistAlbums map[int64][]domain.Album
	albums       map[int64]*domain.Album
	albumTrks    map[int64][]domain.Track
	merges       [][2]int64 // (keep, remove)
	renames      []struct {
		id   int64
		name string
	}
	scan map[string]string // duplicate_scan: group_key → canonical
}

// paginateSlice applies offset/limit the same way the store does, so handler
// tests can assert non-zero offsets end-to-end. A limit <= 0 means no upper
// bound (the store's default is always positive through parsePagination).
func paginateSlice[T any](items []T, offset, limit int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:]
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

var _ jobs.DuplicateScanStore = (*stubLibraryStore)(nil)

func (s *stubLibraryStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	if offset >= len(s.artists) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.artists) {
		end = len(s.artists)
	}
	return s.artists[offset:end], nil
}

func (s *stubLibraryStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	for i := range s.artists {
		if s.artists[i].ID == id {
			return &s.artists[i], nil
		}
	}
	return nil, nil
}

func (s *stubLibraryStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	return s.tracks[artistID], nil
}

func (s *stubLibraryStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	return s.albums[id], nil
}

func (s *stubLibraryStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return s.albumTrks[albumID], nil
}

func (s *stubLibraryStore) SearchTracks(ctx context.Context, query string, offset, limit int) ([]domain.Track, error) {
	return paginateSlice(s.searchTracks, offset, limit), nil
}

func (s *stubLibraryStore) SearchAlbums(ctx context.Context, query string, offset, limit int) ([]domain.Album, error) {
	return paginateSlice(s.searchAlbums, offset, limit), nil
}

func (s *stubLibraryStore) GetAlbumsByArtist(ctx context.Context, artistID int64, offset, limit int) ([]domain.Album, error) {
	return paginateSlice(s.artistAlbums[artistID], offset, limit), nil
}

func (s *stubLibraryStore) MergeArtists(ctx context.Context, keepID, removeID int64) error {
	s.merges = append(s.merges, [2]int64{keepID, removeID})
	return nil
}

func (s *stubLibraryStore) RenameArtist(ctx context.Context, artistID int64, name string) error {
	s.renames = append(s.renames, struct {
		id   int64
		name string
	}{artistID, name})
	return nil
}

func (s *stubLibraryStore) GetDuplicateCanonical(ctx context.Context, groupKey string) (string, bool, error) {
	c, ok := s.scan[groupKey]
	return c, ok, nil
}
func (s *stubLibraryStore) ListDuplicateCanonicals(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range s.scan {
		out[k] = v
	}
	return out, nil
}
func (s *stubLibraryStore) UpsertDuplicateCanonical(ctx context.Context, groupKey, canonical string) error {
	if s.scan == nil {
		s.scan = map[string]string{}
	}
	s.scan[groupKey] = canonical
	return nil
}
func (s *stubLibraryStore) ClearDuplicateCanonicals(ctx context.Context) error {
	s.scan = map[string]string{}
	return nil
}
func (s *stubLibraryStore) DeleteDuplicateCanonical(ctx context.Context, groupKey string) error {
	delete(s.scan, groupKey)
	return nil
}

// stubNameProvider is a metadata provider whose canonical-name lookups come
// from a fixed map (keyed by normalized name), standing in for MusicBrainz.
type stubNameProvider struct {
	name  string // registry name (default "stubmb")
	names map[string]string
	err   error        // optional: return this error from CanonicalArtistName
	fails int          // return err for the first `fails` calls, then succeed
	left  atomic.Int32 // remaining failures (runtime)
}

var _ metadata.Provider = (*stubNameProvider)(nil)
var _ metadata.ArtistNameProvider = (*stubNameProvider)(nil)

func (p *stubNameProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "stubmb"
}
func (p *stubNameProvider) DisplayName() string {
	if p.name != "" {
		return p.name
	}
	return "stubmb"
}
func (p *stubNameProvider) IsConfigured() bool        { return true }
func (p *stubNameProvider) IsMetadataAvailable() bool { return true }
func (p *stubNameProvider) CapabilityStatus() map[string]string {
	return map[string]string{"metadata": "connected"}
}
func (p *stubNameProvider) CheckConnection(_ context.Context) error { return nil }
func (p *stubNameProvider) Connected() bool                         { return true }
func (p *stubNameProvider) SearchCover(_ context.Context, _, _ string) (*metadata.CoverResult, error) {
	return nil, nil
}
func (p *stubNameProvider) SearchArtistImage(_ context.Context, _ string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}
func (p *stubNameProvider) SearchAlbum(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (p *stubNameProvider) EnrichTrack(_ context.Context, _ *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}
func (p *stubNameProvider) CanonicalArtistName(_ context.Context, name string) (string, error) {
	if p.err != nil {
		// First `fails` calls return the error, then succeed (simulates a
		// transient rate limit that recovers).
		if p.left.Load() < int32(p.fails) {
			p.left.Add(1)
			return "", p.err
		}
	}
	return p.names[normalizeKey(name)], nil
}

func TestArtistThumbURLTransform(t *testing.T) {
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Local Singular", ThumbURL: "artist.jpg"},
			{ID: 2, Name: "Local Plural", ThumbURL: "artists.jpg"},
			{ID: 3, Name: "Remote", ThumbURL: "https://example.com/a.jpg"},
			{ID: 4, Name: "Various Artists", ThumbURL: "artist.jpg"},
		},
	}
	s := &Server{cfg: cfg, store: store, log: logger}

	t.Run("list transforms local thumbs, leaves remote", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/artists", nil)
		rec := httptest.NewRecorder()
		s.handleLibraryArtists(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		var got []domain.Artist
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		want := map[int64]string{
			1: "/api/artist-image/1",
			2: "/api/artist-image/2",
			3: "https://example.com/a.jpg",
			4: "", // compilation groupings show the placeholder avatar
		}
		for _, a := range got {
			if a.ThumbURL != want[a.ID] {
				t.Errorf("artist %d thumb_url = %q, want %q", a.ID, a.ThumbURL, want[a.ID])
			}
		}
	})

	t.Run("detail transforms local plural thumb", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/artists/2", nil)
		req.SetPathValue("artistID", "2")
		rec := httptest.NewRecorder()
		s.handleLibraryArtist(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		var got domain.Artist
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if got.ThumbURL != "/api/artist-image/2" {
			t.Errorf("artist 2 thumb_url = %q, want /api/artist-image/2", got.ThumbURL)
		}
	})

	t.Run("detail blanks compilation grouping portrait", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/artists/4", nil)
		req.SetPathValue("artistID", "4")
		rec := httptest.NewRecorder()
		s.handleLibraryArtist(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		var got domain.Artist
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if got.ThumbURL != "" {
			t.Errorf("Various Artists thumb_url = %q, want empty (placeholder)", got.ThumbURL)
		}
	})
}

func TestArtistDuplicatesAndMerge(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en de Munnik"},
			{ID: 2, Name: "Acda en De Munnik"},
			{ID: 3, Name: "Aaliyah"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
		},
		scan: map[string]string{"acda en de munnik": "Acda en de Munnik"},
	}
	s := &Server{store: store, log: testAPILogger()}

	// Duplicates listing: the scanned case-variant group is shown, canonical
	// first. Unscanned duplicates ("Aaliyah" has none here) are hidden.
	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 1 {
		t.Fatalf("expected 1 duplicate group, got %d", len(body.Groups))
	}
	g := body.Groups[0]
	if len(g.Artists) != 2 {
		t.Fatalf("expected 2 artists in group, got %d", len(g.Artists))
	}
	if g.Artists[0].ID != 1 || g.Artists[0].Tracks != 2 {
		t.Errorf("first artist should be the canonical-matching, largest one (%+v)", g.Artists[0])
	}

	// Merge request folds artist 2 into artist 1.
	req = httptest.NewRequest(http.MethodPost, "/api/library/artists/1/merge", strings.NewReader(`{"remove_id":2}`))
	req.SetPathValue("artistID", "1")
	rec = httptest.NewRecorder()
	s.handleLibraryArtistMerge(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.merges) != 1 || store.merges[0] != [2]int64{1, 2} {
		t.Errorf("merge not forwarded to store: %v", store.merges)
	}
	// The merge invalidates the scanned group.
	if _, stillScanned := store.scan["acda en de munnik"]; stillScanned {
		t.Errorf("duplicate scan entry not invalidated after merge")
	}
}

func TestArtistDuplicatesListingFromScan(t *testing.T) {
	// The larger entry is misspelled; the canonical spelling comes from the
	// persisted scan, so the correctly-cased entry is the suggestion even with
	// fewer tracks. Groups not covered by a scan are hidden.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
			{ID: 3, Name: "Danny De Munk"},
			{ID: 4, Name: "Danny de Munk"},
			{ID: 5, Name: "Scanned But No Variant"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 11}, {ID: 12}, {ID: 13}, {ID: 14}, {ID: 15}},
			2: {{ID: 21}},
			3: {{ID: 31}, {ID: 32}, {ID: 33}},
			4: {{ID: 41}},
		},
		scan: map[string]string{
			"acda en de munnik": "Acda en de Munnik",
			"danny de munk":     "Danny de Munk",
		},
	}
	s := &Server{store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 2 {
		t.Fatalf("expected 2 scanned duplicate groups, got %d", len(body.Groups))
	}
	// Explicit keeper checks: correct casing wins even with fewer tracks.
	want := map[string]int64{
		"acda en de munnik": 2,
		"danny de munk":     4,
	}
	for _, g := range body.Groups {
		if len(g.Artists) != 2 {
			t.Fatalf("expected 2 artists in group %q, got %d", g.Name, len(g.Artists))
		}
		w, ok := want[g.Name]
		if !ok {
			t.Fatalf("unexpected group %q", g.Name)
		}
		if g.Artists[0].ID != w {
			t.Errorf("suggestion for %q = id %d (%s), want id %d", g.Name, g.Artists[0].ID, g.Artists[0].Name, w)
		}
	}
}

func TestArtistDuplicatesHidesUnscannedGroups(t *testing.T) {
	// Two case-variant groups exist, but only one was scanned — the other must
	// not appear until the next scan.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Danny De Munk"},
			{ID: 2, Name: "Danny de Munk"},
			{ID: 3, Name: "Acda en De Munnik"},
			{ID: 4, Name: "Acda en de Munnik"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 11}, {ID: 12}, {ID: 13}},
			2: {{ID: 21}},
			3: {{ID: 31}, {ID: 32}},
			4: {{ID: 41}},
		},
		scan: map[string]string{
			"danny de munk": "Danny de Munk",
			// "acda en de munnik" not scanned → must be hidden
		},
	}
	s := &Server{store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 1 || body.Groups[0].Name != "danny de munk" {
		t.Errorf("expected only the scanned group, got %+v", body.Groups)
	}
}

func TestArtistMergeRenamesToCanonical(t *testing.T) {
	// Keeper (id 1) is the misspelled, larger variant. The scan cache holds the
	// canonical spelling, so the merge must rename the survivor to it and
	// invalidate the group.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}, {ID: 12}},
			2: {{ID: 13}},
		},
		scan: map[string]string{"acda en de munnik": "Acda en de Munnik"},
	}
	s := &Server{store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/library/artists/1/merge", strings.NewReader(`{"remove_id":2}`))
	req.SetPathValue("artistID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryArtistMerge(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.merges) != 1 || store.merges[0] != [2]int64{1, 2} {
		t.Fatalf("merge not forwarded to store: %v", store.merges)
	}
	if len(store.renames) != 1 || store.renames[0].id != 1 || store.renames[0].name != "Acda en de Munnik" {
		t.Errorf("survivor not renamed to canonical spelling: %+v", store.renames)
	}
	if _, stillScanned := store.scan["acda en de munnik"]; stillScanned {
		t.Errorf("duplicate scan entry not invalidated after merge")
	}
	var resp struct {
		Merged  bool   `json:"merged"`
		Renamed bool   `json:"renamed"`
		Canon   string `json:"canonical_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if !resp.Merged || !resp.Renamed || resp.Canon != "Acda en de Munnik" {
		t.Errorf("response = %+v, want merged+renamed true with canonical name", resp)
	}
}

func TestArtistImageServesFromArtistDir(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	// Flat layout: artist folder holds tracks directly, with its own portrait.
	write("Flat Artist/01 - Track.mp3", "audio")
	write("Flat Artist/artist.jpg", "flat-image")
	// Nested layout.
	write("Nested Artist/Album/01 - Track.flac", "audio")
	write("Nested Artist/artist.jpg", "nested-image")
	// Compilation grouping: shared folder with a portrait that must not leak.
	write("Various Artists/Album/01 - 2Pac.flac", "audio")
	write("Various Artists/artist.jpg", "va-image")
	// Mismatched folder name (DB artist "AC/DC", folder "AC DC"): still the
	// track's real home and not a compilation — its portrait must serve.
	write("AC DC/Highway to Hell/01 - Highway to Hell.flac", "audio")
	write("AC DC/artist.jpg", "acdc-image")

	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Flat Artist"},
			{ID: 2, Name: "Nested Artist"},
			{ID: 3, Name: "2Pac"},
			{ID: 4, Name: "AC/DC"},
		},
		tracks: map[int64][]domain.Track{
			1: {{FilePath: filepath.Join(root, "Flat Artist", "01 - Track.mp3")}},
			2: {{FilePath: filepath.Join(root, "Nested Artist", "Album", "01 - Track.flac")}},
			3: {{FilePath: filepath.Join(root, "Various Artists", "Album", "01 - 2Pac.flac")}},
			4: {{FilePath: filepath.Join(root, "AC DC", "Highway to Hell", "01 - Highway to Hell.flac")}},
		},
	}
	s := &Server{cfg: cfg, store: store, log: logger}

	tests := []struct {
		name     string
		artistID string
		wantCode int
		wantBody string
	}{
		{"flat artist serves own folder image", "1", http.StatusOK, "flat-image"},
		{"nested artist serves own folder image", "2", http.StatusOK, "nested-image"},
		{"compilation artist must not leak grouping portrait", "3", http.StatusNotFound, ""},
		{"mismatched folder name still serves its own image", "4", http.StatusOK, "acdc-image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/artist-image/"+tt.artistID, nil)
			req.SetPathValue("artistID", tt.artistID)
			rec := httptest.NewRecorder()
			s.handleArtistImage(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" {
				if body := rec.Body.String(); body != tt.wantBody {
					t.Errorf("body = %q, want %q", body, tt.wantBody)
				}
			}
		})
	}
}

// ─── Manual album refresh endpoint ───────────────────────────────────

// stubAlbumRefresher records every EnqueueAlbum call so a test can assert the
// handler forwarded exactly the right album id, reason, and priority. It is a
// stand-in for the refresh engine (*refresh.Service), which the handler reaches
// only through the narrow albumRefresher interface.
type stubAlbumRefresher struct {
	calls     []refreshEnqueue
	returnErr error
}

type refreshEnqueue struct {
	albumID  int64
	reason   string
	priority int
}

var _ albumRefresher = (*stubAlbumRefresher)(nil)

func (r *stubAlbumRefresher) EnqueueAlbum(_ context.Context, albumID int64, reason string, priority int) error {
	r.calls = append(r.calls, refreshEnqueue{albumID: albumID, reason: reason, priority: priority})
	return r.returnErr
}

// TestHandleLibraryAlbumRefreshEnqueuesHighPriority covers the happy path: the
// endpoint returns 202 and forwards exactly one high-priority manual enqueue for
// the path's album id.
func TestHandleLibraryAlbumRefreshEnqueuesHighPriority(t *testing.T) {
	ref := &stubAlbumRefresher{}
	s := &Server{albumRefresh: ref, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/42/refresh", nil)
	req.SetPathValue("albumID", "42")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumRefresh(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	if len(ref.calls) != 1 {
		t.Fatalf("enqueuer called %d times, want exactly 1", len(ref.calls))
	}
	if got := ref.calls[0]; got.albumID != 42 || got.reason != catalog.ReasonManual || got.priority != catalog.PriorityHigh {
		t.Errorf("enqueue = %+v, want {albumID:42 reason:%q priority:%d}",
			got, catalog.ReasonManual, catalog.PriorityHigh)
	}
	var body map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad response JSON: %v (%s)", err, rec.Body.String())
	}
	if !body["queued"] {
		t.Errorf("body = %v, want queued:true", body)
	}
}

// TestHandleLibraryAlbumRefreshInvalidID covers boundary validation: a
// non-numeric album id is a 400 and the enqueuer is never touched.
func TestHandleLibraryAlbumRefreshInvalidID(t *testing.T) {
	ref := &stubAlbumRefresher{}
	s := &Server{albumRefresh: ref, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/not-a-number/refresh", nil)
	req.SetPathValue("albumID", "not-a-number")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumRefresh(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(ref.calls) != 0 {
		t.Errorf("enqueuer must not be called on an invalid id, got %+v", ref.calls)
	}
}

// TestHandleLibraryAlbumRefreshPropagatesServiceError proves the handler is thin
// and synchronous: it calls the service inline and surfaces its error as a 500.
// A handler that fired the enqueue in a goroutine could not observe or report
// this error, so a 500 here is evidence of the no-goroutine contract.
func TestHandleLibraryAlbumRefreshPropagatesServiceError(t *testing.T) {
	ref := &stubAlbumRefresher{returnErr: errors.New("queue write failed")}
	s := &Server{albumRefresh: ref, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/7/refresh", nil)
	req.SetPathValue("albumID", "7")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumRefresh(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	if len(ref.calls) != 1 {
		t.Errorf("enqueuer called %d times, want 1", len(ref.calls))
	}
}

// TestHandleLibraryAlbumRefreshNoService covers the defensive guard: with no
// engine wired the endpoint reports 503 instead of panicking.
func TestHandleLibraryAlbumRefreshNoService(t *testing.T) {
	s := &Server{log: testAPILogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/1/refresh", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumRefresh(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestRouteRegistrationAlbumRefresh proves the route is registered on the API
// router and dispatched to the handler with {albumID} extracted — the
// end-to-end router wiring, not just the handler in isolation. The real route
// table needs the rate limiter production always wires.
func TestRouteRegistrationAlbumRefresh(t *testing.T) {
	ref := &stubAlbumRefresher{}
	s := &Server{
		store:        &stubLibraryStore{albums: map[int64]*domain.Album{7: {ID: 7, ArtistID: 1}}},
		albumRefresh: ref,
		log:          testAPILogger(),
	}
	limiter := newIPRateLimiter(roleTestBuckets(), testAPILogger())
	t.Cleanup(limiter.Shutdown)
	s.rateLimiter = limiter
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/7/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	if len(ref.calls) != 1 || ref.calls[0].albumID != 7 {
		t.Errorf("route did not forward to the refresher: %+v", ref.calls)
	}
}

// TestHandleLibraryAlbumRefreshUnknownAlbum covers the R2-HIGH validation: a
// refresh for an id that does not resolve to a local album is a 404 and the
// enqueuer is never touched (a queue row for an unknown album would only churn).
func TestHandleLibraryAlbumRefreshUnknownAlbum(t *testing.T) {
	ref := &stubAlbumRefresher{}
	s := &Server{
		store:        &stubLibraryStore{albums: map[int64]*domain.Album{1: {ID: 1}}},
		albumRefresh: ref,
		log:          testAPILogger(),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/999/refresh", nil)
	req.SetPathValue("albumID", "999")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumRefresh(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if len(ref.calls) != 0 {
		t.Errorf("enqueuer must not be called for an unknown album, got %+v", ref.calls)
	}
}

// TestHandleLibraryAlbumRefreshKnownAlbum covers the happy path through the
// existence validation: a known album is enqueued and returns 202.
func TestHandleLibraryAlbumRefreshKnownAlbum(t *testing.T) {
	ref := &stubAlbumRefresher{}
	s := &Server{
		store:        &stubLibraryStore{albums: map[int64]*domain.Album{5: {ID: 5}}},
		albumRefresh: ref,
		log:          testAPILogger(),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/5/refresh", nil)
	req.SetPathValue("albumID", "5")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumRefresh(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	if len(ref.calls) != 1 || ref.calls[0].albumID != 5 {
		t.Errorf("known album not enqueued: %+v", ref.calls)
	}
}

// TestAlbumRefreshRouteIsRateLimited proves the refresh route is wrapped in the
// shared download bucket: with a max of one, a second request in the window is a
// 429 carrying the bucket header. An unwrapped route would answer 202 both times.
func TestAlbumRefreshRouteIsRateLimited(t *testing.T) {
	ref := &stubAlbumRefresher{}
	s := &Server{
		store:        &stubLibraryStore{albums: map[int64]*domain.Album{7: {ID: 7}}},
		albumRefresh: ref,
		log:          testAPILogger(),
	}
	limiter := newIPRateLimiter(testBuckets(1), testAPILogger())
	t.Cleanup(limiter.Shutdown)
	s.rateLimiter = limiter

	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/library/albums/7/refresh", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(); rec.Code != http.StatusAccepted {
		t.Fatalf("first refresh = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	rec := post()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second refresh = %d, want 429 (refresh route must share the download bucket)", rec.Code)
	}
	if got := rec.Header().Get("X-RateLimit-Bucket"); got != "download" {
		t.Errorf("X-RateLimit-Bucket = %q, want download", got)
	}
}

// ─── Album discovery read path (catalog-backed) ──────────────────────

// stubCatalogStore embeds the catalog read interfaces and overrides only the
// two methods the album read path calls. Any other catalog method panics,
// proving the handler touches nothing beyond the materialized tracklist and the
// album provider link.
type stubCatalogStore struct {
	catalog.AlbumTrackStore
	catalog.AlbumMetadataStore
	tracks   []catalog.AlbumTrack
	metadata *catalog.AlbumMetadata
}

func (c *stubCatalogStore) GetAlbumTracks(context.Context, int64) ([]catalog.AlbumTrack, error) {
	return c.tracks, nil
}

func (c *stubCatalogStore) GetAlbumMetadata(context.Context, int64) (*catalog.AlbumMetadata, error) {
	return c.metadata, nil
}

var _ catalogReader = (*stubCatalogStore)(nil)

// failingDiscoverProvider fails the test if any discovery method is invoked. It
// is registered in the discovery registry so a passing test proves the album
// read path made zero provider calls.
type failingDiscoverProvider struct {
	t    *testing.T
	name string
}

func (p *failingDiscoverProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "failing"
}
func (p *failingDiscoverProvider) DisplayName() string { return p.Name() }
func (p *failingDiscoverProvider) IsConfigured() bool  { return true }
func (p *failingDiscoverProvider) CheckConnection(context.Context) error {
	return nil
}
func (p *failingDiscoverProvider) Connected() bool { return true }
func (p *failingDiscoverProvider) CapabilityStatus() map[string]string {
	return map[string]string{"discovery": "connected"}
}
func (p *failingDiscoverProvider) fail(method string) {
	p.t.Errorf("discovery provider %q called %s; the album read path must not call providers", p.Name(), method)
}
func (p *failingDiscoverProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	p.fail("SearchArtists")
	return nil, errors.New("provider must not be called")
}
func (p *failingDiscoverProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	p.fail("GetArtistAlbums")
	return nil, errors.New("provider must not be called")
}
func (p *failingDiscoverProvider) SearchAlbums(context.Context, discovery.AlbumQuery, int) ([]discovery.AlbumResult, error) {
	p.fail("SearchAlbums")
	return nil, errors.New("provider must not be called")
}
func (p *failingDiscoverProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	p.fail("GetAlbumTracks")
	return nil, errors.New("provider must not be called")
}

var _ discovery.Provider = (*failingDiscoverProvider)(nil)

// newAlbumDiscoveryServer wires a Server whose discovery registry contains a
// provider that fails the test if touched.
func newAlbumDiscoveryServer(t *testing.T, store library.Store, cat catalogReader, reg *discovery.Registry) *Server {
	t.Helper()
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: cfg, store: store, catalog: cat, discoveryReg: reg, log: testAPILogger()}
}

func decodeAlbumDiscovery(t *testing.T, rec *httptest.ResponseRecorder) albumDiscoveryResponse {
	t.Helper()
	var resp albumDiscoveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v (%s)", err, rec.Body.String())
	}
	return resp
}

// TestHandleLibraryAlbumDiscoveryWarmMergesCatalogAndLocal covers the warm hit:
// materialized rows are merged with live local tracks and the provider is never
// called. The registered provider fails the test if invoked.
func TestHandleLibraryAlbumDiscoveryWarmMergesCatalogAndLocal(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
		albumTrks: map[int64][]domain.Track{
			1: {{
				ID: 10, Title: "T1", TrackNumber: 1, Duration: 1000,
				FilePath: "/music/Artist/Album/01 - T1.mp3", FileSize: 111, Bitrate: 320,
			}},
		},
	}
	cat := &stubCatalogStore{
		tracks: []catalog.AlbumTrack{
			{DiscNumber: 1, TrackNumber: 1, Title: "T1", DurationMs: 1000},
			{DiscNumber: 1, TrackNumber: 2, Title: "T2", DurationMs: 2000},
		},
		metadata: &catalog.AlbumMetadata{AlbumID: 1, ProviderName: "deezer", ProviderAlbumID: "dz-1"},
	}
	s := newAlbumDiscoveryServer(t, store, cat, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	resp := decodeAlbumDiscovery(t, rec)
	if resp.MetadataPending {
		t.Errorf("warm read must not be metadata_pending")
	}
	if resp.Provider != "deezer" || resp.ProviderAlbumID != "dz-1" {
		t.Errorf("provider link = (%q,%q), want (deezer, dz-1)", resp.Provider, resp.ProviderAlbumID)
	}
	if len(resp.Tracks) != 2 {
		t.Fatalf("expected 2 merged tracks, got %d: %+v", len(resp.Tracks), resp.Tracks)
	}
	if !resp.Tracks[0].Downloaded || resp.Tracks[0].LibraryTrackID != 10 {
		t.Errorf("track 1 not matched to its local file: %+v", resp.Tracks[0])
	}
	if resp.Tracks[0].FilePath != "/music/Artist/Album/01 - T1.mp3" || resp.Tracks[0].Format != "MP3" {
		t.Errorf("track 1 local metadata = %+v", resp.Tracks[0])
	}
	if resp.Tracks[1].Downloaded || resp.Tracks[1].LibraryTrackID != 0 {
		t.Errorf("track 2 should be undownloaded: %+v", resp.Tracks[1])
	}
}

// TestHandleLibraryAlbumDiscoveryColdReturnsLocalPending covers the cold miss:
// no materialized rows → local tracks with metadata_pending true, no provider
// call.
func TestHandleLibraryAlbumDiscoveryColdReturnsLocalPending(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
		albumTrks: map[int64][]domain.Track{
			1: {
				{ID: 10, Title: "L1", TrackNumber: 1, Duration: 1000},
				{ID: 11, Title: "L2", TrackNumber: 2, Duration: 2000},
			},
		},
	}
	s := newAlbumDiscoveryServer(t, store, &stubCatalogStore{}, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeAlbumDiscovery(t, rec)
	if !resp.MetadataPending {
		t.Errorf("cold read must be metadata_pending")
	}
	if len(resp.Tracks) != 2 {
		t.Fatalf("cold read should return 2 local tracks, got %d", len(resp.Tracks))
	}
	for _, tr := range resp.Tracks {
		if !tr.Downloaded {
			t.Errorf("cold-read local track must be downloaded: %+v", tr)
		}
	}
}

// TestHandleLibraryAlbumDiscoveryEmptyNoPanic covers empty local + empty
// materialized: empty tracks, no panic, and an unchanged JSON shape.
func TestHandleLibraryAlbumDiscoveryEmptyNoPanic(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
	}
	s := newAlbumDiscoveryServer(t, store, &stubCatalogStore{}, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeAlbumDiscovery(t, rec)
	if !resp.MetadataPending || len(resp.Tracks) != 0 {
		t.Errorf("empty album = pending=%v tracks=%d, want pending true / 0 tracks", resp.MetadataPending, len(resp.Tracks))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	for _, key := range []string{"provider", "provider_album_id", "tracks", "metadata_pending"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("response missing key %q", key)
		}
	}
	// tracks must serialize as [] (not null) so the UI can iterate it.
	if string(raw["tracks"]) != "[]" {
		t.Errorf("tracks JSON = %s, want []", raw["tracks"])
	}
}

// TestGetLibraryAlbumDiscoveryDataReadsCatalogOnly verifies the download-missing
// helper: warm = merged expected list, cold = nothing to diff, neither touches a
// provider.
func TestGetLibraryAlbumDiscoveryDataReadsCatalogOnly(t *testing.T) {
	store := &stubLibraryStore{
		albumTrks: map[int64][]domain.Track{1: {{ID: 10, Title: "T1", TrackNumber: 1}}},
	}
	reg := newDiscoverRegistry(t, &failingDiscoverProvider{t: t})
	cat := &stubCatalogStore{tracks: []catalog.AlbumTrack{
		{DiscNumber: 1, TrackNumber: 1, Title: "T1"},
		{DiscNumber: 1, TrackNumber: 2, Title: "T2"},
	}}
	s := newAlbumDiscoveryServer(t, store, cat, reg)

	got, pending, err := getLibraryAlbumDiscoveryData(context.Background(), s, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pending {
		t.Fatalf("warm read must not be pending")
	}
	if len(got) != 2 || !got[0].Downloaded || got[1].Downloaded {
		t.Fatalf("expected T1 downloaded and T2 missing, got %+v", got)
	}

	// Cold: no materialized rows → pending with no data, and still no provider call.
	cold := newAlbumDiscoveryServer(t, store, &stubCatalogStore{}, reg)
	got, pending, err = getLibraryAlbumDiscoveryData(context.Background(), cold, 1, 2)
	if err != nil {
		t.Fatalf("cold unexpected error: %v", err)
	}
	if !pending {
		t.Fatalf("cold should report pending")
	}
	if got != nil {
		t.Fatalf("cold should return nil data, got %+v", got)
	}
}

// stubColdFetcher records FetchMatched calls and can simulate a provider match.
// onFetch runs after the call is recorded, letting a test materialize rows into
// the stub catalog so the handler's re-read observes them.
type stubColdFetcher struct {
	calls   int
	matched bool
	err     error
	onFetch func()
}

func (f *stubColdFetcher) FetchMatched(context.Context, int64) (bool, error) {
	f.calls++
	if f.onFetch != nil {
		f.onFetch()
	}
	return f.matched, f.err
}

var _ albumColdFetcher = (*stubColdFetcher)(nil)

// TestHandleLibraryAlbumDiscoveryUnmatchedDoesNotColdFetch covers H1/R1: an
// album parked terminal (unmatched) inside its long retry window is served from
// local tracks with metadata_pending. The cold fetcher (the only path to a
// provider) is never called, AND the refresh enqueuer is never called — the
// parked queue row already carries the correct 90-day due_at, so re-enqueueing
// would overwrite it with now.
func TestHandleLibraryAlbumDiscoveryUnmatchedDoesNotColdFetch(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
		albumTrks: map[int64][]domain.Track{
			1: {{ID: 10, Title: "L1", TrackNumber: 1, Duration: 1000}},
		},
	}
	cat := &stubCatalogStore{metadata: &catalog.AlbumMetadata{
		AlbumID: 1, RefreshStatus: catalog.StatusUnmatched, UpdatedAt: time.Now(),
	}}
	fetcher := &stubColdFetcher{matched: true}
	ref := &stubAlbumRefresher{}
	s := newAlbumDiscoveryServer(t, store, cat, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
	s.albumColdFetch = fetcher
	s.albumRefresh = ref

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if fetcher.calls != 0 {
		t.Errorf("cold fetcher called %d times for an unmatched album, want 0", fetcher.calls)
	}
	resp := decodeAlbumDiscovery(t, rec)
	if !resp.MetadataPending {
		t.Errorf("unmatched album must be metadata_pending")
	}
	if len(resp.Tracks) != 1 || !resp.Tracks[0].Downloaded || resp.Tracks[0].LibraryTrackID != 10 {
		t.Errorf("unmatched album should serve its local track: %+v", resp.Tracks)
	}
	if len(ref.calls) != 0 {
		t.Errorf("terminal-unelapsed album must not be re-enqueued (would overwrite the parked due_at), got %+v", ref.calls)
	}
}

// TestHandleLibraryAlbumDiscoveryTerminalUnelapsedDoesNotEnqueue is the R1
// regression: for both terminal statuses (unmatched and failed) inside the
// retry window, opening the album enqueues nothing, while a pending
// (never-materialized) album still enqueues.
func TestHandleLibraryAlbumDiscoveryTerminalUnelapsedDoesNotEnqueue(t *testing.T) {
	store := &stubLibraryStore{
		albums:    map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists:   []domain.Artist{{ID: 2, Name: "Artist"}},
		albumTrks: map[int64][]domain.Track{1: {{ID: 10, Title: "L1", TrackNumber: 1}}},
	}
	cases := []struct {
		name string
		meta *catalog.AlbumMetadata
	}{
		{
			name: "unmatched inside retry window",
			meta: &catalog.AlbumMetadata{AlbumID: 1, RefreshStatus: catalog.StatusUnmatched, UpdatedAt: time.Now()},
		},
		{
			name: "failed inside retry window",
			meta: &catalog.AlbumMetadata{AlbumID: 1, RefreshStatus: catalog.StatusFailed, UpdatedAt: time.Now()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := &stubCatalogStore{metadata: tc.meta}
			// No cold fetcher wired: the pending branch is the fallback, which
			// isolates the enqueue decision from cold-fetch behavior.
			ref := &stubAlbumRefresher{}
			s := newAlbumDiscoveryServer(t, store, cat, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
			s.albumRefresh = ref

			req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
			req.SetPathValue("albumID", "1")
			rec := httptest.NewRecorder()
			s.handleLibraryAlbumDiscovery(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			resp := decodeAlbumDiscovery(t, rec)
			if !resp.MetadataPending {
				t.Errorf("terminal-unelapsed album must be metadata_pending")
			}
			if len(ref.calls) != 0 {
				t.Errorf("terminal-unelapsed album must not be re-enqueued, got %+v", ref.calls)
			}
		})
	}
}

// TestHandleLibraryAlbumDownloadMissingTerminalUnelapsedDoesNotEnqueue is the
// R1 regression for the download-missing pending path: an album parked terminal
// inside its retry window returns the distinguishable 202 but queues nothing,
// while a genuinely cold album still queues a refresh.
func TestHandleLibraryAlbumDownloadMissingTerminalUnelapsedDoesNotEnqueue(t *testing.T) {
	cases := []struct {
		name        string
		meta        *catalog.AlbumMetadata
		wantEnqueue int
	}{
		{
			name:        "terminal inside retry window does not enqueue",
			meta:        &catalog.AlbumMetadata{AlbumID: 1, RefreshStatus: catalog.StatusUnmatched, UpdatedAt: time.Now()},
			wantEnqueue: 0,
		},
		{
			name:        "cold album still enqueues",
			meta:        nil,
			wantEnqueue: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubLibraryStore{
				albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
				artists: []domain.Artist{{ID: 2, Name: "Artist"}},
			}
			ref := &stubAlbumRefresher{}
			s := newAlbumDiscoveryServer(t, store, &stubCatalogStore{metadata: tc.meta},
				newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
			s.albumRefresh = ref

			req := httptest.NewRequest(http.MethodPost, "/api/library/albums/1/download-missing", nil)
			req.SetPathValue("albumID", "1")
			rec := httptest.NewRecorder()
			s.handleLibraryAlbumDownloadMissing(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
			}
			if len(ref.calls) != tc.wantEnqueue {
				t.Errorf("enqueue calls = %d (%+v), want %d", len(ref.calls), ref.calls, tc.wantEnqueue)
			}
		})
	}
}

// TestHandleLibraryAlbumDiscoveryMatchedEmptyDoesNotColdFetch covers H1 for a
// fresh album with zero materialized rows (matched-empty): it is warm, served as
// an empty tracklist with metadata_pending=false, and never re-fetched.
func TestHandleLibraryAlbumDiscoveryMatchedEmptyDoesNotColdFetch(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
		albumTrks: map[int64][]domain.Track{
			1: {{ID: 10, Title: "L1", TrackNumber: 1}},
		},
	}
	cat := &stubCatalogStore{metadata: &catalog.AlbumMetadata{
		AlbumID: 1, ProviderName: "deezer", ProviderAlbumID: "dz-1",
		RefreshStatus: catalog.StatusFresh, LastRefreshedAt: time.Now(),
	}}
	fetcher := &stubColdFetcher{matched: true}
	s := newAlbumDiscoveryServer(t, store, cat, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
	s.albumColdFetch = fetcher

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if fetcher.calls != 0 {
		t.Errorf("cold fetcher called %d times for a matched-empty album, want 0", fetcher.calls)
	}
	resp := decodeAlbumDiscovery(t, rec)
	if resp.MetadataPending {
		t.Errorf("matched-empty album must not be metadata_pending")
	}
	if len(resp.Tracks) != 0 {
		t.Errorf("matched-empty album should serve 0 materialized rows, got %+v", resp.Tracks)
	}
}

// TestHandleLibraryAlbumDiscoveryPendingColdFetches covers H1: an album with no
// metadata row is cold and still triggers the one-time Option-A fetch.
func TestHandleLibraryAlbumDiscoveryPendingColdFetches(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
	}
	fetcher := &stubColdFetcher{matched: false}
	s := newAlbumDiscoveryServer(t, store, &stubCatalogStore{}, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
	s.albumColdFetch = fetcher

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if fetcher.calls != 1 {
		t.Fatalf("cold fetcher called %d times for a pending album, want 1", fetcher.calls)
	}
	if resp := decodeAlbumDiscovery(t, rec); !resp.MetadataPending {
		t.Errorf("pending album with no match must be metadata_pending")
	}
}

// TestHandleLibraryAlbumDiscoveryColdFetchMaterializes covers F5: a wired cold
// fetcher that reports a match forces the handler to re-read the catalog and
// return the freshly materialized + merged tracks with metadata_pending=false.
func TestHandleLibraryAlbumDiscoveryColdFetchMaterializes(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
		albumTrks: map[int64][]domain.Track{
			1: {{ID: 10, Title: "T1", TrackNumber: 1, Duration: 1000,
				FilePath: "/music/Artist/Album/01 - T1.mp3", FileSize: 111, Bitrate: 320}},
		},
	}
	cat := &stubCatalogStore{} // empty until the cold fetch runs
	fetcher := &stubColdFetcher{matched: true, onFetch: func() {
		cat.tracks = []catalog.AlbumTrack{
			{DiscNumber: 1, TrackNumber: 1, Title: "T1", DurationMs: 1000},
			{DiscNumber: 1, TrackNumber: 2, Title: "T2", DurationMs: 2000},
		}
		cat.metadata = &catalog.AlbumMetadata{
			AlbumID: 1, ProviderName: "deezer", ProviderAlbumID: "dz-1",
			RefreshStatus: catalog.StatusFresh, LastRefreshedAt: time.Now(),
		}
	}}
	s := newAlbumDiscoveryServer(t, store, cat, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
	s.albumColdFetch = fetcher

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if fetcher.calls != 1 {
		t.Fatalf("cold fetcher called %d times, want 1", fetcher.calls)
	}
	resp := decodeAlbumDiscovery(t, rec)
	if resp.MetadataPending {
		t.Errorf("materialized cold fetch must clear metadata_pending")
	}
	if resp.Provider != "deezer" || resp.ProviderAlbumID != "dz-1" {
		t.Errorf("provider link = (%q,%q), want (deezer, dz-1)", resp.Provider, resp.ProviderAlbumID)
	}
	if len(resp.Tracks) != 2 {
		t.Fatalf("expected 2 merged tracks, got %d: %+v", len(resp.Tracks), resp.Tracks)
	}
	if !resp.Tracks[0].Downloaded || resp.Tracks[0].LibraryTrackID != 10 {
		t.Errorf("track 1 not merged with its local file: %+v", resp.Tracks[0])
	}
	if resp.Tracks[1].Downloaded {
		t.Errorf("track 2 should be undownloaded: %+v", resp.Tracks[1])
	}
}

// TestHandleLibraryAlbumDiscoveryStaleServesAndEnqueues covers F2/Tier-1: a
// materialized album whose metadata is past the soft TTL is served immediately
// from its rows, with a low-priority refresh enqueued behind the response and no
// provider call in the request.
func TestHandleLibraryAlbumDiscoveryStaleServesAndEnqueues(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
	}
	cat := &stubCatalogStore{
		tracks: []catalog.AlbumTrack{{DiscNumber: 1, TrackNumber: 1, Title: "T1"}},
		metadata: &catalog.AlbumMetadata{
			AlbumID: 1, ProviderName: "deezer", ProviderAlbumID: "dz-1",
			RefreshStatus:   catalog.StatusFresh,
			LastRefreshedAt: time.Now().Add(-100 * 24 * time.Hour),
		},
	}
	fetcher := &stubColdFetcher{matched: true}
	ref := &stubAlbumRefresher{}
	s := newAlbumDiscoveryServer(t, store, cat, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
	s.albumColdFetch = fetcher
	s.albumRefresh = ref
	s.albumStaleAfter = func() time.Duration { return 24 * time.Hour }

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDiscovery(rec, req)

	if fetcher.calls != 0 {
		t.Errorf("stale read must not call the provider, fetcher calls = %d", fetcher.calls)
	}
	resp := decodeAlbumDiscovery(t, rec)
	if resp.MetadataPending || len(resp.Tracks) != 1 {
		t.Fatalf("stale read should serve materialized rows immediately: pending=%v tracks=%+v", resp.MetadataPending, resp.Tracks)
	}
	if len(ref.calls) != 1 {
		t.Fatalf("stale read must enqueue exactly one refresh, got %+v", ref.calls)
	}
	if got := ref.calls[0]; got.reason != catalog.ReasonStale || got.priority != catalog.PriorityLow {
		t.Errorf("stale enqueue = %+v, want reason=%q priority=%d", got, catalog.ReasonStale, catalog.PriorityLow)
	}
}

// TestHandleLibraryAlbumDiscoveryMetadataState is the UI-truthfulness
// regression: the discovery response must distinguish a genuinely cold/pending
// album from one parked terminal (unmatched/failed), while keeping the existing
// metadata_pending field and its shape. Terminal rows report
// metadata_pending=true (still unmaterialized) AND metadata_terminal=true with
// the terminal status; a cold/pending row reports metadata_terminal=false; a warm
// (fresh/stale) row reports its warm status and metadata_terminal=false. No
// provider is called on any branch.
func TestHandleLibraryAlbumDiscoveryMetadataState(t *testing.T) {
	const albumID = 1
	cases := []struct {
		name         string
		meta         *catalog.AlbumMetadata
		materialized []catalog.AlbumTrack
		wantPending  bool
		wantTerminal bool
		wantStatus   catalog.RefreshStatus
	}{
		{name: "cold nil row", meta: nil, materialized: nil, wantPending: true, wantTerminal: false, wantStatus: ""},
		{
			name:         "pending row",
			meta:         &catalog.AlbumMetadata{AlbumID: albumID, RefreshStatus: catalog.StatusPending},
			materialized: nil,
			wantPending:  true,
			wantTerminal: false,
			wantStatus:   catalog.StatusPending,
		},
		{
			name:         "terminal unmatched",
			meta:         &catalog.AlbumMetadata{AlbumID: albumID, RefreshStatus: catalog.StatusUnmatched, UpdatedAt: time.Now()},
			materialized: nil,
			wantPending:  true,
			wantTerminal: true,
			wantStatus:   catalog.StatusUnmatched,
		},
		{
			name:         "terminal failed",
			meta:         &catalog.AlbumMetadata{AlbumID: albumID, RefreshStatus: catalog.StatusFailed, UpdatedAt: time.Now()},
			materialized: nil,
			wantPending:  true,
			wantTerminal: true,
			wantStatus:   catalog.StatusFailed,
		},
		{
			name: "warm fresh",
			meta: &catalog.AlbumMetadata{
				AlbumID: albumID, ProviderName: "deezer", ProviderAlbumID: "dz-1",
				RefreshStatus: catalog.StatusFresh, LastRefreshedAt: time.Now(),
			},
			materialized: []catalog.AlbumTrack{{DiscNumber: 1, TrackNumber: 1, Title: "T1"}},
			wantPending:  false,
			wantTerminal: false,
			wantStatus:   catalog.StatusFresh,
		},
		{
			name: "warm stale",
			meta: &catalog.AlbumMetadata{
				AlbumID: albumID, ProviderName: "deezer", ProviderAlbumID: "dz-1",
				RefreshStatus: catalog.StatusStale, LastRefreshedAt: time.Now(),
			},
			materialized: []catalog.AlbumTrack{{DiscNumber: 1, TrackNumber: 1, Title: "T1"}},
			wantPending:  false,
			wantTerminal: false,
			wantStatus:   catalog.StatusStale,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubLibraryStore{
				albums:    map[int64]*domain.Album{albumID: {ID: albumID, ArtistID: 2, Title: "Album"}},
				artists:   []domain.Artist{{ID: 2, Name: "Artist"}},
				albumTrks: map[int64][]domain.Track{albumID: {{ID: 10, Title: "L1", TrackNumber: 1}}},
			}
			cat := &stubCatalogStore{metadata: tc.meta, tracks: tc.materialized}
			// No cold fetcher and no refresher wired: the pending fallback is the
			// path under test, isolating the metadata-state projection from any
			// provider or queue side effect.
			s := newAlbumDiscoveryServer(t, store, cat, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))

			req := httptest.NewRequest(http.MethodGet, "/api/library/albums/1/discovery", nil)
			req.SetPathValue("albumID", "1")
			rec := httptest.NewRecorder()
			s.handleLibraryAlbumDiscovery(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			resp := decodeAlbumDiscovery(t, rec)
			if resp.MetadataPending != tc.wantPending {
				t.Errorf("metadata_pending = %v, want %v", resp.MetadataPending, tc.wantPending)
			}
			if resp.MetadataTerminal != tc.wantTerminal {
				t.Errorf("metadata_terminal = %v, want %v", resp.MetadataTerminal, tc.wantTerminal)
			}
			if resp.MetadataStatus != tc.wantStatus {
				t.Errorf("metadata_status = %q, want %q", resp.MetadataStatus, tc.wantStatus)
			}
		})
	}
}

// TestHandleLibraryAlbumDownloadMissingPending covers M8: a download-missing
// request against a cold/pending album returns a distinguishable 202 with
// pending=true (not a silent {"queued":0}) and queues a best-effort refresh.
func TestHandleLibraryAlbumDownloadMissingPending(t *testing.T) {
	store := &stubLibraryStore{
		albums:  map[int64]*domain.Album{1: {ID: 1, ArtistID: 2, Title: "Album"}},
		artists: []domain.Artist{{ID: 2, Name: "Artist"}},
		albumTrks: map[int64][]domain.Track{
			1: {{ID: 10, Title: "L1", TrackNumber: 1}},
		},
	}
	ref := &stubAlbumRefresher{}
	s := newAlbumDiscoveryServer(t, store, &stubCatalogStore{}, newDiscoverRegistry(t, &failingDiscoverProvider{t: t}))
	s.albumRefresh = ref

	req := httptest.NewRequest(http.MethodPost, "/api/library/albums/1/download-missing", nil)
	req.SetPathValue("albumID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDownloadMissing(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad response JSON: %v (%s)", err, rec.Body.String())
	}
	if body["pending"] != true {
		t.Errorf("pending = %v, want true", body["pending"])
	}
	if body["queued"] != float64(0) {
		t.Errorf("queued = %v, want 0", body["queued"])
	}
	if msg, _ := body["message"].(string); msg == "" {
		t.Errorf("pending response must carry a message, got %v", body["message"])
	}
	if len(ref.calls) != 1 || ref.calls[0].reason != catalog.ReasonCold {
		t.Errorf("pending download-missing should enqueue a cold refresh, got %+v", ref.calls)
	}
}

func TestLookupCanonicalArtistFromMusicBrainz(t *testing.T) {
	// The MusicBrainz-style provider supplies the canonical spelling; a
	// discovery-only provider is irrelevant to this path.
	pluginReg := plugin.NewRegistry()
	_ = pluginReg.Register(&stubNameProvider{name: "musicbrainz", names: map[string]string{
		"dannydemunk": "Danny de Munk",
	}})
	s := &Server{
		mdRegistry: metadata.NewRegistryFrom(pluginReg),
		log:        testAPILogger(),
	}
	s.runners = jobs.NewRunners(jobs.RunnerDeps{Log: testAPILogger(), Metadata: s.mdRegistry})

	got, err := s.runners.LookupCanonicalArtist(context.Background(), "Danny De Munk")
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if got != "Danny de Munk" {
		t.Errorf("canonical = %q, want musicbrainz's 'Danny de Munk'", got)
	}
}

func TestLookupCanonicalArtistUnknownReturnsEmpty(t *testing.T) {
	// MusicBrainz has no entry for this artist — the lookup returns empty
	// (no discovery fallback).
	pluginReg := plugin.NewRegistry()
	_ = pluginReg.Register(&stubNameProvider{name: "musicbrainz"}) // returns "" (unknown)
	s := &Server{
		mdRegistry: metadata.NewRegistryFrom(pluginReg),
		log:        testAPILogger(),
	}
	s.runners = jobs.NewRunners(jobs.RunnerDeps{Log: testAPILogger(), Metadata: s.mdRegistry})

	got, err := s.runners.LookupCanonicalArtist(context.Background(), "Danny De Munk")
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if got != "" {
		t.Errorf("canonical = %q, want empty (unknown to MusicBrainz)", got)
	}
}

// TestLookupCanonicalArtistPrefersRateLimit tests that a rate-limit error is
// returned even when a later provider fails with a generic error. The
// duplicates scan pauses-and-retries on ErrRateLimited; if the generic error
// masked it, a throttled MusicBrainz would never get its pause-and-retry.
func TestLookupCanonicalArtistPrefersRateLimit(t *testing.T) {
	pluginReg := plugin.NewRegistry()
	// First provider rate-limits; second fails generically (e.g. timeout).
	_ = pluginReg.Register(&stubNameProvider{
		name:  "musicbrainz",
		err:   metadata.NewRateLimitError("musicbrainz", 0, "HTTP 503"),
		fails: 1,
	})
	_ = pluginReg.Register(&stubNameProvider{
		name:  "other",
		err:   fmt.Errorf("network timeout"),
		fails: 1,
	})
	s := &Server{
		mdRegistry: metadata.NewRegistryFrom(pluginReg),
		log:        testAPILogger(),
	}
	s.runners = jobs.NewRunners(jobs.RunnerDeps{Log: testAPILogger(), Metadata: s.mdRegistry})

	_, err := s.runners.LookupCanonicalArtist(context.Background(), "Danny De Munk")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want rate-limit error to win over the generic failure", err)
	}
}

func TestArtistDuplicatesUnicodeVariantsGrouped(t *testing.T) {
	// The duplicates job persists groups keyed by NormalizeArtistKey; the
	// listing must group by the same key so Tiësto/Tiesto (and other unicode
	// variants) surface together and merge works end to end.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Tiësto"},
			{ID: 2, Name: "Tiesto"},
			{ID: 3, Name: "Ne-Yo"},
			{ID: 4, Name: "Ne‐Yo"}, // U+2010 hyphen
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
			3: {{ID: 13}},
			4: {{ID: 14}},
		},
		scan: map[string]string{
			"tiesto": "Tiësto",
			"ne-yo":  "Ne-Yo",
		},
	}
	s := &Server{store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 2 {
		t.Fatalf("expected 2 unicode-variant groups, got %d: %+v", len(body.Groups), body.Groups)
	}
	for _, g := range body.Groups {
		if len(g.Artists) != 2 {
			t.Errorf("group %q should contain both variants, got %+v", g.Name, g.Artists)
		}
		if g.Artists[0].ID != 1 && g.Artists[0].ID != 3 {
			t.Errorf("group %q first artist = id %d (%s), want the canonical-matching one",
				g.Name, g.Artists[0].ID, g.Artists[0].Name)
		}
	}

	// Merge accented keeper: cache lookup uses the same normalized key.
	req = httptest.NewRequest(http.MethodPost, "/api/library/artists/1/merge", strings.NewReader(`{"remove_id":2}`))
	req.SetPathValue("artistID", "1")
	rec = httptest.NewRecorder()
	s.handleLibraryArtistMerge(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if _, stillScanned := store.scan["tiesto"]; stillScanned {
		t.Errorf("duplicate scan entry not invalidated after unicode-group merge")
	}
	// Keeper (id 1) is already the canonical spelling "Tiësto", so no rename.
	if len(store.renames) != 0 {
		t.Errorf("keeper already canonical, unexpected renames = %+v", store.renames)
	}
}

func TestArtistDuplicatesFeatMarkedGroupedWithPrimary(t *testing.T) {
	// The duplicates job writes duplicate_scan keys via
	// NormalizeArtistKey(IdentityArtistName(name)); the listing must use the
	// same key so "2Pac feat. X" groups with "2Pac" and the merge flow heals
	// the pre-guard split discography.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "2Pac"},
			{ID: 2, Name: "2Pac feat. Anthony Hamilton"},
			{ID: 3, Name: "Simon & Garfunkel"}, // real band, no group
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
			3: {{ID: 13}},
		},
		scan: map[string]string{"2pac": "2Pac"},
	}
	s := &Server{store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 1 {
		t.Fatalf("expected 1 feat group, got %d: %+v", len(body.Groups), body.Groups)
	}
	if len(body.Groups[0].Artists) != 2 {
		t.Errorf("feat group should contain 2Pac + the feat row, got %+v", body.Groups[0].Artists)
	}

	// Merge through the same key: merge the feat row into 2Pac.
	req = httptest.NewRequest(http.MethodPost, "/api/library/artists/1/merge", strings.NewReader(`{"remove_id":2}`))
	req.SetPathValue("artistID", "1")
	rec = httptest.NewRecorder()
	s.handleLibraryArtistMerge(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if _, stillScanned := store.scan["2pac"]; stillScanned {
		t.Errorf("duplicate scan entry not invalidated after feat-group merge")
	}
	if len(store.merges) != 1 || store.merges[0] != [2]int64{1, 2} {
		t.Errorf("merge not forwarded to store: %v", store.merges)
	}
}

// TestHandleLibraryTracks_ExposesAddedByAttribution verifies the library list
// handler passes domain.Track attribution straight through to JSON as
// snake_case added_by_user_id/added_by_username for user-added items. System
// items carry zero/blank attribution, which the omitempty tags drop.
func TestHandleLibraryTracks_ExposesAddedByAttribution(t *testing.T) {
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	store := &stubLibraryStore{
		searchTracks: []domain.Track{
			{ID: 1, Title: "User Song", AddedByUserID: 42, AddedByUsername: "alice"},
			{ID: 2, Title: "Scanned Song"},
		},
	}
	s := &Server{cfg: cfg, store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/tracks", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryTracks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(got))
	}
	if got[0]["added_by_user_id"] != float64(42) {
		t.Errorf("added_by_user_id = %v, want 42", got[0]["added_by_user_id"])
	}
	if got[0]["added_by_username"] != "alice" {
		t.Errorf("added_by_username = %v, want alice", got[0]["added_by_username"])
	}
	// System row: zero/blank attribution is omitted, not serialized.
	if _, ok := got[1]["added_by_user_id"]; ok {
		t.Errorf("system track should omit added_by_user_id, got %v", got[1]["added_by_user_id"])
	}
	if _, ok := got[1]["added_by_username"]; ok {
		t.Errorf("system track should omit added_by_username, got %v", got[1]["added_by_username"])
	}
}

// TestHandleLibraryAlbums_HonoursOffset asserts the albums handler threads
// offset/limit straight through to the store: a non-zero offset returns the
// sliced page, not the head of the list, while offset=0 is unchanged.
func TestHandleLibraryAlbums_HonoursOffset(t *testing.T) {
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	store := &stubLibraryStore{
		searchAlbums: []domain.Album{
			{ID: 1, Title: "Alpha"},
			{ID: 2, Title: "Bravo"},
			{ID: 3, Title: "Charlie"},
		},
	}
	s := &Server{cfg: cfg, store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/albums?offset=1&limit=1", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryAlbums(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var page []domain.Album
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(page) != 1 || page[0].Title != "Bravo" {
		t.Fatalf("offset=1 limit=1 = %+v, want [Bravo]", page)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/library/albums?limit=2", nil)
	rec = httptest.NewRecorder()
	s.handleLibraryAlbums(rec, req)
	var head []domain.Album
	if err := json.Unmarshal(rec.Body.Bytes(), &head); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(head) != 2 || head[0].Title != "Alpha" || head[1].Title != "Bravo" {
		t.Fatalf("offset=0 limit=2 = %+v, want [Alpha Bravo]", head)
	}
}

// TestHandleLibraryArtistAlbums_HonoursOffset asserts the artist-albums handler
// now parses and forwards offset/limit instead of ignoring offset entirely.
func TestHandleLibraryArtistAlbums_HonoursOffset(t *testing.T) {
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	store := &stubLibraryStore{
		artistAlbums: map[int64][]domain.Album{
			7: {{ID: 1, Title: "Alpha"}, {ID: 2, Title: "Bravo"}, {ID: 3, Title: "Charlie"}},
		},
	}
	s := &Server{cfg: cfg, store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/7/albums?offset=1&limit=2", nil)
	req.SetPathValue("artistID", "7")
	rec := httptest.NewRecorder()
	s.handleLibraryArtistAlbums(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var page []domain.Album
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(page) != 2 || page[0].Title != "Bravo" || page[1].Title != "Charlie" {
		t.Fatalf("offset=1 limit=2 = %+v, want [Bravo Charlie]", page)
	}
}

// TestHandleLibraryAlbum covers the single-album lookup: 200 with the domain
// album for an existing id, 404 for an unknown id, and 400 for a non-numeric
// id. This endpoint lets a deep link resolve an album that is not on a loaded
// page of the paginated artist-album listing.
func TestHandleLibraryAlbum(t *testing.T) {
	store := &stubLibraryStore{
		albums: map[int64]*domain.Album{
			7: {ID: 7, ArtistID: 3, Title: "Discovery"},
		},
	}
	s := &Server{store: store, log: testAPILogger()}

	t.Run("existing album returns the domain album", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/albums/7", nil)
		req.SetPathValue("albumID", "7")
		rec := httptest.NewRecorder()
		s.handleLibraryAlbum(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var got domain.Album
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if got.ID != 7 || got.ArtistID != 3 || got.Title != "Discovery" {
			t.Fatalf("album = %+v, want {ID:7 ArtistID:3 Title:Discovery}", got)
		}
	})

	t.Run("missing album is 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/albums/999", nil)
		req.SetPathValue("albumID", "999")
		rec := httptest.NewRecorder()
		s.handleLibraryAlbum(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-numeric album id is 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/albums/not-a-number", nil)
		req.SetPathValue("albumID", "not-a-number")
		rec := httptest.NewRecorder()
		s.handleLibraryAlbum(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleLibraryTracks_HonoursOffset asserts the tracks handler forwards
// offset/limit to the store.
func TestHandleLibraryTracks_HonoursOffset(t *testing.T) {
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	store := &stubLibraryStore{
		searchTracks: []domain.Track{
			{ID: 1, Title: "Alpha Track"},
			{ID: 2, Title: "Bravo Track"},
			{ID: 3, Title: "Charlie Track"},
		},
	}
	s := &Server{cfg: cfg, store: store, log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/tracks?offset=2&limit=1", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryTracks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var page []domain.Track
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(page) != 1 || page[0].Title != "Charlie Track" {
		t.Fatalf("offset=2 limit=1 = %+v, want [Charlie Track]", page)
	}
}
