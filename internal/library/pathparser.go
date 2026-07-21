package library

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Default fallback values used when metadata cannot be determined from a path.
const (
	DefaultArtistName = "Unknown Artist"
	DefaultAlbumTitle = "Unknown Album"
)

// ParseFileMetadata extracts artist, album, and track title from a file path.
// Handles common directory structures:
//
//	Artist/Album (Year)/01 - Title.ext  → artist, album, title
//	Artist - Album/01 - Title.ext       → artist, album, title
//	Artist/01 - Title.ext               → artist, DefaultAlbumTitle, title
//	Artist - Title.ext                  → artist, DefaultAlbumTitle, title
//	Artist - Album - Title.ext          → artist, album, title
func ParseFileMetadata(path string) (artist, album, title string) {
	dir := filepath.Dir(path)
	filename := filepath.Base(path)
	title = strings.TrimSuffix(filename, filepath.Ext(filename))

	// Strip track number prefix.
	if m := TrackNumRE.FindStringSubmatch(title); m != nil {
		title = m[2]
	}

	parts := splitPath(dir)

	// Filter out disc directories (CD1, Disc 2, etc.).
	discPattern := regexp.MustCompile(`^(?i)(cd|disc)\s*\d+$`)
	filtered := parts[:0]
	for _, p := range parts {
		if !discPattern.MatchString(p) {
			filtered = append(filtered, p)
		}
	}
	parts = filtered
	parts = append(parts, title)

	switch {
	case len(parts) >= 3:
		artist = parts[0]
		album = regexp.MustCompile(`\s*\(\d{4}\)\s*$`).ReplaceAllString(parts[1], "")
		album = strings.TrimSpace(album)
		title = parts[2]
	case len(parts) == 2:
		if strings.Contains(parts[0], " - ") {
			ap := strings.SplitN(parts[0], " - ", 2)
			artist = ap[0]
			album = ap[1]
		} else {
			artist = parts[0]
			album = DefaultAlbumTitle
		}
		title = parts[1]
	default:
		if strings.Contains(title, " - ") {
			flatParts := strings.SplitN(title, " - ", 3)
			switch len(flatParts) {
			case 3:
				artist = strings.TrimSpace(flatParts[0])
				album = strings.TrimSpace(flatParts[1])
				title = strings.TrimSpace(flatParts[2])
			case 2:
				artist = strings.TrimSpace(flatParts[0])
				album = DefaultAlbumTitle
				title = strings.TrimSpace(flatParts[1])
			}
		} else {
			artist = DefaultArtistName
			album = DefaultAlbumTitle
			title = parts[0]
		}
	}

	return artist, album, title
}

// splitPath normalizes and splits a directory path into segments,
// filtering empty and "." entries.
func splitPath(p string) []string {
	p = strings.ReplaceAll(p, "\\", "/")
	parts := strings.Split(p, "/")
	var result []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		result = append(result, part)
	}
	return result
}

// ParseFlatFilename extracts artist, album, and title from a flat filename
// like "Artist - Title.ext" or "Artist - Album - Title.ext".
// Deprecated: prefer metadata from discovery providers or audio file tags.
func ParseFlatFilename(filename string) (artist, album, title string) {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	parts := strings.SplitN(base, " - ", 3)

	switch len(parts) {
	case 3:
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	case 2:
		return strings.TrimSpace(parts[0]), DefaultAlbumTitle, strings.TrimSpace(parts[1])
	default:
		return "", "", ""
	}
}
