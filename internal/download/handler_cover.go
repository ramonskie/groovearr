package download

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
	"github.com/ramonskie/groovearr/internal/library"
)

// CoverArtHandler downloads album cover art for a completed download and
// updates the album thumb_url in the library store.
type CoverArtHandler struct {
	libStore   library.Store
	httpClient *http.Client
}

// NewCoverArtHandler creates a handler that fetches cover art from the
// download record's CoverURL and caches it as cover.jpg in the album directory.
func NewCoverArtHandler(libStore library.Store) *CoverArtHandler {
	return &CoverArtHandler{
		libStore:   libStore,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Handle downloads cover art from record.CoverURL, saves it alongside the
// audio file, and updates the matching album's thumb_url in the library store.
// Cover art failures are non-fatal — the import continues.
func (h *CoverArtHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.CoverURL == "" || record.FilePath == "" {
		return nil
	}

	albumDir := filepath.Dir(record.FilePath)
	coverPath := filepath.Join(albumDir, "cover.jpg")

	// Skip if cover already cached.
	if _, err := os.Stat(coverPath); err == nil {
		return nil
	}

	if err := h.downloadCover(ctx, record.CoverURL, coverPath); err != nil {
		log.Printf("cover handler: download failed for %s: %v", record.Filename, err)
		return nil // non-fatal
	}

	// Best-effort album thumb update.
	_ = h.updateAlbumThumb(ctx, albumDir)

	return nil
}

// downloadCover fetches an image from url and writes it to dst.
func (h *CoverArtHandler) downloadCover(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := h.httpClient.Do(req)
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

// updateAlbumThumb updates the album's thumb_url to "cover.jpg" when the
// album directory matches an existing album in the library.
func (h *CoverArtHandler) updateAlbumThumb(ctx context.Context, albumDir string) error {
	artistName := filepath.Base(filepath.Dir(albumDir))
	albumTitle := extractAlbumTitle(filepath.Base(albumDir))

	if artistName == "" || albumTitle == "" {
		return nil
	}

	albums, err := h.libStore.SearchAlbums(ctx, albumTitle, 10)
	if err != nil || len(albums) == 0 {
		return fmt.Errorf("album not found: %s", albumTitle)
	}

	for i := range albums {
		artist, err := h.libStore.GetArtist(ctx, albums[i].ArtistID)
		if err != nil {
			continue
		}
		if artist.Name == artistName && albums[i].Title == albumTitle {
			albums[i].ThumbURL = "cover.jpg"
			if _, err := h.libStore.UpsertAlbum(ctx, &albums[i]); err != nil {
				return fmt.Errorf("upsert album thumb: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("no matching artist for %s - %s", artistName, albumTitle)
}

// extractAlbumTitle strips the year suffix from a directory name.
func extractAlbumTitle(dirName string) string {
	for i := len(dirName) - 2; i > 0; i-- {
		if dirName[i] == '(' {
			return dirName[:i-1]
		}
	}
	return dirName
}
