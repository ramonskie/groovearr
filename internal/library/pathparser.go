package library

import (
	"path/filepath"
	"regexp"
	"strconv"
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

// ParseArtistTitle extracts artist, title, and track number from a filename.
// Handles patterns like:
//
//	"Artist - Title.mp3"              → artist="Artist", title="Title"
//	"08 - Artist - Title.mp3"         → artist="Artist", title="Title", trackNum=8
//	"01 - Title.mp3"                  → trackNum=1, title="Title" (no artist)
//	"artist_-_title_(remix).mp3"       → artist="artist", title="title (remix)"
func ParseArtistTitle(filename string) (artist, title string, trackNum int) {
	// Normalize Windows backslashes for path.Base compatibility.
	normalized := strings.ReplaceAll(filename, "\\", "/")
	base := strings.TrimSuffix(filepath.Base(normalized), filepath.Ext(normalized))

	// Try " - " delimiter first.
	parts := strings.SplitN(base, " - ", 4)
	artist, title, trackNum = splitArtistParts(parts)
	if artist != "" || title != base {
		if artist != "" {
			title = stripArtistPrefix(title, artist)
		}
		return artist, title, trackNum
	}

	// Try "_-_" delimiter.
	parts = strings.SplitN(base, "_-_", 3)
	artist, title, trackNum = splitArtistParts(parts)
	if artist != "" {
		title = stripArtistPrefix(title, artist)
	}
	return artist, title, trackNum
}

// splitArtistParts examines split parts to determine artist/title/trackNum.
// Parts is the result of splitting a filename base on a delimiter.
func splitArtistParts(parts []string) (artist, title string, trackNum int) {
	if len(parts) < 2 {
		return "", parts[0], 0
	}

	first := strings.TrimSpace(parts[0])

	// Track-number-aware: if first part is digits, it's a track number.
	if n, err := strconv.Atoi(first); err == nil {
		trackNum = n
		if len(parts) >= 3 {
			// "08 - Artist - Title" → artist=parts[1], title=parts[2..]
			artist = strings.TrimSpace(parts[1])
			title = strings.TrimSpace(strings.Join(parts[2:], " - "))
		} else {
			// "08 - Title" → no artist
			title = strings.TrimSpace(strings.Join(parts[1:], " - "))
		}
		return artist, title, trackNum
	}

	// Non-numeric first part → likely artist name.
	if len(first) > 0 && len(first) < 80 {
		artist = first
	}
	title = strings.TrimSpace(strings.Join(parts[1:], " - "))

	// Also extract track number from the title portion.
	if m := TrackNumRE.FindStringSubmatch(title); m != nil {
		trackNum, _ = strconv.Atoi(m[1])
		title = m[2]
	}
	return artist, title, trackNum
}

// stripArtistPrefix removes the artist prefix from a title if present.
// E.g. title="Haddaway - What Is Love" with artist="Haddaway" → "What Is Love".
func stripArtistPrefix(title, artist string) string {
	for _, sep := range []string{" - ", "_-_"} {
		prefix := strings.ToLower(artist + sep)
		if strings.HasPrefix(strings.ToLower(title), prefix) {
			return strings.TrimSpace(title[len(prefix):])
		}
	}
	return title
}

// ParseAlbumDir extracts artist, album title, and year from a directory path segment.
// Patterns handled:
//
//	"Artist - Album (2024)"  → artist="Artist", album="Album", year="2024"
//	"Artist/Album"           → artist="Artist", album="Album"
//	"Artist - Album"         → artist="Artist", album="Album"
//	"Album (2024)"           → artist="", album="Album", year="2024"
func ParseAlbumDir(dirPath string) (artist, album, year string) {
	// Use the last directory segment (the deepest album folder).
	seg := filepath.Base(dirPath)

	// Extract year: (YYYY) or [YYYY].
	yearRE := regexp.MustCompile(`[\[\(](\d{4})[\]\)]`)
	if m := yearRE.FindStringSubmatch(seg); m != nil {
		year = m[1]
		seg = yearRE.ReplaceAllString(seg, "")
	}

	// Try "Artist - Album" split.
	if idx := strings.Index(seg, " - "); idx > 0 {
		artist = strings.TrimSpace(seg[:idx])
		album = strings.TrimSpace(seg[idx+3:])
		return artist, album, year
	}

	// Fallback: just the segment as album title.
	album = strings.TrimSpace(seg)
	return "", album, year
}
