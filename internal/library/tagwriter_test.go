package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
	"github.com/ramonskie/groovearr/internal/domain"
)

func TestTagWriterHook(t *testing.T) {
	t.Run("no-op on empty FilePath", func(t *testing.T) {
		hook := NewTagWriterHook()
		path, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test",
			FilePath: "",
			Filename: "test.mp3",
		})
		if err != nil {
			t.Fatal(err)
		}
		if path != "" {
			t.Errorf("expected empty path, got %q", path)
		}
	})

	t.Run("no-op on unsupported format", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "test.ogg")
		if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}
		hook := NewTagWriterHook()
		_, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test",
			FilePath: src,
			Filename: "test.ogg",
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no-op when no metadata in filename", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "no-metadata.mp3")
		if err := os.WriteFile(src, minimalMP3(), 0o644); err != nil {
			t.Fatal(err)
		}
		hook := NewTagWriterHook()
		_, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test",
			FilePath: src,
			Filename: "no-metadata.mp3",
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("writes ID3v2 tags to MP3", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "Artist - Title.mp3")
		if err := os.WriteFile(src, minimalMP3(), 0o644); err != nil {
			t.Fatal(err)
		}

		hook := NewTagWriterHook()
		_, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test-mp3",
			FilePath: src,
			Filename: "Artist - Title.mp3",
		})
		if err != nil {
			t.Fatal(err)
		}

		// Read back with dhowden/tag.
		f, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		m, err := tag.ReadFrom(f)
		if err != nil {
			t.Fatalf("read tags back: %v", err)
		}
		if m.Artist() != "Artist" {
			t.Errorf("artist = %q, want Artist", m.Artist())
		}
		if m.Title() != "Title" {
			t.Errorf("title = %q, want Title", m.Title())
		}
		if m.Album() != "Unknown Album" {
			t.Errorf("album = %q, want Unknown Album", m.Album())
		}
	})

	t.Run("writes Vorbis comments to FLAC", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "Artist - Title.flac")
		if err := os.WriteFile(src, minimalFLAC("", "", "", 0, 0, 0), 0o644); err != nil {
			t.Fatal(err)
		}

		hook := NewTagWriterHook()
		_, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test-flac",
			FilePath: src,
			Filename: "Artist - Title.flac",
		})
		if err != nil {
			t.Fatal(err)
		}

		// Read back with dhowden/tag.
		f, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		m, err := tag.ReadFrom(f)
		if err != nil {
			t.Fatalf("read tags back: %v", err)
		}
		if m.Artist() != "Artist" {
			t.Errorf("artist = %q, want Artist", m.Artist())
		}
		if m.Title() != "Title" {
			t.Errorf("title = %q, want Title", m.Title())
		}
	})

	t.Run("embeds cover art in MP3", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "Artist - Title.mp3")
		coverPath := filepath.Join(dir, "cover.jpg")
		if err := os.WriteFile(src, minimalMP3(), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(coverPath, []byte("fake-jpeg-data"), 0o644); err != nil {
			t.Fatal(err)
		}

		hook := NewTagWriterHook()
		_, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test-cover",
			FilePath: src,
			Filename: "Artist - Title.mp3",
		})
		if err != nil {
			t.Fatal(err)
		}

		// Open with id3v2 to verify cover art.
		tagObj, err := id3v2.Open(src, id3v2.Options{Parse: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tagObj.Close()

		frames := tagObj.GetFrames(tagObj.CommonID("Attached picture"))
		if len(frames) == 0 {
			t.Fatal("no attached picture frame found")
		}
	})

	t.Run("does not fail on non-existent cover.jpg", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "Artist - Song.mp3")
		if err := os.WriteFile(src, minimalMP3(), 0o644); err != nil {
			t.Fatal(err)
		}

		hook := NewTagWriterHook()
		_, err := hook(context.Background(), domain.DownloadRecord{
			ID:       "test-nocover",
			FilePath: src,
			Filename: "Artist - Song.mp3",
		})
		if err != nil {
			t.Fatalf("hook failed when cover missing: %v", err)
		}
	})
}

// minimalMP3 creates a minimal MP3 file with an empty ID3v2.4 tag.
// The resulting file has a valid ID3 header but no frames.
func minimalMP3() []byte {
	tag := id3v2.NewEmptyTag()
	tag.SetVersion(4)
	tag.SetArtist("") // force tag not to be empty

	var buf []byte
	// Write the tag header to get the raw bytes.
	w := &bytesWriter{}
	tag.WriteTo(w)

	// Append fake audio data so the file isn't just a tag.
	buf = append(w.buf, 0xFF, 0xFB, 0x90, 0x00) // MPEG1 Layer3 sync header
	return buf
}

type bytesWriter struct {
	buf []byte
}

func (w *bytesWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}
