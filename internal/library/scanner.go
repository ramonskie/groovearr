// Package library provides file scanning and library import.
package library

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dhowden/tag"
	"github.com/ramonskie/groovearr/internal/domain"
)

// TrackNumRE matches leading track numbers like "01 - Title" or "1. Title".
var TrackNumRE = regexp.MustCompile(`^(\d{1,3})[\.\s\-]+(.+)$`)

// Scanner walks directories and imports audio files into the library store.
type Scanner struct {
	store Store
	log   *slog.Logger
}

// ScanStats tracks the outcome of a library scan.
type ScanStats struct {
	Scanned  int // total audio files found
	Imported int // new tracks imported
	Skipped  int // duplicates (already in library)
	Errors   int // files that failed to import
}

// NewScanner creates a library scanner.
func NewScanner(store Store, logger *slog.Logger) *Scanner {
	return &Scanner{store: store, log: logger}
}

// tagMeta holds metadata extracted from audio file tags.
type tagMeta struct {
	Artist   string
	Album    string
	Title    string
	Year     int
	TrackNum int
	DiscNum  int
	Genre    string
}

// readFileTags attempts to read audio metadata from a file using ID3/FLAC/Vorbis tags.
// Returns nil, nil if no tags are found or the file isn't recognized audio (caller falls
// back to path parsing). Only returns an error for filesystem-level failures.
func readFileTags(path string) (*tagMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		// Any parse error means no usable tags — fall back to path parsing.
		return nil, nil
	}

	trackNum, _ := m.Track()
	discNum, _ := m.Disc()

	meta := &tagMeta{
		Artist:   strings.TrimSpace(m.Artist()),
		Album:    strings.TrimSpace(m.Album()),
		Title:    strings.TrimSpace(m.Title()),
		Year:     m.Year(),
		TrackNum: trackNum,
		DiscNum:  discNum,
		Genre:    strings.TrimSpace(m.Genre()),
	}

	// If no structured artist, try AlbumArtist.
	if meta.Artist == "" {
		meta.Artist = strings.TrimSpace(m.AlbumArtist())
	}

	// If everything is empty, treat as no tags.
	if meta.Artist == "" && meta.Album == "" && meta.Title == "" {
		return nil, nil
	}

	return meta, nil
}

// TagMeta holds metadata extracted from audio file tags (exported version of tagMeta).
type TagMeta struct {
	Artist   string
	Album    string
	Title    string
	Year     int
	TrackNum int
	DiscNum  int
	Genre    string
}

// ReadTags reads basic artist/title/album metadata from an audio file's ID3/FLAC tags.
// Returns nil if the file has no usable tags.
func ReadTags(path string) (*TagMeta, error) {
	tm, err := readFileTags(path)
	if err != nil || tm == nil {
		return nil, err
	}
	return &TagMeta{
		Artist:   tm.Artist,
		Album:    tm.Album,
		Title:    tm.Title,
		Year:     tm.Year,
		TrackNum: tm.TrackNum,
		DiscNum:  tm.DiscNum,
		Genre:    tm.Genre,
	}, nil
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
		existing, dbErr := s.store.GetTrackByFilePath(ctx, path)
		if dbErr != nil {
			s.log.Error("GetTrackByFilePath failed", "path", path, "error", dbErr, "component", "scanner")
			stats.Errors++
			return nil
		}
		if existing != nil {
			stats.Skipped++
			return nil
		}

		// Try reading audio tags first; fall back to path parsing if none found.
		relPath, _ := filepath.Rel(absRoot, path)
		tags, err := readFileTags(path)
		if err != nil {
			s.log.Warn("tag read failed", "path", path, "error", err, "component", "scanner")
			stats.Errors++
			return nil
		}

		var (
			trackTitle  string
			artistName  string
			albumTitle  string
			albumYear   int
			trackNumber int
			discNumber  int
			genres      []string
		)

		if tags != nil {
			trackTitle = tags.Title
			artistName = tags.Artist
			albumTitle = tags.Album
			albumYear = tags.Year
			trackNumber = tags.TrackNum
			discNumber = tags.DiscNum
			if tags.Genre != "" {
				genres = splitGenres(tags.Genre)
			}
		} else {
			artistName, albumTitle, trackTitle = ParseFileMetadata(relPath)
		}

		// Get file info.
		fi, err := d.Info()
		if err != nil {
			stats.Errors++
			return nil
		}

		// Import track via shared pipeline.
		trackID, err := s.store.ImportTrack(ctx, &domain.Track{
			Title:       trackTitle,
			TrackNumber: trackNumber,
			DiscNumber:  discNumber,
			FilePath:    path,
			FileSize:    fi.Size(),
		}, artistName, albumTitle, albumYear, genres)
		if err != nil {
			s.log.Error("import track failed", "path", path, "error", err, "component", "scanner")
			stats.Errors++
			return nil
		}
		_ = trackID

		stats.Imported++
		return nil
	})
	return stats, err
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

// splitGenres splits a genre tag string on common separators (;, ,, /).
func splitGenres(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ';' || r == ',' || r == '/'
	})
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
