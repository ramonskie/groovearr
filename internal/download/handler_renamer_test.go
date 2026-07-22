package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
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
	record := &domain.DownloadRecord{
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

func TestFileRenamerHandler_NoFilePath(t *testing.T) {
	store := newMockDownloadStore()
	handler := NewFileRenamerHandler(&library.Renamer{}, store, nil)

	record := &domain.DownloadRecord{ID: "no-file"}
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
	record := &domain.DownloadRecord{
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
