// Package library provides file scanning and library import.
package library

import (
	"bytes"
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
	Picture  *tag.Picture
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
		Picture:  m.Picture(),
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

// CoverCandidates are common cover image filenames that mark an album
// directory as already having artwork on disk. Shared with the HTTP layer so
// the extractor and the cover server agree on what counts as a cover.
var CoverCandidates = []string{
	"cover.jpg", "cover.jpeg", "cover.png", "cover.webp", "cover.gif",
	"folder.jpg", "folder.jpeg", "folder.png", "folder.webp",
	"front.jpg", "front.png",
}

// CoverFilePath returns the path of the first existing cover image in dir, or
// "" when none of the known cover names exist.
func CoverFilePath(dir string) string {
	for _, name := range CoverCandidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// HasCoverFile reports whether dir already contains a cover image.
func HasCoverFile(dir string) bool {
	return CoverFilePath(dir) != ""
}

// artistImageNames are the local artist portrait filenames.
var artistImageNames = []string{
	"artist.jpg", "artist.jpeg", "artist.png", "artist.webp", "artist.gif",
}

// hasArtistImage reports whether the artist directory for a track already
// contains a local artist image in any format.
func hasArtistImage(trackPath string) bool {
	artistDir := ArtistDirFromTrack(trackPath)
	for _, name := range artistImageNames {
		if _, err := os.Stat(filepath.Join(artistDir, name)); err == nil {
			return true
		}
	}
	return false
}

// coverExt maps an embedded picture to a safe cover file extension.
func coverExt(p *tag.Picture) string {
	// Prefer the MIME type (the authoritative field embedded in the tag), then
	// sniff the actual bytes, then the extension. The extension can lie — tag
	// parsers often default it to "jpg" even when the embedded data is PNG.
	switch strings.ToLower(p.MIMEType) {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	}
	if ext := sniffImageExt(p.Data); ext != "" {
		return ext
	}
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p.Ext), ".")) {
	case "jpg", "png", "webp", "gif":
		return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p.Ext), "."))
	case "jpeg":
		return "jpg"
	}
	return "jpg"
}

// sniffImageExt detects an image format from its magic bytes, falling back to
// "jpg". Used when a tag picture carries no usable extension or MIME type so a
// PNG isn't written to a .jpg file.
func sniffImageExt(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "jpg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	case len(data) >= 6 && (bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))):
		return "gif"
	}
	return "jpg"
}

// discDirRE matches album subfolders that hold a disc, e.g. "Disc 1", "CD2",
// "disc-01". Tracks inside these sit one level deeper than {artist}/{album}.
var discDirRE = regexp.MustCompile(`(?i)^(disc|disk|cd|dvd)\s*-?\s*\d+$`)

// AlbumDirFromTrack resolves the album directory for a track file path. For
// the standard {library}/{artist}/{album}/track layout this is the track's
// parent; for multi-disc layouts ({album}/Disc N/track) it is the disc
// folder's parent. Returns "" for a path with no parent.
func AlbumDirFromTrack(trackPath string) string {
	dir := filepath.Dir(trackPath)
	if isDiscDir(dir) {
		return filepath.Dir(dir)
	}
	return dir
}

// ArtistDirFromTrack resolves the artist directory for a track file path —
// the parent of the album directory.
func ArtistDirFromTrack(trackPath string) string {
	return filepath.Dir(AlbumDirFromTrack(trackPath))
}

