package download

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestScanAudioFiles_Recursive(t *testing.T) {
	tmp := t.TempDir()

	// Flat files in root.
	createFile(t, filepath.Join(tmp, "01 Intro.flac"))
	createFile(t, filepath.Join(tmp, "02 Song.mp3"))
	createFile(t, filepath.Join(tmp, "cover.jpg")) // non-audio, should be skipped

	// Multi-disc subdirectories.
	cd1 := filepath.Join(tmp, "CD1")
	cd2 := filepath.Join(tmp, "CD2")
	mustMkdir(t, cd1)
	mustMkdir(t, cd2)
	createFile(t, filepath.Join(cd1, "01 Track One.flac"))
	createFile(t, filepath.Join(cd1, "02 Track Two.opus"))
	createFile(t, filepath.Join(cd2, "01 Live One.mp3"))
	createFile(t, filepath.Join(cd2, "02 Live Two.wav"))

	// Nested subdirectory.
	bonus := filepath.Join(tmp, "Bonus")
	mustMkdir(t, bonus)
	createFile(t, filepath.Join(bonus, "Bonus Track.aac"))

	h := &AlbumImportHandler{}
	got, err := h.scanAudioFiles(tmp)
	if err != nil {
		t.Fatalf("scanAudioFiles: %v", err)
	}

	want := []string{
		filepath.Join(tmp, "01 Intro.flac"),
		filepath.Join(tmp, "02 Song.mp3"),
		filepath.Join(bonus, "Bonus Track.aac"),
		filepath.Join(cd1, "01 Track One.flac"),
		filepath.Join(cd1, "02 Track Two.opus"),
		filepath.Join(cd2, "01 Live One.mp3"),
		filepath.Join(cd2, "02 Live Two.wav"),
	}
	sort.Strings(got)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("count mismatch: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("file[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScanAudioFiles_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	h := &AlbumImportHandler{}
	got, err := h.scanAudioFiles(tmp)
	if err != nil {
		t.Fatalf("scanAudioFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 files in empty dir, got %d: %v", len(got), got)
	}
}

func TestScanAudioFiles_NoAudioFiles(t *testing.T) {
	tmp := t.TempDir()
	createFile(t, filepath.Join(tmp, "cover.jpg"))
	createFile(t, filepath.Join(tmp, "info.txt"))
	createFile(t, filepath.Join(tmp, "folder.png"))

	h := &AlbumImportHandler{}
	got, err := h.scanAudioFiles(tmp)
	if err != nil {
		t.Fatalf("scanAudioFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 audio files, got %d: %v", len(got), got)
	}
}

func TestScanAudioFiles_NonexistentDir(t *testing.T) {
	h := &AlbumImportHandler{}
	_, err := h.scanAudioFiles("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func createFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("dummy"), 0644); err != nil {
		t.Fatalf("createFile(%q): %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir(%q): %v", path, err)
	}
}
