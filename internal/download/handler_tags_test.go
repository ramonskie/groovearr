package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestTagWriterHandler_MP3(t *testing.T) {
	// Create a minimal valid MP3 file (enough for id3v2 to parse).
	// A valid ID3v2 tag at minimum needs an "ID3" header.
	tmpDir := t.TempDir()
	mp3Path := filepath.Join(tmpDir, "Test Artist - Test Title.mp3")

	// Write minimal file with ID3v2 header.
	header := make([]byte, 10)
	copy(header, "ID3")
	header[3] = 4 // version 2.4
	header[4] = 0 // flags
	// Size: synchsafe int for 0 bytes
	os.WriteFile(mp3Path, header, 0o644)

	handler := NewTagWriterHandler()
	record := &domain.DownloadRecord{
		ID:       "test-tags-1",
		FilePath: mp3Path,
		Artist:   "Test Artist",
		Album:    "Test Album",
		Title:    "Test Title",
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	// Tags written — id3v2 library doesn't error on valid files.
}

func TestTagWriterHandler_FLAC(t *testing.T) {
	tmpDir := t.TempDir()
	flacPath := filepath.Join(tmpDir, "Artist - Title.flac")

	// Write minimal file — FLAC parsing will fail on empty data but handler
	// treats this as non-fatal.
	os.WriteFile(flacPath, []byte("fLaC\x00"), 0o644)

	handler := NewTagWriterHandler()
	record := &domain.DownloadRecord{
		ID:       "test-tags-2",
		FilePath: flacPath,
		Artist:   "Artist",
		Album:    "Album",
		Title:    "Title",
	}

	// Should not error (tag write failures are non-fatal).
	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle should not error on tag write failure: %v", err)
	}
}

func TestTagWriterHandler_NoFilePath(t *testing.T) {
	handler := NewTagWriterHandler()
	record := &domain.DownloadRecord{ID: "test-tags-3"}
	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle should return nil for empty file path: %v", err)
	}
}

func TestTagWriterHandler_NonAudio(t *testing.T) {
	tmpDir := t.TempDir()
	txtPath := filepath.Join(tmpDir, "readme.txt")
	os.WriteFile(txtPath, []byte("hello"), 0o644)

	handler := NewTagWriterHandler()
	record := &domain.DownloadRecord{
		ID:       "test-tags-4",
		FilePath: txtPath,
	}
	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle should skip non-audio files: %v", err)
	}
}
