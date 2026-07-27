package spotify

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/discovery"
)

// spotifyArtistAlbums fetches an artist's albums (shared by Plugin and DiscoveryPlugin).
func spotifyArtistAlbums(api *API, ctx context.Context, providerArtistID string, limit int, log *slog.Logger) ([]discovery.AlbumResult, error) {
	page, err := api.GetArtistAlbums(ctx, providerArtistID, limit, 0, "album,single,compilation")
	if err != nil {
		if log != nil {
			log.Error("spotify get artist albums failed", "error", err, "component", "spotify_discover")
		}
		return nil, err
	}

	var out []discovery.AlbumResult
	for _, a := range page.Items {
		year := 0
		if len(a.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(a.ReleaseDate[:4])
		}
		artistName := ""
		if len(a.Artists) > 0 {
			artistName = a.Artists[0].Name
		}
		out = append(out, discovery.AlbumResult{
			ProviderID:   a.ID,
			ProviderName: "spotify",
			ArtistName:   artistName,
			Title:        a.Name,
			Year:         year,
			CoverURL:     bestImage(a.Images, 300),
			TrackCount:   a.TotalTracks,
			Type:         strings.ToLower(a.AlbumType),
		})
	}
	return out, nil
}

// spotifyArtistTopTracks fetches an artist's top tracks.
func spotifyArtistTopTracks(api *API, ctx context.Context, providerArtistID string, log *slog.Logger) ([]discovery.TrackInfo, error) {
	tracks, err := api.GetArtistTopTracks(ctx, providerArtistID)
	if err != nil {
		if log != nil {
			log.Error("spotify get artist top tracks failed", "error", err, "component", "spotify_discover")
		}
		return nil, err
	}

	out := make([]discovery.TrackInfo, 0, len(tracks))
	for _, t := range tracks {
		artistName := ""
		albumTitle := t.Album.Name
		if len(t.Artists) > 0 {
			artistName = t.Artists[0].Name
		}
		out = append(out, discovery.TrackInfo{
			ProviderID:  t.ID,
			ArtistName:  artistName,
			AlbumTitle:  albumTitle,
			Title:       t.Name,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.DiscNumber,
			DurationMs:  int64(t.DurationMs),
		})
	}
	return out, nil
}

// spotifyAlbumTracks fetches all tracks for an album (shared by Plugin).
func spotifyAlbumTracks(api *API, ctx context.Context, providerAlbumID string, log *slog.Logger) ([]discovery.TrackInfo, error) {
	album, err := api.GetAlbum(ctx, providerAlbumID)
	if err != nil {
		if log != nil {
			log.Error("spotify get album for tracks failed", "error", err, "component", "spotify_discover")
		}
		return nil, err
	}

	artistName := ""
	if len(album.Artists) > 0 {
		artistName = album.Artists[0].Name
	}

	var out []discovery.TrackInfo
	offset := 0
	for {
		page, err := api.GetAlbumTracks(ctx, providerAlbumID, 50, offset)
		if err != nil {
			if log != nil {
				log.Error("spotify get album tracks page failed", "error", err, "component", "spotify_discover")
			}
			return nil, fmt.Errorf("spotify: get album tracks: %w", err)
		}
		for _, t := range page.Items {
			out = append(out, trackToInfo(t, artistName, album.Name))
		}
		if page.Next == "" || len(page.Items) == 0 {
			break
		}
		offset += len(page.Items)
	}

	return out, nil
}

// bestImage returns the URL of the image closest to the target width.
func bestImage(images []Image, targetWidth int) string {
	if len(images) == 0 {
		return ""
	}
	best := images[0]
	bestDiff := imageDiff(best, targetWidth)
	for _, img := range images[1:] {
		diff := imageDiff(img, targetWidth)
		if diff < bestDiff {
			best = img
			bestDiff = diff
		}
	}
	return best.URL
}

func imageDiff(img Image, target int) int {
	if img.Width == nil {
		return 99999
	}
	w := *img.Width
	if w > target {
		return w - target
	}
	return target - w
}

func trackToInfo(t SimplifiedTrack, artistName, albumTitle string) discovery.TrackInfo {
	trackArtists := artistName
	if len(t.Artists) > 0 {
		trackArtists = t.Artists[0].Name
	}
	return discovery.TrackInfo{
		ProviderID:  t.ID,
		ArtistName:  trackArtists,
		AlbumTitle:  albumTitle,
		Title:       t.Name,
		TrackNumber: t.TrackNumber,
		DiscNumber:  t.DiscNumber,
		DurationMs:  int64(t.DurationMs),
	}
}
