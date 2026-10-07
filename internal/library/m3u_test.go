package library

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestFindM3UFiles(t *testing.T) {
	root := t.TempDir()

	write := func(rel string) string {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte("#EXTM3U\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	atRoot := write("album.m3u")
	inSubdir := write("sub/nested.m3u8")
	upper := write("UPPER.M3U")

	// Non-playlist files everywhere must be ignored.
	write("notes.txt")
	write("song.flac")
	write("sub/readme.md")
	write("sub/deep/other.mp3")

	got := FindM3UFiles(root)
	want := []string{atRoot, inSubdir, upper}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindM3UFiles() = %v, want %v", got, want)
	}

	// Determinism: a second call returns the identical order.
	if again := FindM3UFiles(root); !reflect.DeepEqual(again, got) {
		t.Fatalf("FindM3UFiles() not deterministic: %v vs %v", got, again)
	}
}

func TestFindM3UFilesMissingDir(t *testing.T) {
	got := FindM3UFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(got) != 0 {
		t.Fatalf("FindM3UFiles(missing) = %v, want empty", got)
	}
}

// TestFindM3UFilesSkipsNonRegular pins that a directory literally named
// "album.m3u" is not a playlist: only regular files qualify.
func TestFindM3UFilesSkipsNonRegular(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "album.m3u"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindM3UFiles(root); len(got) != 0 {
		t.Fatalf("FindM3UFiles() = %v, want empty (directory is not a playlist)", got)
	}
}

// TestFindM3UFilesDepthBound pins the walk-depth cap: a playlist within
// maxM3UWalkDepth levels is found, one below it is not, so an album dir equal
// to the library root cannot recurse through the whole library.
func TestFindM3UFilesDepthBound(t *testing.T) {
	root := t.TempDir()

	write := func(rel string) string {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte("#EXTM3U\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	// File depth 4 is within the bound; the containing "d" dir would be at
	// depth 4, so the deeper file is pruned.
	shallow := write("a/b/c/shallow.m3u")
	write("a/b/c/d/too-deep.m3u")

	got := FindM3UFiles(root)
	if want := []string{shallow}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FindM3UFiles() = %v, want %v (depth bound broken)", got, want)
	}
}

// TestFindM3UFilesCountBound pins the file-count cap: a flat tree with more
// than maxM3UFiles playlists returns exactly maxM3UFiles, sorted.
func TestFindM3UFilesCountBound(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxM3UFiles+50; i++ {
		p := filepath.Join(root, fmt.Sprintf("list-%03d.m3u", i))
		if err := os.WriteFile(p, []byte("#EXTM3U\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := FindM3UFiles(root)
	if len(got) != maxM3UFiles {
		t.Fatalf("FindM3UFiles() returned %d, want exactly %d (count bound)", len(got), maxM3UFiles)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("FindM3UFiles() result is not sorted: %v", got)
	}
}
