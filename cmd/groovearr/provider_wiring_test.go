package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// musicbrainzPkgDir is the one package allowed to construct the shared
// MusicBrainz *APIClient (through its own NewAPIClient / NewClient).
const musicbrainzPkgDir = "internal/providers/musicbrainz"

// prowlarrPkgDir must never import the MusicBrainz provider package: after the
// D-001 unification prowlarr consumes the shared release resolver through the
// discovery.ReleaseResolver interface, so a concrete sibling-provider import
// here would be an AGENTS §1 violation.
const prowlarrPkgDir = "internal/providers/prowlarr"

// repoRoot resolves the module root from this test file's location
// (<root>/cmd/groovearr/provider_wiring_test.go), so the scan is robust to
// whatever working directory `go test` happens to use.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// walkGoFiles calls fn for every .go file under root, skipping vendor and
// dot-directories (e.g. .git, .tmp). fn receives the repo-relative slash path
// and the absolute file path.
func walkGoFiles(t *testing.T, root string, fn func(rel, path string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || (strings.HasPrefix(name, ".") && name != "." && name != "..") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		fn(filepath.ToSlash(rel), path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// TestExactlyOneMusicBrainzAPIClientConstruction proves the single-client
// invariant from D-001 §3: across the whole module there is exactly ONE call
// that constructs a MusicBrainz *APIClient, and it lives inside the
// internal/providers/musicbrainz package (the plugin that owns the process's
// shared client). An extra construction site — e.g. a second client built by a
// consumer — would reintroduce the duplicate rate-limit pacer that unification
// removed (AGENTS §8).
func TestExactlyOneMusicBrainzAPIClientConstruction(t *testing.T) {
	root := repoRoot(t)
	// Build the needle from fragments so this guard file's own source does not
	// match itself. Match the bare token (not only "…(") so a spaced call such
	// as `NewAPIClient (cfg, logger)` is still caught.
	needle := "New" + "API" + "Client"

	var sites []string
	walkGoFiles(t, root, func(rel, path string) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, needle) {
				continue
			}
			// Count references only: skip the function declaration and any
			// comment mention of the constructor.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			sites = append(sites, fmt.Sprintf("%s:%d", rel, i+1))
		}
	})

	if len(sites) != 1 {
		t.Fatalf("expected exactly one MusicBrainz APIClient construction site, found %d: %v", len(sites), sites)
	}
	if !strings.HasPrefix(sites[0], musicbrainzPkgDir+"/") {
		t.Fatalf("the sole construction site %q is outside %s: the shared client must be owned by the musicbrainz package", sites[0], musicbrainzPkgDir)
	}
}

// TestProwlarrDoesNotImportMusicBrainz proves the Phase 2 decoupling holds
// structurally: prowlarr consumes the shared release resolver through the
// discovery.ReleaseResolver interface, so it must not import the MusicBrainz
// provider package (AGENTS §1 — providers never import a sibling provider
// concretely). Parsing imports rather than matching raw text keeps this robust
// against a comment or string that merely mentions the package.
func TestProwlarrDoesNotImportMusicBrainz(t *testing.T) {
	root := repoRoot(t)
	forbidden := "github.com/ramonskie/groovearr/internal/providers/" + "musicbrainz"
	dir := filepath.Join(root, filepath.FromSlash(prowlarrPkgDir))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", prowlarrPkgDir, err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		checked++
		for _, imp := range file.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); p == forbidden {
				t.Errorf("%s imports %s: prowlarr must depend on discovery.ReleaseResolver, not a concrete sibling provider", e.Name(), forbidden)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no Go files found in %s; the guard would be vacuous", prowlarrPkgDir)
	}
}
