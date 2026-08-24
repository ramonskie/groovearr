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

	"github.com/ramonskie/groovearr/internal/config"
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
	artists []domain.Artist
	tracks  map[int64][]domain.Track
	merges  [][2]int64 // (keep, remove)
	renames []struct {
		id   int64
		name string
	}
	scan map[string]string // duplicate_scan: group_key → canonical
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
func (p *stubNameProvider) SearchAlbum(_ context.Context, _, _ string) string { return "" }
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
