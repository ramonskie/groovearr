// Package deezer implements the Deezer playlist source using ARL auth.
package deezer

import (
	"context"
	"strconv"
	"strings"

	deezer "github.com/ramonskie/groovearr/internal/download/deezer"
	"github.com/ramonskie/groovearr/internal/playlist"
)

// PlaylistSource implements playlist.Source for Deezer via ARL auth.
type PlaylistSource struct {
	client *deezer.DownloadClient
}

// NewPlaylistSource creates a Deezer playlist source wrapping the download client.
func NewPlaylistSource(client *deezer.DownloadClient) *PlaylistSource {
	return &PlaylistSource{client: client}
}

func (s *PlaylistSource) Name() string          { return "deezer" }
func (s *PlaylistSource) DisplayName() string    { return "Deezer" }
func (s *PlaylistSource) IsConfigured() bool     { return s.client.IsConfigured() }

// GetUserPlaylists returns the authenticated user's playlists.
func (s *PlaylistSource) GetUserPlaylists(ctx context.Context) ([]playlist.PlaylistInfo, error) {
	raw, err := s.client.GetUserPlaylists(ctx)
	if err != nil {
		return nil, err
	}

	var out []playlist.PlaylistInfo
	for _, p := range raw {
		out = append(out, playlist.PlaylistInfo{
			SourceID:    p.ID,
			Name:        strings.TrimSpace(p.Title),
			Description: strings.TrimSpace(p.Description),
			TrackCount:  p.TrackCount,
		})
	}
	return out, nil
}

// GetPlaylistTracks returns all tracks for a playlist.
func (s *PlaylistSource) GetPlaylistTracks(ctx context.Context, sourcePlaylistID string) ([]playlist.TrackInfo, string, error) {
	raw, playlistName, err := s.client.GetPlaylistTracks(ctx, sourcePlaylistID)
	if err != nil {
		return nil, "", err
	}

	var out []playlist.TrackInfo
	for _, t := range raw {
		durMs, _ := strconv.ParseInt(t.Duration, 10, 64)
		out = append(out, playlist.TrackInfo{
			SourceTrackID: t.ID,
			Title:         strings.TrimSpace(t.Title),
			Artist:        strings.TrimSpace(t.Artist),
			Album:         strings.TrimSpace(t.Album),
			DurationMs:    durMs * 1000,
			ISRC:          strings.TrimSpace(t.ISRC),
		})
	}
	return out, playlistName, nil
}
