package library

import (
	"fmt"
	"os"
	"path/filepath"
)

// PlaylistRenamer moves downloaded playlist tracks into the playlist directory
// using a configurable template (e.g., "{position:02d} {artist} - {title}").
type PlaylistRenamer struct {
	resolver *PathResolver
	root     string
}

// NewPlaylistRenamer creates a renamer for playlist track organization.
func NewPlaylistRenamer(template, root string) *PlaylistRenamer {
	if template == "" {
		template = "{position:02d} {artist} - {title}"
	}
	return &PlaylistRenamer{
		resolver: NewPathResolver(template),
		root:     root,
	}
}

// Rename moves a file to the playlist directory organized by template.
func (r *PlaylistRenamer) Rename(filePath string, position int, artist, title, ext string) (string, error) {
	targetPath := r.ResolvePath(position, artist, title, ext)
	if targetPath == "" || filepath.Clean(filePath) == filepath.Clean(targetPath) {
		return filePath, nil
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return filePath, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Rename(filePath, targetPath); err != nil {
		return filePath, fmt.Errorf("rename %s → %s: %w", filePath, targetPath, err)
	}
	return targetPath, nil
}

// ResolvePath computes the target path for a playlist track without moving any files.
func (r *PlaylistRenamer) ResolvePath(position int, artist, title, ext string) string {
	resolved := r.resolver.Resolve(ResolveArgs{
		Artist:    artist,
		Title:     title,
		TrackNum:  position,
		Ext:       ext,
		AlbumType: "Playlist",
	})
	if resolved == "" {
		return ""
	}
	return filepath.Join(r.root, resolved+"."+ext)
}
