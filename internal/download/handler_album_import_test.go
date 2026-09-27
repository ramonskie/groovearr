package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
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

// TestAlbumImport_LinksOnceForWholeAlbum proves the album-level hook replaced
// the per-track link: an album with N matched files calls LinkImportedAlbum
// exactly once, not N times.
func TestAlbumImport_LinksOnceForWholeAlbum(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"01 - One.flac", "02 - Two.flac", "03 - Three.flac"} {
		createFile(t, filepath.Join(tmp, name))
	}

	resolver := func(context.Context, string, string, string, int, string) ([]domain.ExpectedTrack, string, error) {
		return []domain.ExpectedTrack{
			{TrackNumber: 1, Title: "One"},
			{TrackNumber: 2, Title: "Two"},
			{TrackNumber: 3, Title: "Three"},
		}, "mbid-1", nil
	}
	linker := &fakeTrackingLinker{}
	chain := []ImportHandler{NewTrackingLinkHandler(linker, testLogger())}
	h := NewAlbumImportHandler(chain, resolver, newMockAlbumStore(), nil, nil, testLogger())

	record := &Record{
		ID: "album-link-once", SourceName: "prowlarr", State: StateImporting,
		Artist: "Tool", Album: "Lateralus", AlbumType: "album", FilePath: tmp,
	}
	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if linker.calls != 1 {
		t.Fatalf("LinkImportedAlbum calls = %d, want exactly 1 for the whole album", linker.calls)
	}
	if linker.album != "Lateralus" {
		t.Fatalf("linked album = %q, want Lateralus", linker.album)
	}
}

// TestAlbumImport_HookErrorDoesNotFailImport proves the album hook is
// best-effort: a linking failure is swallowed and the import still succeeds.
func TestAlbumImport_HookErrorDoesNotFailImport(t *testing.T) {
	tmp := t.TempDir()
	createFile(t, filepath.Join(tmp, "01 - One.flac"))

	resolver := func(context.Context, string, string, string, int, string) ([]domain.ExpectedTrack, string, error) {
		return []domain.ExpectedTrack{{TrackNumber: 1, Title: "One"}}, "", nil
	}
	linker := &fakeTrackingLinker{err: errors.New("tracking store down")}
	chain := []ImportHandler{NewTrackingLinkHandler(linker, testLogger())}
	h := NewAlbumImportHandler(chain, resolver, newMockAlbumStore(), nil, nil, testLogger())

	record := &Record{
		ID: "album-hook-err", SourceName: "prowlarr", State: StateImporting,
		Artist: "Tool", Album: "Lateralus", AlbumType: "album", FilePath: tmp,
	}
	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle must swallow album hook error, got: %v", err)
	}
	if linker.calls != 1 {
		t.Fatalf("LinkImportedAlbum calls = %d, want 1", linker.calls)
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
