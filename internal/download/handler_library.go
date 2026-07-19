package download

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// LibraryImporterHandler creates artist, album, and track records in the
// library store for a single downloaded file, similar to how the scanner
// handles discovered audio files.
type LibraryImporterHandler struct {
	libStore library.Store
}

// NewLibraryImporterHandler creates a handler that imports downloaded files
// into the music library by creating/updating artist, album, and track records.
func NewLibraryImporterHandler(libStore library.Store) *LibraryImporterHandler {
	return &LibraryImporterHandler{libStore: libStore}
}

// Handle creates artist/album/track records for the downloaded file.
// Metadata is extracted from the file path (post-renamer) and audio tags.
func (h *LibraryImporterHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" {
		return fmt.Errorf("library importer: no file path for %s", record.ID)
	}

	// Check if already imported by file path (renamer always produces absolute paths).
	existing, err := h.libStore.GetTrackByFilePath(ctx, record.FilePath)
	if err == nil && existing != nil {
		record.LibraryTrackID = existing.ID
		return nil
	}

	trackTitle, artistName, albumTitle := h.extractMetadata(record)

	// Get or create artist.
	artistID, err := h.upsertArtist(ctx, artistName)
	if err != nil {
		return fmt.Errorf("library importer: artist: %w", err)
	}

	// Get or create album.
	albumID, err := h.upsertAlbum(ctx, artistID, albumTitle, record.Year)
	if err != nil {
		return fmt.Errorf("library importer: album: %w", err)
	}

	// Get file info for size.
	fi, err := os.Stat(record.FilePath)
	if err != nil {
		return fmt.Errorf("library importer: stat file: %w", err)
	}

	// Create track record (renamer guarantees absolute file paths).
	track := &domain.Track{
		AlbumID:     albumID,
		ArtistID:    artistID,
		Title:       trackTitle,
		TrackNumber: record.TrackNumber,
		DiscNumber:  record.DiscNumber,
		FilePath:    record.FilePath,
		FileSize:    fi.Size(),
	}

	// Copy external IDs from record to track.
	if record.TrackID != "" {
		switch record.SourceName {
		case "deezer":
			track.DeezerID = record.TrackID
		case "spotify":
			track.SpotifyID = record.TrackID
		case "tidal":
			track.TidalID = record.TrackID
		}
	}

	trackID, err := h.libStore.UpsertTrack(ctx, track)
	if err != nil {
		return fmt.Errorf("library importer: upsert track: %w", err)
	}

	record.LibraryTrackID = trackID
	return nil
}

// extractMetadata pulls artist/album/title from the record and file path.
func (h *LibraryImporterHandler) extractMetadata(record *domain.DownloadRecord) (title, artist, album string) {
	// Use record metadata if available.
	artist = record.Artist
	album = record.Album
	title = record.Title

	// Fall back to file path parsing using the full path so directory
	// structure (Artist/Album/Track) is preserved.
	if artist == "" || album == "" || title == "" {
		title, artist, album = parsePath(record.FilePath)
	}

	if artist == "" {
		artist = "Unknown Artist"
	}
	if album == "" {
		album = "Unknown Album"
	}
	if title == "" {
		title = filepath.Base(record.FilePath)
	}

	return title, artist, album
}

// upsertArtist finds or creates an artist record.
func (h *LibraryImporterHandler) upsertArtist(ctx context.Context, name string) (int64, error) {
	existing, err := h.libStore.GetArtistByName(ctx, name)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}

	id, err := h.libStore.UpsertArtist(ctx, &domain.Artist{Name: name})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// upsertAlbum finds or creates an album record for the given artist.
func (h *LibraryImporterHandler) upsertAlbum(ctx context.Context, artistID int64, title string, year int) (int64, error) {
	albums, err := h.libStore.SearchAlbums(ctx, title, 1)
	if err != nil {
		return 0, err
	}

	for _, al := range albums {
		if strings.EqualFold(al.Title, title) && al.ArtistID == artistID {
			if al.Year == 0 && year != 0 {
				al.Year = year
				if _, err := h.libStore.UpsertAlbum(ctx, &al); err != nil {
					return 0, err
				}
			}
			return al.ID, nil
		}
	}

	album := &domain.Album{
		ArtistID:  artistID,
		Title:     title,
		Year:      year,
		AlbumType: domain.AlbumTypeAlbum,
	}
	id, err := h.libStore.UpsertAlbum(ctx, album)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// parsePath extracts artist and title from a filename path.
func parsePath(path string) (track, artist, album string) {
	dir := filepath.Dir(path)
	filename := filepath.Base(path)
	title := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Strip track number prefix.
	if m := regexp.MustCompile(`^(\d{1,3})[\.\s\-]+(.+)$`).FindStringSubmatch(title); m != nil {
		title = m[2]
	}

	parts := splitPath(dir)

	// Filter out disc directories.
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
		track = parts[2]
	case len(parts) == 2:
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
