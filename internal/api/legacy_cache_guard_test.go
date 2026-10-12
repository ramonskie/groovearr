package api

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// legacyCacheTables are the deprecated 24h JSON-blob metadata caches. The
// tables stay in the schema (additive-only rule) but must have no writers and
// no readers outside the one-time migration in internal/catalog/migrate.
var legacyCacheTables = []string{"album_discovery_cache", "artist_overview_cache"}

// legacyCacheAllowedRefs is the exhaustive allowlist of production files
// permitted to mention a legacy cache table, relative to internal/:
//
//   - library/sqlite/store.go      — CREATE TABLE IF NOT EXISTS only (the
//     tables are never dropped so existing DBs keep upgrading).
//   - catalog/migrate/backfill.go  — one-time read of the legacy blobs.
//
// Any other production reference is a regression: a writer must never come
// back, and no read path may consult the old caches again.
var legacyCacheAllowedRefs = map[string]bool{
	"library/sqlite/store.go":     true,
	"catalog/migrate/backfill.go": true,
}

// productionGoFiles walks the repository's internal/ tree and returns the
// content of every non-test .go file keyed by its slash-separated path
// relative to internal/. Walking from this test file keeps the guard
// independent of the test's working directory.
func productionGoFiles(t *testing.T) map[string]string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate internal/ tree")
	}
	internalDir := filepath.Dir(filepath.Dir(thisFile)) // .../internal

	files := make(map[string]string)
	err := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(internalDir, path)
		if relErr != nil {
			return relErr
		}
		files[filepath.ToSlash(rel)] = string(src)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no production Go files found under internal/")
	}
	return files
}

// legacyCacheWritePattern matches a write statement (INSERT [OR REPLACE] INTO,
// REPLACE INTO, or UPDATE) bound directly to the named table, allowing the
// usual whitespace, quoted-identifier, and OR REPLACE variants. It is
// deliberately anchored to the verb+table pair so it cannot match a write to
// one table and a distant mention of another.
func legacyCacheWritePattern(table string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(
		`(?is)\b(?:insert\s+(?:or\s+replace\s+)?into|replace\s+into|update)\s+[\x60"']?%s\b`,
		regexp.QuoteMeta(table),
	))
}

// TestNoProductionWritesToLegacyCacheTables enforces acceptance criterion 1:
// zero INSERT/UPDATE/REPLACE against the deprecated cache tables remains in any
// production Go file under internal/.
func TestNoProductionWritesToLegacyCacheTables(t *testing.T) {
	files := productionGoFiles(t)

	for _, table := range legacyCacheTables {
		re := legacyCacheWritePattern(table)
		for path, src := range files {
			loc := re.FindStringIndex(src)
			if loc == nil {
				continue
			}
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("%s:%d: production write to deprecated table %q (%q) is forbidden; the table must keep zero writers",
				path, line, table, strings.TrimSpace(re.FindString(src)))
		}
	}
}

// TestLegacyCacheReferencesAreAllowlisted enforces acceptance criterion 2:
// no read path consults the old tables. Every production file mentioning a
// legacy table must be in the allowlist (schema definition + one-time backfill).
func TestLegacyCacheReferencesAreAllowlisted(t *testing.T) {
	files := productionGoFiles(t)

	for path, src := range files {
		if legacyCacheAllowedRefs[path] {
			continue
		}
		for _, table := range legacyCacheTables {
			if strings.Contains(src, table) {
				t.Errorf("%s references deprecated table %q; only schema (library/sqlite/store.go) and one-time backfill (catalog/migrate/backfill.go) may",
					path, table)
			}
		}
	}
}
