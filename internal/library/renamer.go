// Package library provides file scanning and path resolution.
package library

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
)

// Renamer moves downloaded files into the library using a PathResolver template.
// Compilation tracks (VA albums) are routed through the compilation template so
// every flow — download, import, organize — produces the same paths.
type Renamer struct {
	resolver            *PathResolver
	compilationResolver *PathResolver // VA albums; falls back to resolver when nil
	root                string        // absolute base directory for resolved paths
	log                 *slog.Logger
}

// NewRenamer creates a Renamer with a folder template and root directory.
// Compilation tracks fall back to the folder template.
func NewRenamer(template, root string, logger *slog.Logger) *Renamer {
	return newRenamer(template, "", root, logger)
}

// NewRenamerWithCompilation creates a Renamer that additionally routes
// compilation tracks through the compilation template.
func NewRenamerWithCompilation(template, compilationTemplate, root string, logger *slog.Logger) *Renamer {
	return newRenamer(template, compilationTemplate, root, logger)
}

func newRenamer(template, compilationTemplate, root string, logger *slog.Logger) *Renamer {
	if logger == nil {
		logger = slog.Default()
	}
	var compilation *PathResolver
	if compilationTemplate != "" {
		compilation = NewPathResolver(compilationTemplate)
	}
	return &Renamer{
		resolver:            NewPathResolver(template),
		compilationResolver: compilation,
		root:                root,
		log:                 logger,
	}
}

// resolverFor returns the template matching the compilation flag — the single
// source of truth for how every flow maps a VA album to a directory layout.
func (r *Renamer) resolverFor(compilation bool) *PathResolver {
	if compilation && r.compilationResolver != nil {
		return r.compilationResolver
	}
	return r.resolver
}

// RenameOrganized renames a downloaded file using individual metadata fields
// instead of a struct to avoid cross-package coupling.
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

// Rename moves filePath to a computed path under the configured root using the
// folder template. Returns the new absolute path, or the original path if
// renaming is skipped (e.g., missing metadata).
func (r *Renamer) Rename(filePath string, meta FileMeta) (string, error) {
	return r.RenameFor(filePath, meta, false)
}

// RenameFor moves filePath using the folder template, or the compilation
// template for VA albums. Both the download renamer handler and the library
// organizer go through this so every flow produces identical paths.
func (r *Renamer) RenameFor(filePath string, meta FileMeta, compilation bool) (string, error) {
	targetPath := r.target(filePath, meta, compilation)
	if targetPath == "" {
		return filePath, nil
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
			if copyErr := r.copyFile(filePath, targetPath); copyErr != nil {
				return filePath, fmt.Errorf("copy %s → %s: %w", filePath, targetPath, copyErr)
			}
			os.Remove(filePath)
		} else {
			return filePath, fmt.Errorf("rename %s → %s: %w", filePath, targetPath, err)
		}
	}

	return targetPath, nil
}

// target computes the absolute destination path for filePath under the
// configured root using the template matching the compilation flag, filling
// missing metadata from embedded tags. Returns "" when the file cannot be
// organized (no artist name). It does not touch the filesystem.
func (r *Renamer) target(filePath string, meta FileMeta, compilation bool) string {
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
		return ""
	}

	albumType := "Album"

	resolved := r.resolverFor(compilation).Resolve(ResolveArgs{
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
		return ""
	}

	targetPath := filepath.Join(r.root, resolved+"."+ext)

	// Normalize to absolute path so downstream consumers (scanner, store,
	// GetTrackByFilePath) all compare against the same canonical form.
	if abs, err := filepath.Abs(targetPath); err == nil {
		targetPath = abs
	}
	return targetPath
}

// copyFile copies a file from src to dst.
func (r *Renamer) copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		r.log.Error("open src failed", "src", src, "error", err, "component", "renamer")
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		r.log.Error("create dst failed", "dst", dst, "error", err, "component", "renamer")
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
