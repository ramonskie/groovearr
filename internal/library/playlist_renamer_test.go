package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlaylistRenamerResolvePath(t *testing.T) {
	r := NewPlaylistRenamer("{position:02d} {artist} - {title}", "/playlists")

	t.Run("basic template", func(t *testing.T) {
		got := r.ResolvePath(1, "Daft Punk", "Get Lucky", "flac")
		want := "/playlists/01 Daft Punk - Get Lucky.flac"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("position padding", func(t *testing.T) {
		got := r.ResolvePath(12, "Artist", "Title", "mp3")
		want := "/playlists/12 Artist - Title.mp3"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty template uses default", func(t *testing.T) {
		r2 := NewPlaylistRenamer("", "/root")
		got := r2.ResolvePath(5, "Artist", "Title", "mp3")
		want := "/root/05 Artist - Title.mp3"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestPlaylistRenamerRename(t *testing.T) {
	t.Run("moves file to new path", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "playlists")
		src := filepath.Join(dir, "song.flac")
		os.WriteFile(src, []byte("dummy"), 0o644)

		r := NewPlaylistRenamer("{position:02d} {artist} - {title}", root)
		newPath, err := r.Rename(src, 3, "Artist", "Title", "flac")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "03 Artist - Title.flac")
		if newPath != want {
			t.Errorf("got %q, want %q", newPath, want)
		}
		if _, err := os.Stat(newPath); err != nil {
			t.Errorf("file not at destination: %v", err)
		}
	})

	t.Run("same path returns original", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "pl")
		os.MkdirAll(root, 0o755)
		src := filepath.Join(root, "01 Song.mp3")
		os.WriteFile(src, []byte("x"), 0o644)

		r := NewPlaylistRenamer("{position:02d} {artist} - {title}", root)
		newPath, err := r.Rename(src, 1, "Song", "", "mp3")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "01 Song - Unknown.mp3")
		if newPath != want {
			t.Errorf("got %q, want %q", newPath, want)
		}
	})
}
