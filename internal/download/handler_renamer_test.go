package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/library"
)

func TestFileRenamerHandler(t *testing.T) {
	// Setup: create temp dirs and a test file.
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "downloads")
	libRoot := filepath.Join(tmpDir, "library")
	os.MkdirAll(srcDir, 0o755)

	srcFile := filepath.Join(srcDir, "test.mp3")
	if err := os.WriteFile(srcFile, []byte("dummy audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newMockDownloadStore()
	record := &Record{
		ID:          "test-rename-1",
		FilePath:    srcFile,
		Filename:    "test.mp3",
		Artist:      "Test Artist",
		Album:       "Test Album",
		Title:       "Test Title",
		TrackNumber: 1,
		Year:        2024,
	}
	store.Insert(context.Background(), record)

	renamer := library.NewRenamer("{artist}/{album} ({year})/{tracknum:02d} {title}", libRoot, nil)
	handler := NewFileRenamerHandler(renamer, store, nil)

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Verify file was moved (path changed from srcFile).
	got, _ := store.Get(context.Background(), "test-rename-1")
	if got.FilePath == srcFile {
		t.Errorf("file path should have changed, still got %q", got.FilePath)
	}
	if _, err := os.Stat(got.FilePath); err != nil {
		t.Errorf("renamed file should exist at %s: %v", got.FilePath, err)
	}
}

func TestFileRenamerHandler_Compilation(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "downloads")
	libRoot := filepath.Join(tmpDir, "library")
	os.MkdirAll(srcDir, 0o755)

	srcFile := filepath.Join(srcDir, "test.mp3")
	if err := os.WriteFile(srcFile, []byte("dummy audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newMockDownloadStore()
	record := &Record{
		ID:          "test-rename-va",
		FilePath:    srcFile,
		Filename:    "test.mp3",
		Artist:      "2Pac",
		Album:       "Best of 90s",
		Title:       "California Love",
		TrackNumber: 1,
		Year:        1995,
		AlbumType:   "Compilation",
	}
	store.Insert(context.Background(), record)

	// A compilation-aware renamer routes the record through the compilation
	// template, exactly like the organizer — same paths across every flow.
	renamer := library.NewRenamerWithCompilation(
		"{artist}/{album} ({year})/{track:02d} - {title}",
		"Various Artists/{album} ({year})/{track:02d}. {artist} - {title}",
		libRoot, nil,
	)
	handler := NewFileRenamerHandler(renamer, store, nil)

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	got, _ := store.Get(context.Background(), "test-rename-va")
	want := filepath.Join(libRoot, "Various Artists", "Best of 90s (1995)", "01. 2Pac - California Love.mp3")
	if got.FilePath != want {
		t.Errorf("compilation path = %q, want %q", got.FilePath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("renamed file should exist at %s: %v", want, err)
	}
}

func TestFileRenamerHandler_NoFilePath(t *testing.T) {
	store := newMockDownloadStore()
	handler := NewFileRenamerHandler(&library.Renamer{}, store, nil)

	record := &Record{ID: "no-file"}
	err := handler.Handle(context.Background(), record)
	if err == nil {
		t.Error("expected error for empty file path")
	}
}

func TestFileRenamerHandler_ExistingPath(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "already_placed.mp3")
	os.WriteFile(srcFile, []byte("audio"), 0o644)

	store := newMockDownloadStore()
	record := &Record{
		ID:       "test-rename-3",
		FilePath: srcFile,
		Filename: "already_placed.mp3",
	}
	store.Insert(context.Background(), record)

	// Renamer with missing metadata should keep file unchanged.
	renamer := library.NewRenamer("{artist}/{album}/{title}", t.TempDir(), nil)
	handler := NewFileRenamerHandler(renamer, store, nil)

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// File should remain in place (no metadata to build path).
	if _, err := os.Stat(srcFile); err != nil {
		t.Errorf("file should still exist: %v", err)
	}
}
