package tagging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTagsAtomicSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.flac")
	original := []byte("original audio bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	tagger := New(nil)
	err := tagger.writeTagsAtomic(path, true, func(tmpPath string) error {
		return os.WriteFile(tmpPath, []byte("tagged audio bytes"), 0o644)
	})
	if err != nil {
		t.Fatalf("writeTagsAtomic: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "tagged audio bytes" {
		t.Fatalf("file not replaced, got %q", data)
	}
	assertNoTempLeft(t, dir)
}

// TestWriteTagsAtomicNoCopy exercises the FLAC path: the writer reads the
// original directly and emits to the temp (no copy paid), which still commits
// atomically and leaves no temp behind.
func TestWriteTagsAtomicNoCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.flac")
	original := []byte("original audio bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	tagger := New(nil)
	err := tagger.writeTagsAtomic(path, false, func(tmpPath string) error {
		// Simulate go-flac's Save: the writer has read the original and emits
		// the full content to the given output path.
		return os.WriteFile(tmpPath, []byte("rewritten"), 0o644)
	})
	if err != nil {
		t.Fatalf("writeTagsAtomic: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rewritten" {
		t.Fatalf("file not replaced via no-copy path, got %q", data)
	}
	assertNoTempLeft(t, dir)
}

func TestWriteTagsAtomicPreservesOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.flac")
	original := []byte("original audio bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	tagger := New(nil)
	err := tagger.writeTagsAtomic(path, true, func(tmpPath string) error {
		return errors.New("tag write failed")
	})
	if err == nil {
		t.Fatal("want error from write closure")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original audio bytes" {
		t.Fatalf("original file was modified on failure, got %q", data)
	}
	assertNoTempLeft(t, dir)
}

func TestWriteTagsAtomicPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(path, []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}

	tagger := New(nil)
	if err := tagger.writeTagsAtomic(path, true, func(tmpPath string) error {
		return os.WriteFile(tmpPath, []byte("new"), 0o644)
	}); err != nil {
		t.Fatalf("writeTagsAtomic: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode not preserved, got %o", info.Mode().Perm())
	}
}

func assertNoTempLeft(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".groovearr-tag-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
