package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
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
}

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
	}
	s := &Server{store: store, log: testAPILogger()}

	// Duplicates listing: the two case-variants group together, largest first.
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
		t.Errorf("canonical artist should be the largest (%+v)", g.Artists[0])
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
