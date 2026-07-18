package library

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library/sqlite"
)

func TestCoverHook(t *testing.T) {
	t.Run("skip when CoverURL is empty", func(t *testing.T) {
		dir := t.TempDir()

		src := filepath.Join(dir, "test.flac")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}

		store := newTestStore(t)

		hook := NewCoverHook(store)
		newPath, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test-1",
			FilePath: src,
			CoverURL: "",
		})
		if err != nil {
			t.Fatal(err)
		}
		if newPath != src {
			t.Errorf("path changed: %q → %q", src, newPath)
		}

		// cover.jpg should not exist in album dir.
		coverPath := filepath.Join(dir, "cover.jpg")
		if _, err := os.Stat(coverPath); !os.IsNotExist(err) {
			t.Error("cover.jpg created when CoverURL was empty")
		}
	})

	t.Run("fetches cover and writes cover.jpg", func(t *testing.T) {
		// Start a test HTTP server serving a fake JPEG.
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("fake-jpeg-data"))
		}))
		defer ts.Close()

		dir := t.TempDir()
		albumDir := filepath.Join(dir, "Daft Punk", "Random Access Memories (2013)")
		if err := os.MkdirAll(albumDir, 0o755); err != nil {
			t.Fatal(err)
		}

		src := filepath.Join(albumDir, "01 - Get Lucky.flac")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}

		// Seed the store with artist + album matching the directory name.
		store := newTestStore(t)
		artistID, err := store.UpsertArtist(context.Background(), &domain.Artist{Name: "Daft Punk"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.UpsertAlbum(context.Background(), &domain.Album{
			ArtistID:   artistID,
			Title:      "Random Access Memories",
			Year:       2013,
			TrackCount: 1,
		})
		if err != nil {
			t.Fatal(err)
		}

		hook := NewCoverHook(store)
		newPath, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test-2",
			FilePath: src,
			CoverURL: ts.URL + "/cover.jpg",
		})
		if err != nil {
			t.Fatal(err)
		}
		if newPath != src {
			t.Errorf("path changed: %q → %q", src, newPath)
		}

		// Verify cover.jpg was written.
		coverPath := filepath.Join(albumDir, "cover.jpg")
		data, err := os.ReadFile(coverPath)
		if err != nil {
			t.Fatalf("cover.jpg not created: %v", err)
		}
		if string(data) != "fake-jpeg-data" {
			t.Errorf("cover.jpg content = %q, want fake-jpeg-data", string(data))
		}

		// Verify album thumb_url was updated.
		albums, err := store.SearchAlbums(context.Background(), "Random Access Memories", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(albums) < 1 {
			t.Fatal("album not found after hook")
		}
		if albums[0].ThumbURL != "cover.jpg" {
			t.Errorf("thumb_url = %q, want cover.jpg", albums[0].ThumbURL)
		}
	})

	t.Run("skips when cover.jpg already exists", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("HTTP request made when cover already cached")
		}))
		defer ts.Close()

		dir := t.TempDir()
		albumDir := filepath.Join(dir, "Test", "Test Album (2024)")
		if err := os.MkdirAll(albumDir, 0o755); err != nil {
			t.Fatal(err)
		}

		src := filepath.Join(albumDir, "test.flac")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}

		// Pre-create cover.jpg.
		if err := os.WriteFile(filepath.Join(albumDir, "cover.jpg"), []byte("existing"), 0o644); err != nil {
			t.Fatal(err)
		}

		store := newTestStore(t)
		hook := NewCoverHook(store)
		_, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test-3",
			FilePath: src,
			CoverURL: ts.URL + "/cover.jpg",
		})
		if err != nil {
			t.Fatal(err)
		}

		// cover.jpg should still have original content.
		data, _ := os.ReadFile(filepath.Join(albumDir, "cover.jpg"))
		if string(data) != "existing" {
			t.Errorf("cover.jpg was overwritten: got %q", string(data))
		}
	})
}

func TestCoverHook_NoFilePath(t *testing.T) {
	store := newTestStore(t)
	hook := NewCoverHook(store)
	newPath, err := hook(context.Background(), domain.DownloadRecord{
		ID:       "test-4",
		FilePath: "",
		CoverURL: "http://example.com/cover.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if newPath != "" {
		t.Errorf("expected empty path, got %q", newPath)
	}
}

func TestExtractAlbumTitle(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Random Access Memories (2013)", "Random Access Memories"},
		{"Discovery", "Discovery"},
		{"Album (2024)", "Album"},
		{"(What's the Story) Morning Glory? (1995)", "(What's the Story) Morning Glory?"},
	}
	for _, tt := range tests {
		got := extractAlbumTitle(tt.input)
		if got != tt.want {
			t.Errorf("extractAlbumTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// newTestStore creates a SQLite store backed by a temporary database.
func newTestStore(t *testing.T) Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(fmt.Errorf("sqlite test store: %w", err))
	}
	t.Cleanup(func() { store.Close() })
	return store
}
