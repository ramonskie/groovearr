// Package sanitize provides filesystem-safe string cleaning used across
// the download, library, and playlist packages.
package sanitize

import (
	"regexp"
	"strings"
)

var spaceRE = regexp.MustCompile(`\s+`)

// FileName makes a string safe for use as a filename or path component.
// Characters invalid on most filesystems (/, \, :, *, ?, ", <, >, |) are
// replaced or removed. Consecutive whitespace is collapsed and the result
// is trimmed.
func FileName(s string) string {
	if s == "" {
		return "unknown"
	}
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	for _, ch := range []string{":", "*", "?", "\"", "<", ">", "|"} {
		s = strings.ReplaceAll(s, ch, "")
	}
	s = spaceRE.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	return s
}

// PathSegment is like FileName but returns "Unknown" (not "unknown")
// for empty inputs, matching the library path resolver's convention.
func PathSegment(s string) string {
	if s == "" {
		return "Unknown"
	}
	return FileName(s)
}

// DirName makes a string safe for use as a directory name by replacing
// all path-separator and special characters with underscores.
func DirName(name string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	return replacer.Replace(name)
}
