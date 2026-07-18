// Package library provides file scanning, path resolution, and post-download hooks.
package library

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
)

// NewCoverHook creates a post-download hook that fetches album cover art from the
// download record's CoverURL. The image is cached as cover.jpg in the album directory
// and the album's thumb_url is updated in the store.
//
// The hook is source-agnostic — any download plugin can populate CoverURL and this
// hook will handle it the same way. If CoverURL is empty, the hook is a no-op.
func NewCoverHook(store Store) func(ctx context.Context, record domain.DownloadRecord) (string, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}

	return func(ctx context.Context, record domain.DownloadRecord) (string, error) {
		if record.CoverURL == "" || record.FilePath == "" {
			return record.FilePath, nil
		}

		albumDir := filepath.Dir(record.FilePath)
		coverPath := filepath.Join(albumDir, "cover.jpg")

		// Skip if cover already cached.
		if _, err := os.Stat(coverPath); err == nil {
			return record.FilePath, nil
		}

		// Download the cover image.
		if err := downloadCover(ctx, httpClient, record.CoverURL, coverPath); err != nil {
			log.Printf("cover hook: download failed for %s: %v", record.Filename, err)
			return record.FilePath, nil // non-fatal — don't break the download chain
		}

		// Update the album thumb_url in the store (best-effort).
		if err := updateAlbumThumb(ctx, store, albumDir); err != nil {
			log.Printf("cover hook: db update failed for %s: %v", record.Filename, err)
		}

		return record.FilePath, nil
	}
}

// downloadCover fetches an image from url and writes it to dst.
func downloadCover(ctx context.Context, client *http.Client, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// updateAlbumThumb finds the album in the store that matches the given directory
// and updates its thumb_url to the local cover.jpg path.
func updateAlbumThumb(ctx context.Context, store Store, albumDir string) error {
	// Extract artist and album from the directory name.
	// Directory structure: {root}/{artist}/{album} ({year})
	artistName := filepath.Base(filepath.Dir(albumDir))
	albumTitle := extractAlbumTitle(filepath.Base(albumDir))

	if artistName == "" || albumTitle == "" {
		return nil // can't determine metadata from path
	}

	albums, err := store.SearchAlbums(ctx, albumTitle, 10)
	if err != nil || len(albums) == 0 {
		return fmt.Errorf("album not found in library: %s", albumTitle)
	}

	// Find album matching the artist.
	for i := range albums {
		artist, err := store.GetArtist(ctx, albums[i].ArtistID)
		if err != nil {
			continue
		}
		if artist.Name == artistName && albums[i].Title == albumTitle {
			albums[i].ThumbURL = "cover.jpg"
			if _, err := store.UpsertAlbum(ctx, &albums[i]); err != nil {
				return fmt.Errorf("upsert album thumb: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("no matching artist found for %s - %s", artistName, albumTitle)
}

// extractAlbumTitle strips the year suffix from a directory name like
// "Random Access Memories (2013)" → "Random Access Memories".
func extractAlbumTitle(dirName string) string {
	// Strip trailing year in parentheses: "Album (2024)"
	for i := len(dirName) - 2; i > 0; i-- {
		if dirName[i] == '(' {
			return dirName[:i-1] // trim space before '('
		}
	}
	return dirName
}
