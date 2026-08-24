package jobs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/library"
)

// ErrScanLayout is returned when the configured library path overlaps a
// download staging directory in either direction. The job fails so the UI
// shows the misconfiguration instead of a misleading "scanned 0".
var ErrScanLayout = errors.New("scan refused: library path overlaps a download directory")

// checkScanLayout refuses the scan when the library path overlaps any
// download staging directory in either direction — a library inside a
// staging dir, or a staging dir nested under the library, would sweep
// downloaded files into the library. Paths are resolved once (so a symlink
// alias of a staging dir is caught) and compared case-insensitively. This is
// the layout guard; the scanner itself is config-free and walks whatever it
// is given.
func checkScanLayout(cfg config.Config, libPath string) error {
	lib := resolveConfigPath(libPath)
	for _, p := range config.ProviderDownloadPaths(&cfg) {
		if p == "" {
			continue
		}
		root := resolveConfigPath(p)
		if config.PathIsUnder(lib, root) {
			return fmt.Errorf("%w: library path %q is inside download directory %q; move the library out of the download staging directory", ErrScanLayout, libPath, p)
		}
		if config.PathIsUnder(root, lib) {
			return fmt.Errorf("%w: download directory %q is inside the library path %q; stage downloads outside the library", ErrScanLayout, p, libPath)
		}
	}
	return nil
}

// resolveConfigPath returns the symlink-resolved absolute path, falling back
// to the cleaned absolute path when the target does not exist yet.
func resolveConfigPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(real)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// Scan scans all configured library paths, reporting per-file progress. Only
// the library path is scanned — the layout guard (checkScanLayout) rejects a
// library path that overlaps the download staging directory before any
// filesystem work happens.
func (r *Runners) Scan() Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Scanner == nil {
			return fmt.Errorf("scan: library scanner not configured")
		}
		cfg := r.deps.Config()
		paths := []string{cfg.Library.LibraryPath}
		if paths[0] == "" {
			paths[0] = config.DefaultLibraryPath
		}

		// Layout guard: refuse before the reconcile and pre-count walks touch
		// the filesystem. Failing here marks the job failed so the UI shows
		// the misconfiguration instead of a silent "scanned 0".
		if err := checkScanLayout(cfg, paths[0]); err != nil {
			return err
		}

		// Reconcile DB paths an interrupted organize run left behind (file moved,
		// DB not updated). Only runs when a killed organize armed the trigger, so a
		// normal scan pays nothing. Run before the walk so moved files aren't
		// re-imported as duplicate tracks.
		if r.OrganizeDivergencePossible() {
			if n, err := r.reconcileDivergedPaths(ctx, report, cfg); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.deps.Log.Warn("reconcile diverged paths failed", "error", err, "component", "jobs")
			} else if n > 0 {
				report(Report{Message: fmt.Sprintf("Reconciled %d moved track paths", n)})
			}
		}

		// Pre-count for progress estimation. Cancellable; reports a message so the
		// UI isn't stuck at 0% during the counting walk.
		report(Report{Message: "Counting audio files…"})
		total := 0
		for _, p := range paths {
			n, err := library.CountAudioFiles(ctx, p)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.deps.Log.Warn("count audio files failed", "path", p, "error", err, "component", "jobs")
				continue
			}
			total += n
		}
		if total == 0 {
			total = 1
		}

		done := 0
		var scanned, imported, skipped, scanErrors int
		for _, p := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			stats, err := r.deps.Scanner.ScanPathWithProgress(ctx, p, func(path string) {
				done++
				report(Report{Done: done, Total: total, Message: filepath.Base(path)})
			})
			scanned += stats.Scanned
			imported += stats.Imported
			skipped += stats.Skipped
			scanErrors += stats.Errors
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Non-cancellation scan errors are logged and the next path is
				// still attempted — mirrors the old synchronous scan handler. (A
				// layout refusal is caught earlier by checkScanLayout.)
				r.deps.Log.Error("scan path failed", "path", p, "error", err, "component", "jobs")
			}
		}

		// Surface the outcome counts in the final progress message so the UI can
		// show them once the job completes.
		report(Report{
			Done:    done,
			Total:   total,
			Message: fmt.Sprintf("scanned %d, imported %d, skipped %d, errors %d", scanned, imported, skipped, scanErrors),
		})
		return nil
	}
}
