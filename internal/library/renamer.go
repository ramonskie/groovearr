// Package library provides file scanning and path resolution.
package library

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
)

// Renamer moves downloaded files into the library using a PathResolver template.
type Renamer struct {
	resolver *PathResolver
	root     string // absolute base directory for resolved paths
}

// NewRenamer creates a Renamer with a folder template and root directory.
func NewRenamer(template, root string) *Renamer {
	return &Renamer{
		resolver: NewPathResolver(template),
		root:     root,
	}
}

// RenameOrganized satisfies the deezer.FileRenamer interface for post-download organization.
// It accepts individual metadata fields instead of a struct to avoid cross-package coupling.
func (r *Renamer) RenameOrganized(filePath string, artist, album, title string, trackNum, discNum, year int) (string, error) {
	return r.Rename(filePath, FileMeta{
		Artist:   artist,
		Album:    album,
		Title:    title,
		TrackNum: trackNum,
		DiscNum:  discNum,
		Year:     year,
	})
}

// FileMeta holds structured metadata for renaming.
type FileMeta struct {
	Artist   string
	Album    string
	Title    string
	Year     int
	TrackNum int
	DiscNum  int
}

// Rename moves filePath to a computed path under the configured root using the template.
// Returns the new absolute path, or the original path if renaming is skipped (e.g., missing metadata).
func (r *Renamer) Rename(filePath string, meta FileMeta) (string, error) {
	ext := strings.TrimPrefix(filepath.Ext(filePath), ".")

	// Build resolve args: use provided metadata, fall back to ID3 tags, then filename parsing.
	artist := meta.Artist
	album := meta.Album
	title := meta.Title

	if artist == "" || album == "" || title == "" {
		// Try reading embedded ID3/FLAC tags to fill missing fields.
		if tags, tagErr := readFileTags(filePath); tagErr == nil && tags != nil {
			if artist == "" {
				artist = tags.Artist
			}
			if album == "" {
				album = tags.Album
			}
			if title == "" {
				title = tags.Title
			}
			if meta.Year == 0 {
				meta.Year = tags.Year
			}
			if meta.TrackNum == 0 {
				meta.TrackNum = tags.TrackNum
			}
			if meta.DiscNum == 0 {
				meta.DiscNum = tags.DiscNum
			}
		}
	}
	if artist == "" {
		artist, album, title = ParseFlatFilename(filepath.Base(filePath))
	}
	if artist == "" {
		return filePath, nil
	}

	albumType := "Album"

	resolved := r.resolver.Resolve(ResolveArgs{
		Artist:    artist,
		Album:     album,
		Year:      meta.Year,
		TrackNum:  meta.TrackNum,
		Title:     title,
		Ext:       ext,
		DiscNum:   meta.DiscNum,
		AlbumType: albumType,
	})

	if resolved == "" {
		return filePath, nil
	}

	targetPath := filepath.Join(r.root, resolved+"."+ext)

	// Normalize to absolute path so downstream consumers (scanner, store,
	// GetTrackByFilePath) all compare against the same canonical form.
	if abs, err := filepath.Abs(targetPath); err == nil {
		targetPath = abs
	}

	// If source and target are the same, nothing to do.
	if filepath.Clean(filePath) == filepath.Clean(targetPath) {
		return filePath, nil
	}

	// Ensure the target directory exists.
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return filePath, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Move the file. Falls back to copy+delete on cross-device errors (e.g. Docker volumes).
	if err := os.Rename(filePath, targetPath); err != nil {
		if strings.Contains(err.Error(), "cross-device") {
			// Copy + delete for cross-filesystem moves (Docker volumes).
			if copyErr := copyFile(filePath, targetPath); copyErr != nil {
				return filePath, fmt.Errorf("copy %s → %s: %w", filePath, targetPath, copyErr)
			}
			os.Remove(filePath)
		} else {
			return filePath, fmt.Errorf("rename %s → %s: %w", filePath, targetPath, err)
		}
	}

	return targetPath, nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()

	if _, err := io.Copy(d, s); err != nil {
		return err
	}
	return d.Sync()
}

// ScanMetadata extracts metadata from a domain.Track and its linked Artist/Album.
func ScanMetadata(track *domain.Track, artist *domain.Artist, album *domain.Album) FileMeta {
	meta := FileMeta{
		Title:    track.Title,
		TrackNum: track.TrackNumber,
		DiscNum:  track.DiscNumber,
	}
	if artist != nil {
		meta.Artist = artist.Name
	}
	if album != nil {
		meta.Album = album.Title
		meta.Year = album.Year
	}
	return meta
}
