// Package library provides file scanning and library import.
package library

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// m3uExtensions are the playlist extensions FindM3UFiles recognizes. Matching
// is case-insensitive so ".M3U" / ".M3U8" (common on Windows-managed
// libraries) are found too.
var m3uExtensions = map[string]bool{
	".m3u":  true,
	".m3u8": true,
}

const (
	// maxM3UWalkDepth bounds how deep FindM3UFiles descends below dir. An album
	// directory can equal the library root (a track stored at the root), so an
	// unbounded recursive walk could traverse the whole library on a single
	// album request and enqueue every playlist it finds. Four levels covers the
	// {artist}/{album}/{Disc N}/{list}.m3u layouts with margin.
	maxM3UWalkDepth = 4
	// maxM3UFiles caps how many playlists one walk returns, so a single album
	// request cannot enqueue an unbounded number of archive entries. WalkDir
	// visits entries in lexical order, so the cap is deterministic.
	maxM3UFiles = 100
)

// FindM3UFiles walks dir recursively and returns the absolute paths of every
// regular file whose extension is .m3u or .m3u8 (case-insensitive). The result
// is sorted for deterministic output. It is a pure lookup — it never reads,
// creates, or rewrites a playlist.
//
// The walk is bounded: it descends at most maxM3UWalkDepth levels below dir and
// stops after maxM3UFiles matches (see the constants for why an unbounded walk
// is unsafe here).
//
// An unreadable directory (or a raced-away entry) is skipped rather than
// aborting the walk, so one bad subtree cannot hide the rest. A missing or
// non-directory dir therefore yields an empty slice.
func FindM3UFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Playlists are optional: skip an unreadable entry/directory and
			// keep walking its siblings instead of failing the whole lookup.
			return nil
		}
		if len(out) >= maxM3UFiles {
			// Bound the walk: every further match would be discarded anyway.
			return filepath.SkipAll
		}
		if d.IsDir() {
			// Skip descending past the depth cap. The root itself (rel ".")
			// is always allowed; each child level is counted below it.
			if path != dir {
				if rel, relErr := filepath.Rel(dir, path); relErr != nil ||
					strings.Count(rel, string(filepath.Separator))+1 >= maxM3UWalkDepth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !m3uExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		// Require a regular file explicitly: a device, FIFO, socket, or
		// symlink must not be handed back as a playlist.
		if info, statErr := d.Info(); statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return nil
		}
		out = append(out, abs)
		return nil
	})
	sort.Strings(out)
	return out
}
