// Package library provides file scanning and library import.
package library

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
)

// Scanner walks directories and imports audio files into the library store.
type Scanner struct {
	store Store
}

// ScanStats tracks the outcome of a library scan.
type ScanStats struct {
	Scanned  int // total audio files found
	Imported int // new tracks imported
	Skipped  int // duplicates (already in library)
	Errors   int // files that failed to import
}

// NewScanner creates a library scanner.
func NewScanner(store Store) *Scanner {
	return &Scanner{store: store}
}

// audioExtensions are file extensions recognized as audio.
var audioExtensions = map[string]bool{
	".mp3": true, ".flac": true, ".ogg": true, ".oga": true,
	".opus": true, ".m4a": true, ".mp4": true, ".aac": true,
	".wma": true, ".wav": true,
}

// ScanPath walks a directory tree and imports any new audio files.
func (s *Scanner) ScanPath(ctx context.Context, root string) (ScanStats, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}

	var stats ScanStats
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !audioExtensions[ext] {
			return nil
		}

		stats.Scanned++

		// Check if already imported (use absolute path).
		existing, _ := s.store.GetTrackByFilePath(ctx, path)
		if existing != nil {
			stats.Skipped++
			return nil
		}

		// Parse metadata from path relative to scan root.
		relPath, _ := filepath.Rel(absRoot, path)
		track, artist, album := parsePath(relPath)

		// Get or create artist.
		var artistID int64
		existingArtist, _ := s.store.GetArtistByName(ctx, artist)
		if existingArtist != nil {
			artistID = existingArtist.ID
		} else {
			a := &domain.Artist{Name: artist}
			id, err := s.store.UpsertArtist(ctx, a)
			if err != nil {
				log.Printf("scanner: upsert artist %q: %v", artist, err)
				stats.Errors++
				return nil
			}
			artistID = id
		}

		// Get or create album.
		var albumID int64
		albums, _ := s.store.SearchAlbums(ctx, album, 1)
		found := false
		for _, al := range albums {
			if strings.EqualFold(al.Title, album) && al.ArtistID == artistID {
				albumID = al.ID
				found = true
				break
			}
		}
		if !found {
			a := &domain.Album{
				ArtistID:  artistID,
				Title:     album,
				AlbumType: domain.AlbumTypeAlbum,
			}
			id, err := s.store.UpsertAlbum(ctx, a)
			if err != nil {
				log.Printf("scanner: upsert album %q: %v", album, err)
				stats.Errors++
				return nil
			}
			albumID = id
		}

		// Get file info.
		fi, err := d.Info()
		if err != nil {
			stats.Errors++
			return nil
		}

		// Create track record.
		t := &domain.Track{
			AlbumID:     albumID,
			ArtistID:    artistID,
			Title:       track,
			FilePath:    path,
			FileSize:    fi.Size(),
		}

		if _, err := s.store.UpsertTrack(ctx, t); err != nil {
			log.Printf("scanner: upsert track %q: %v", track, err)
			stats.Errors++
			return nil
		}

		stats.Imported++
		return nil
	})
	return stats, err
}

// parsePath extracts artist, album, and track title from a file path.
// Handles common patterns:
//
//	Artist/Album/TrackNumber - Title.ext
//	Artist - Album/TrackNumber - Title.ext
//	Artist/Album (Year)/TrackNumber Title.ext
//
// Flat filenames (no directory structure) fall back to parsing " - " separators:
//
//	Artist - Title.ext          → artist=Artist, album=Unknown Album, track=Title
//	Artist - Album - Title.ext  → artist=Artist, album=Album, track=Title
func parsePath(path string) (track, artist, album string) {
	dir := filepath.Dir(path)
	filename := filepath.Base(path)
	title := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Try to extract track number prefix.
	trackNumPattern := regexp.MustCompile(`^(\d{1,3})[\.\s\-]+(.+)$`)
	if m := trackNumPattern.FindStringSubmatch(title); m != nil {
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
		// Artist/Album/Track or Artist - Album/Track
		artist = parts[0]
		// Remove year from album.
		album = regexp.MustCompile(`\s*\(\d{4}\)\s*$`).ReplaceAllString(parts[1], "")
		album = strings.TrimSpace(album)
		track = parts[2]
	case len(parts) == 2:
		// Could be Artist - Album or Artist/Track
		if strings.Contains(parts[0], " - ") {
			ap := strings.SplitN(parts[0], " - ", 2)
			artist = ap[0]
			album = ap[1]
		} else {
			artist = parts[0]
			album = "Unknown Album"
		}
		track = parts[1]
	default:
		// Flat file: try "Artist - Title" or "Artist - Album - Title" patterns.
		if strings.Contains(title, " - ") {
			flatParts := strings.SplitN(title, " - ", 3)
			switch len(flatParts) {
			case 3:
				artist = strings.TrimSpace(flatParts[0])
				album = strings.TrimSpace(flatParts[1])
				track = strings.TrimSpace(flatParts[2])
			case 2:
				artist = strings.TrimSpace(flatParts[0])
				album = "Unknown Album"
				track = strings.TrimSpace(flatParts[1])
			}
		} else {
			artist = "Unknown Artist"
			album = "Unknown Album"
			track = parts[0]
		}
	}

	return track, artist, album
}

func splitPath(p string) []string {
	// Normalize separators.
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

// FormatHumanSize returns a human-readable file size.
func FormatHumanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// FormatDuration formats milliseconds as m:ss.
func FormatDuration(ms int64) string {
	sec := ms / 1000
	m := sec / 60
	s := sec % 60
	if m > 0 {
		return fmt.Sprintf("%d:%02d", m, s)
	}
	return fmt.Sprintf("0:%02d", s)
}

// ParseTrackNumber extracts a track number from a filename.
func ParseTrackNumber(filename string) int {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	re := regexp.MustCompile(`^(\d{1,3})`)
	if m := re.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}
