package library

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveWithinRoot(t *testing.T) {
	root := t.TempDir()

	// Fixtures under root.
	nestedDir := filepath.Join(root, "Artist", "Album")
	nestedFile := filepath.Join(nestedDir, "01 - Song.flac")
	writeTestFile(t, nestedFile)

	realInside := filepath.Join(root, "inside.flac")
	writeTestFile(t, realInside)
	insideLink := filepath.Join(root, "inside-link.flac")
	if err := os.Symlink(realInside, insideLink); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}

	// Fixtures outside root.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.flac")
	writeTestFile(t, outsideFile)
	escapeLink := filepath.Join(root, "escape-link.flac")
	if err := os.Symlink(outsideFile, escapeLink); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}

	tests := []struct {
		name    string
		target  string
		wantErr error
	}{
		{"nested file under root", nestedFile, nil},
		{"single dotdot escape", filepath.Join(root, "..", "secret.flac"), ErrPathOutsideRoot},
		{"double dotdot escape", filepath.Join(root, "..", "..", "secret.flac"), ErrPathOutsideRoot},
		{"absolute path outside root", outsideFile, ErrPathOutsideRoot},
		{"symlink escaping root", escapeLink, ErrPathOutsideRoot},
		{"symlink resolving inside root", insideLink, nil},
		{"directory rejected", nestedDir, ErrNotRegularFile},
		{"root itself rejected", root, ErrNotRegularFile},
		{"nonexistent target", filepath.Join(root, "missing.flac"), ErrPathUnresolvable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveWithinRoot(root, tc.target)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolveWithinRoot(%q) error = %v, want %v", tc.target, err, tc.wantErr)
			}

			if tc.wantErr != nil {
				if got != "" {
					t.Errorf("got non-empty path %q on error", got)
				}
				if strings.Contains(err.Error(), tc.target) {
					t.Errorf("error %q leaks the input path", err.Error())
				}
				return
			}

			want, werr := filepath.EvalSymlinks(tc.target)
			if werr != nil {
				t.Fatalf("fixture EvalSymlinks(%q): %v", tc.target, werr)
			}
			if got != want {
				t.Errorf("ResolveWithinRoot(%q) = %q, want %q", tc.target, got, want)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("result %q is not absolute", got)
			}
		})
	}
}

// TestResolveWithinRoot_NonRegularSocket covers the "FIFO/socket" case: a unix
// domain socket is not a regular file and must be rejected.
func TestResolveWithinRoot_NonRegularSocket(t *testing.T) {
	root := t.TempDir()
	sockPath := filepath.Join(root, "sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	got, err := ResolveWithinRoot(root, sockPath)
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("error = %v, want %v", err, ErrNotRegularFile)
	}
	if got != "" {
		t.Errorf("got %q, want empty path on error", got)
	}
}
