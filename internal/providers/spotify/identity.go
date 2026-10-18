package spotify

import (
	"context"
	"errors"
	"strings"

	"github.com/ramonskie/groovearr/internal/discovery"
)

var _ discovery.TrackIdentityProvider = (*Plugin)(nil)

// IdentityAvailable reports whether Spotify's Web API is configured. Free mode
// still discovers/search, but cannot resolve a track identity.
func (p *Plugin) IdentityAvailable() bool { return p.api != nil }

// GetTrackIdentity resolves one Spotify track by its provider id. It returns
// (nil, nil) in free mode (no Web API) and when the id is empty.
func (p *Plugin) GetTrackIdentity(ctx context.Context, providerTrackID string) (*discovery.TrackIdentity, error) {
	id := strings.TrimSpace(providerTrackID)
	if p.api == nil || id == "" {
		return nil, nil
	}
	t, err := p.api.GetTrack(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return spotifyTrackIdentity(t), nil
}

// LookupTrackByISRC resolves one Spotify track by ISRC. Spotify's search API
// supports the `isrc:` filter; the result is accepted only when the returned
// track carries the same ISRC, so a fuzzy search miss cannot become an
// identity. Returns (nil, nil) when no track matches.
func (p *Plugin) LookupTrackByISRC(ctx context.Context, isrc string) (*discovery.TrackIdentity, error) {
	isrc = strings.TrimSpace(isrc)
	if p.api == nil || isrc == "" {
		return nil, nil
	}
	page, err := p.api.SearchTracks(ctx, "isrc:"+isrc, 5, 0)
	if err != nil {
		return nil, err
	}
	for i := range page.Items {
		if strings.EqualFold(strings.TrimSpace(page.Items[i].ExternalIDs.ISRC), isrc) {
			return spotifyTrackIdentity(&page.Items[i]), nil
		}
	}
	return nil, nil
}

// spotifyTrackIdentity maps the Web API track onto the provider-neutral
// identity. Spotify populates the album summary (type, total tracks) on a
// track lookup, so the resolver can distinguish a single from an album track.
func spotifyTrackIdentity(t *Track) *discovery.TrackIdentity {
	artistName := ""
	if len(t.Artists) > 0 {
		artistName = t.Artists[0].Name
	}
	return &discovery.TrackIdentity{
		ProviderName:    pluginName,
		ProviderTrackID: t.ID,
		ProviderAlbumID: t.Album.ID,
		Title:           t.Name,
		ArtistName:      artistName,
		AlbumTitle:      t.Album.Name,
		AlbumType:       strings.ToLower(t.Album.AlbumType),
		AlbumTrackCount: t.Album.TotalTracks,
		TrackNumber:     t.TrackNumber,
		DiscNumber:      t.DiscNumber,
		DurationMs:      int64(t.DurationMs),
		ISRC:            t.ExternalIDs.ISRC,
	}
}
