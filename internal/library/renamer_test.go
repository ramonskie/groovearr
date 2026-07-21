package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestRenamerRename(t *testing.T) {
	t.Run("full metadata", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "library")
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root)

		src := filepath.Join(dir, "Daft Punk - Get Lucky.flac")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}

		newPath, err := r.Rename(src, FileMeta{
			Artist:   "Daft Punk",
			Album:    "Random Access Memories",
			Title:    "Get Lucky",
			Year:     2013,
			TrackNum: 7,
		})
		if err != nil {
			t.Fatal(err)
		}

		expected := filepath.Join(root, "Daft Punk/Random Access Memories/07 - Get Lucky.flac")
		if newPath != expected {
			t.Errorf("got %q, want %q", newPath, expected)
		}

		if _, err := os.Stat(newPath); err != nil {
			t.Fatalf("renamed file not found: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("original file still exists after rename")
		}
	})

	t.Run("filename fallback", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "library")
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root)

		src := filepath.Join(dir, "Daft Punk - Get Lucky.mp3")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}

		newPath, err := r.Rename(src, FileMeta{})
		if err != nil {
			t.Fatal(err)
		}

		expected := filepath.Join(root, "Daft Punk/Unknown Album/00 - Get Lucky.mp3")
		if newPath != expected {
			t.Errorf("got %q, want %q", newPath, expected)
		}
	})

	t.Run("same path skipped", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "library")
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root)

		// File already at the computed target path.
		targetDir := filepath.Join(root, "Daft Punk/Random Access Memories")
		os.MkdirAll(targetDir, 0o755)
		src := filepath.Join(targetDir, "07 - Get Lucky.flac")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}

		newPath, err := r.Rename(src, FileMeta{
			Artist:   "Daft Punk",
			Album:    "Random Access Memories",
			Title:    "Get Lucky",
			TrackNum: 7,
		})
		if err != nil {
			t.Fatal(err)
		}
		if newPath != src {
			t.Errorf("expected no-op, got %q", newPath)
		}
	})

	t.Run("no metadata no-op", func(t *testing.T) {
		dir := t.TempDir()
		r := NewRenamer("{artist}/{album}", dir)
		src := filepath.Join(dir, "track.mp3")
		os.WriteFile(src, []byte("dummy"), 0o644)

		newPath, err := r.Rename(src, FileMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if newPath != src {
			t.Errorf("expected no-op, got %q", newPath)
		}
	})

	t.Run("three-part filename fallback", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "library")
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root)

		src := filepath.Join(dir, "Daft Punk - Random Access Memories - Get Lucky.flac")
		os.WriteFile(src, []byte("dummy"), 0o644)

		newPath, err := r.Rename(src, FileMeta{})
		if err != nil {
			t.Fatal(err)
		}

		expected := filepath.Join(root, "Daft Punk/Random Access Memories/00 - Get Lucky.flac")
		if newPath != expected {
			t.Errorf("got %q, want %q", newPath, expected)
		}
	})
}

func TestParseMetadataFromFilename(t *testing.T) {
	tests := []struct {
		filename     string
		wantArtist   string
		wantAlbum    string
		wantTitle    string
	}{
		{"Daft Punk - Get Lucky.flac", "Daft Punk", "Unknown Album", "Get Lucky"},
		{"Artist - Album - Title.mp3", "Artist", "Album", "Title"},
		{"NoSeparator.flac", "", "", ""},
		{"", "", "", ""},
	}

	for _, tt := range tests {
		artist, album, title := ParseFlatFilename(tt.filename)
		if artist != tt.wantArtist || album != tt.wantAlbum || title != tt.wantTitle {
			t.Errorf("ParseFlatFilename(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tt.filename, artist, album, title,
				tt.wantArtist, tt.wantAlbum, tt.wantTitle)
		}
	}
}

func TestScanMetadata(t *testing.T) {
	t.Run("full chain", func(t *testing.T) {
		artist := &domain.Artist{Name: "Daft Punk"}
		album := &domain.Album{Title: "RAM", Year: 2013}
		track := &domain.Track{Title: "Get Lucky", TrackNumber: 3, DiscNumber: 1}

		meta := ScanMetadata(track, artist, album)
		if meta.Artist != "Daft Punk" {
			t.Errorf("artist: got %q", meta.Artist)
		}
		if meta.Album != "RAM" {
			t.Errorf("album: got %q", meta.Album)
		}
		if meta.Year != 2013 {
			t.Errorf("year: got %d", meta.Year)
		}
	})

	t.Run("nil artist/album", func(t *testing.T) {
		track := &domain.Track{Title: "Trackname", TrackNumber: 1}
		meta := ScanMetadata(track, nil, nil)
		if meta.Artist != "" {
			t.Errorf("artist should be empty, got %q", meta.Artist)
		}
	})
}