// isDiscDir reports whether dir looks like a disc subfolder of an album. A
// matching name alone isn't enough — an album literally named "Disc 1" or
// "CD2" would otherwise be misclassified as a disc folder — so the parent must
// also contain a sibling disc-named directory. Reads the parent directory;
// only called for disc-named folders.
func isDiscDir(dir string) bool {
	if !discDirRE.MatchString(filepath.Base(dir)) {
		return false
	}
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return false
	}
	base := filepath.Base(dir)
	for _, e := range entries {
		if e.IsDir() && e.Name() != base && discDirRE.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

// writeAlbumCover extracts an embedded picture to the album and artist
// directories and records the artist thumbnail in the library store. It is
// best-effort — failures are logged and never fail the scan.
//
// libraryRoot guards both writes: artwork is never written into the scan root
// itself (tracks loose in the root, or an album that resolves to the root),
// and the artist image only runs when the artist directory is nested below
// the scan root.
func (s *Scanner) writeAlbumCover(ctx context.Context, trackPath, libraryRoot, artistName string, p *tag.Picture) {
	rootClean := filepath.Clean(libraryRoot)
	albumDir := AlbumDirFromTrack(trackPath)
	if albumDir == rootClean {
		return // never write artwork into the library root
	}

	ext := coverExt(p)

	// Write the cover only when no cover image of any format exists yet — an
	// album that already has folder.jpg/cover.png doesn't get a redundant one.
	// The artist image below is still extracted regardless.
	if !HasCoverFile(albumDir) {
		coverPath := filepath.Join(albumDir, "cover."+ext)
		if err := os.WriteFile(coverPath, p.Data, 0o644); err != nil {
			s.log.Warn("write cover failed", "path", coverPath, "error", err, "component", "scanner")
		} else {
			s.log.Info("extracted cover art", "path", coverPath, "component", "scanner")
		}
	}

	// Artist image lives one level up from the album directory, but only in a
	// nested {artist}/{album} layout — never the scan root itself. Written only
	// when no portrait exists in any format, so a sibling track with a
	// different image format doesn't add or overwrite a second portrait.
	artistDir := ArtistDirFromTrack(trackPath)
	if artistDir == rootClean {
		return
	}
	if !hasArtistImage(trackPath) {
		artistPath := filepath.Join(artistDir, "artist."+ext)
		if err := os.WriteFile(artistPath, p.Data, 0o644); err != nil {
			s.log.Warn("write artist image failed", "path", artistPath, "error", err, "component", "scanner")
		} else {
			s.log.Info("extracted artist image", "path", artistPath, "component", "scanner")
		}
	}

	if artistName == "" {
		return
	}
	// Compilations group other artists' tracks under a directory whose name
	// doesn't match the embedded artist (e.g. "Various Artists"). Recording
	// the image as that artist's thumbnail would point at a file outside
	// their own directory — skip the thumb write (the image file itself is
	// still useful as the directory's portrait).
	if !strings.EqualFold(filepath.Base(artistDir), artistName) {
		return
	}
	artist, err := s.store.GetArtistByName(ctx, artistName)
	if err != nil || artist == nil {
		return
	}
	// Only record a local thumbnail when the artist has none yet, or already
	// points at a local artist.* image — never clobber a remote URL.
	if artist.ThumbURL == "" || strings.HasPrefix(artist.ThumbURL, "artist.") {
		_ = s.store.SetArtistThumbURL(ctx, artist.ID, "artist."+ext)
	}
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

// CountAudioFiles returns the number of audio files under root. Used to
// estimate scan progress ahead of time. The walk aborts when ctx is cancelled.
func CountAudioFiles(ctx context.Context, root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if audioExtensions[strings.ToLower(filepath.Ext(path))] {
			count++
		}
		return nil
	})
	return count, err
}

// ScanPath walks a directory tree and imports any new audio files.
func (s *Scanner) ScanPath(ctx context.Context, root string) (ScanStats, error) {
	return s.ScanPathWithProgress(ctx, root, nil)
}

// ScanPathWithProgress walks a directory tree and imports any new audio files,
// invoking onProgress with each audio file path it examines. The walk stops
// early when ctx is cancelled.
func (s *Scanner) ScanPathWithProgress(ctx context.Context, root string, onProgress func(path string)) (ScanStats, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}

	var stats ScanStats
	// coverAttempts bounds how many audio files per album directory are probed
	// for embedded artwork in a single run. The first file may lack a picture
	// while a sibling has one (e.g. art embedded only in track 1 but lexical
	// order makes another file first), so we try a few files before giving up.
	// Once a cover lands on disk (or the cap is hit) extraction stops.
	coverAttempts := make(map[string]int)
	const maxCoverAttempts = 4
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !audioExtensions[ext] {
			return nil
		}

		stats.Scanned++
		if onProgress != nil {
			onProgress(path)
		}
		// Resolve the album directory so multi-disc albums ({album}/Disc N)
		// share one cover/artist entry and one attempt budget.
		albumDir := AlbumDirFromTrack(path)

		needCover := coverAttempts[albumDir] < maxCoverAttempts
		if needCover {
			coverAttempts[albumDir]++
		}

		// Check if already imported (use absolute path).
		existing, dbErr := s.store.GetTrackByFilePath(ctx, path)
		if dbErr != nil {
			s.log.Error("GetTrackByFilePath failed", "path", path, "error", dbErr, "component", "scanner")
			stats.Errors++
			return nil
		}
		if existing != nil {
			stats.Skipped++
			// Backfill embedded artwork for albums that were scanned before
			// cover extraction existed. Runs when either the cover or the
			// artist image is missing — albums that already carried a
			// cover.jpg/folder.jpg on disk would otherwise skip extraction
			// entirely and never get an artist image.
			if needCover && (!HasCoverFile(albumDir) || !hasArtistImage(path)) {
				s.backfillCover(ctx, path, absRoot)
			}
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

		// Extract embedded artwork for newly imported albums.
		if needCover && tags != nil && tags.Picture != nil {
			s.writeAlbumCover(ctx, path, absRoot, artistName, tags.Picture)
		}

		stats.Imported++
		return nil
	})
	return stats, err
}

// backfillCover extracts embedded artwork for an already-imported album whose
// directory has no cover file on disk.
func (s *Scanner) backfillCover(ctx context.Context, path, libraryRoot string) {
	tags, err := readFileTags(path)
	if err != nil || tags == nil || tags.Picture == nil {
		return
	}
	s.writeAlbumCover(ctx, path, libraryRoot, tags.Artist, tags.Picture)
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
