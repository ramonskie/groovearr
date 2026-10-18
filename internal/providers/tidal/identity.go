package tidal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/metadata"
)

var _ discovery.TrackIdentityProvider = (*Client)(nil)
var _ discovery.IdentityAvailability = (*Client)(nil)

// IdentityAvailable reports whether Tidal has an access token. The API cannot
// resolve tracks without auth, so an unconfigured provider is omitted from
// identity candidates while still remaining a normal discovery plugin.
func (c *Client) IdentityAvailable() bool { return c.IsMetadataAvailable() }

// GetTrackIdentity resolves one Tidal track by its provider id. The track
// lookup supplies the album link; a second album lookup fills the release type
// and track count used by the identity scorer. Returns (nil, nil) when the track
// does not exist; wraps discovery.ErrNotFound when the supplied id is malformed.
func (c *Client) GetTrackIdentity(ctx context.Context, providerTrackID string) (*discovery.TrackIdentity, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(providerTrackID), 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("tidal: invalid track id %q: %w", providerTrackID, discovery.ErrNotFound)
	}
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}
	track, err := c.streams.GetTrack(ctx, uint64(id))
	if err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}

	albumID := ""
	if track.Album.ID > 0 {
		albumID = strconv.FormatInt(int64(track.Album.ID), 10)
	}
	return c.fillAlbumContext(ctx, tidalStreamTrackIdentity(track, albumID))
}

// LookupTrackByISRC resolves a Tidal track by exact ISRC. The legacy v1 tracks
// endpoint can return several tracks for one ISRC; only an exact normalized
// match is accepted. When the exact matches span more than one track or album
// the release is ambiguous, so it returns (nil, nil) and the album stays a
// review case.
func (c *Client) LookupTrackByISRC(ctx context.Context, isrc string) (*discovery.TrackIdentity, error) {
	isrc = matching.NormalizeISRC(isrc)
	if isrc == "" {
		return nil, nil
	}
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}
	tracks, err := c.api.GetTracksByISRC(ctx, isrc)
	if err != nil {
		return nil, err
	}
	exact := make([]TrackSearchResult, 0, 1)
	for i := range tracks {
		if matching.NormalizeISRC(tracks[i].ISRC) == isrc {
			exact = append(exact, tracks[i])
		}
	}
	if len(exact) == 0 {
		return nil, nil
	}

	// One ISRC must identify exactly one track and one release. A different
	// track id on the same album is just as ambiguous as two albums: choosing
	// one would make the returned track number/title arbitrary.
	trackIDs := make(map[string]struct{}, len(exact))
	albumIDs := make(map[string]struct{}, len(exact))
	for i := range exact {
		if exact[i].ID > 0 {
			trackIDs[strconv.FormatInt(exact[i].ID, 10)] = struct{}{}
		}
		if exact[i].Album.ID > 0 {
			albumIDs[strconv.FormatInt(exact[i].Album.ID, 10)] = struct{}{}
		}
	}
	if len(trackIDs) != 1 || len(albumIDs) != 1 {
		return nil, nil // ISRC clash: review/search case
	}

	var chosen *TrackSearchResult
	for i := range exact {
		if exact[i].ID > 0 && exact[i].Album.ID > 0 {
			chosen = &exact[i]
			break
		}
	}
	if chosen == nil {
		return nil, nil // no materializable release context
	}
	return c.fillAlbumContext(ctx, tidalSearchTrackIdentity(chosen))
}

// fillAlbumContext fetches the album summary that the sparse track endpoints do
// not carry. Release context is optional evidence: a rate limit still parks the
// provider, while a transient busy response or a missing/unknown album keeps
// the identity usable.
func (c *Client) fillAlbumContext(ctx context.Context, id *discovery.TrackIdentity) (*discovery.TrackIdentity, error) {
	if id == nil || id.ProviderAlbumID == "" {
		return id, nil
	}
	album, err := c.api.GetAlbum(ctx, id.ProviderAlbumID)
	if err != nil {
		// A rate-limit response must reach the shared bucket even when the
		// caller's context is ending, so it takes precedence over ctx.Err().
		if errors.Is(err, metadata.ErrRateLimited) {
			return nil, err
		}
		// Optional release context must not turn a canceled or timed-out call
		// into a successful identity lookup.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if c.log != nil {
			c.log.Debug("tidal album context unavailable", "album_id", id.ProviderAlbumID,
				"error", err, "component", "tidal")
		}
		return id, nil
	}
	if album != nil {
		id.AlbumType = strings.ToLower(album.Type)
		id.AlbumTrackCount = album.NumTracks
		if id.AlbumTitle == "" {
			id.AlbumTitle = album.Title
		}
	}
	return id, nil
}

// tidalSearchTrackIdentity maps the sparse tracks-endpoint result. Album type
// and count are filled separately by fillAlbumContext because the v1 track
// object only guarantees the album id/title.
func tidalSearchTrackIdentity(t *TrackSearchResult) *discovery.TrackIdentity {
	albumID := ""
	if t.Album.ID > 0 {
		albumID = strconv.FormatInt(t.Album.ID, 10)
	}
	return &discovery.TrackIdentity{
		ProviderName:    pluginName,
		ProviderTrackID: strconv.FormatInt(t.ID, 10),
		ProviderAlbumID: albumID,
		Title:           t.Title,
		ArtistName:      primaryArtistName(t.Artist, t.Artists),
		AlbumTitle:      t.Album.Title,
		AlbumType:       strings.ToLower(t.Album.Type),
		AlbumTrackCount: t.Album.NumTracks,
		TrackNumber:     t.TrackNumber,
		DiscNumber:      t.VolumeNumber,
		DurationMs:      int64(t.Duration) * 1000,
		ISRC:            matching.NormalizeISRC(t.ISRC),
	}
}

// tidalStreamTrackIdentity maps the full track lookup. The stream track model
// only carries the album id/title, so fillAlbumContext supplies the release
// type and count.
func tidalStreamTrackIdentity(t Track, albumID string) *discovery.TrackIdentity {
	artist := t.Artist.Name
	if artist == "" && len(t.Artists) > 0 {
		artist = t.Artists[0].Name
	}
	return &discovery.TrackIdentity{
		ProviderName:    pluginName,
		ProviderTrackID: strconv.Itoa(t.ID),
		ProviderAlbumID: albumID,
		Title:           t.Title,
		ArtistName:      artist,
		AlbumTitle:      t.Album.Title,
		TrackNumber:     t.TrackNumber,
		DiscNumber:      t.VolumeNumber,
		DurationMs:      int64(t.Duration) * 1000,
		ISRC:            matching.NormalizeISRC(t.Isrc),
	}
}
