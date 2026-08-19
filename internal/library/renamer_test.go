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
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root, nil)

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
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root, nil)

		src := filepath.Join(dir, "Daft Punk - Get Lucky.mp3")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}

		// No tags, no metadata — file stays at original path.
		newPath, err := r.Rename(src, FileMeta{})
		if err != nil {
			t.Fatal(err)
		}

		if newPath != src {
			t.Errorf("expected file to stay at %q, got %q", src, newPath)
		}
	})

	t.Run("same path skipped", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "library")
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root, nil)

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
		r := NewRenamer("{artist}/{album}", dir, nil)
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
		r := NewRenamer("{artist}/{album}/{track:00} - {title}", root, nil)

		src := filepath.Join(dir, "Daft Punk - Random Access Memories - Get Lucky.flac")
		os.WriteFile(src, []byte("dummy"), 0o644)

		// No tags, no metadata — file stays at original path.
		newPath, err := r.Rename(src, FileMeta{})
		if err != nil {
			t.Fatal(err)
		}

		if newPath != src {
			t.Errorf("expected file to stay at %q, got %q", src, newPath)
		}
	})
}

// TestRenamerTarget verifies the pure path computation extracted from Rename:
// it resolves the template path without touching the filesystem.
func TestRenamerTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	r := NewRenamer("{artist}/{album} ({year})/{track:02d} - {title}", root, nil)

	got := r.target("/some/where/source.flac", FileMeta{
		Artist:   "Daft Punk",
		Album:    "Random Access Memories",
		Title:    "Get Lucky",
		Year:     2013,
		TrackNum: 2,
	}, false)
	want := filepath.Join(root, "Daft Punk/Random Access Memories (2013)/02 - Get Lucky.flac")
	if got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
	// Pure computation — nothing created on disk.
	if _, err := os.Stat(filepath.Join(root, "Daft Punk")); !os.IsNotExist(err) {
		t.Error("target() must not create directories")
	}

	// Missing artist → unresolvable.
	if got := r.target("/x.flac", FileMeta{}, false); got != "" {
		t.Errorf("target with no artist = %q, want empty", got)
	}
}

// TestRenamerRenameTagFallback verifies that partial metadata is completed from
// the file's embedded tags (used by the download renamer and organizer when the
// DB is missing a field).
func TestRenamerRenameTagFallback(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	r := NewRenamer("{artist}/{album}/{track:00} - {title}", root, nil)

	src := filepath.Join(dir, "source.flac")
	if err := os.WriteFile(src, minimalFLAC("Daft Punk", "Random Access Memories", "Get Lucky", 2013, 7, 1), 0o644); err != nil {
		t.Fatal(err)
	}

	// Only the artist is supplied; album/title/track/year come from the tags.
	newPath, err := r.Rename(src, FileMeta{Artist: "Daft Punk"})
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
}

// TestRenamerCompilationRouting verifies that the compilation flag switches to
// the compilation template — the single path decision shared by the download
// handler and the organizer.
func TestRenamerCompilationRouting(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	r := NewRenamerWithCompilation(
		"{artist}/{album} ({year})/{track:02d} - {title}",
		"Various Artists/{album} ({year})/{track:02d}. {artist} - {title}",
		root, nil,
	)

	src := filepath.Join(dir, "source.flac")
	if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := FileMeta{
		Artist:   "2Pac",
		Album:    "Best of 90s",
		Title:    "California Love",
		Year:     1995,
		TrackNum: 1,
	}

	// Regular track → folder template.
	regular := r.target(src, meta, false)
	if want := filepath.Join(root, "2Pac", "Best of 90s (1995)", "01 - California Love.flac"); regular != want {
		t.Errorf("regular target = %q, want %q", regular, want)
	}

	// Compilation track → compilation template, stays under Various Artists.
	va := r.target(src, meta, true)
	if want := filepath.Join(root, "Various Artists", "Best of 90s (1995)", "01. 2Pac - California Love.flac"); va != want {
		t.Errorf("compilation target = %q, want %q", va, want)
	}

	// A renamer built without a compilation template falls back to the folder
	// template for compilations.
	plain := NewRenamer("{artist}/{album} ({year})/{track:02d} - {title}", root, nil)
	fallback := plain.target(src, meta, true)
	if fallback != regular {
		t.Errorf("fallback compilation target = %q, want folder template %q", fallback, regular)
	}
}

func TestParseMetadataFromFilename(t *testing.T) {
	tests := []struct {
		filename   string
		wantArtist string
		wantAlbum  string
		wantTitle  string
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
