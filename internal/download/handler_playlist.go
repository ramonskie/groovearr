package download

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// PlaylistLinkerHandler links a newly imported track to its corresponding
// playlist_tracks entry in the library store when the download originated
// from a playlist.
type PlaylistLinkerHandler struct {
	libStore library.Store
}

// NewPlaylistLinkerHandler creates a handler that links playlist tracks to
// their library track IDs after import.
func NewPlaylistLinkerHandler(libStore library.Store) *PlaylistLinkerHandler {
	return &PlaylistLinkerHandler{libStore: libStore}
}

// Handle links the imported track to unmatched playlist_tracks entries
// using title and artist matching.
func (h *PlaylistLinkerHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.PlaylistID == "" || record.LibraryTrackID == 0 {
		return nil
	}

	playlistID, err := strconv.ParseInt(record.PlaylistID, 10, 64)
	if err != nil {
		return fmt.Errorf("playlist linker: invalid playlist_id %q: %w", record.PlaylistID, err)
	}

	// Get the imported library track.
	track, err := h.libStore.GetTrack(ctx, record.LibraryTrackID)
	if err != nil || track == nil {
		return fmt.Errorf("playlist linker: track %d not found", record.LibraryTrackID)
	}

	// Get the artist name for matching.
	artist, err := h.libStore.GetArtist(ctx, track.ArtistID)
	if err != nil || artist == nil {
		return fmt.Errorf("playlist linker: artist %d not found", track.ArtistID)
	}

	// Link by exact title + artist match only. No loose fallback —
	// mis-linking the wrong track is worse than leaving it unmatched.
	tracks, err := h.libStore.GetPlaylistTracks(ctx, playlistID)
	if err != nil {
		return fmt.Errorf("playlist linker: get playlist tracks: %w", err)
	}

	linked := false
	for i := range tracks {
		if tracks[i].TrackID != nil {
			continue
		}

		if h.matchesTrack(&tracks[i], track, artist) {
			tid := track.ID
			tracks[i].TrackID = &tid
			if err := h.libStore.UpsertPlaylistTrack(ctx, &tracks[i]); err != nil {
				return fmt.Errorf("playlist linker: upsert: %w", err)
			}
			linked = true
		}
	}

	if !linked {
		log.Printf("playlist linker: no match for %s - %s in playlist %d", track.Title, artist.Name, playlistID)
	}

	return nil
}

// matchesTrack checks whether a playlist track matches a library track
// by comparing title and artist names.
func (h *PlaylistLinkerHandler) matchesTrack(pt *domain.PlaylistTrack, track *domain.Track, artist *domain.Artist) bool {
	titleMatch := strings.EqualFold(pt.Title, track.Title)
	artistMatch := strings.EqualFold(pt.Artist, artist.Name)

	if titleMatch && artistMatch {
		return true
	}

	// Loose match: normalize both sides.
	ptTitle := strings.ToLower(strings.TrimSpace(pt.Title))
	trackTitle := strings.ToLower(strings.TrimSpace(track.Title))
	ptArtist := strings.ToLower(strings.TrimSpace(pt.Artist))
	trArtist := strings.ToLower(strings.TrimSpace(artist.Name))

	return ptTitle == trackTitle && ptArtist == trArtist
}
