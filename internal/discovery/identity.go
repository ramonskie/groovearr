package discovery

import "context"

// TrackIdentity is one provider track with the release context needed to
// resolve a local track to a provider release. It is deliberately
// provider-neutral: a provider maps its own response onto it and the caller
// never sees a provider-specific type (AGENTS §1).
type TrackIdentity struct {
	ProviderName    string
	ProviderTrackID string
	ProviderAlbumID string
	Title           string
	ArtistName      string
	AlbumTitle      string
	// AlbumType is the provider's release kind (album, single, ep,
	// compilation). Empty when the provider does not report one.
	AlbumType string
	// AlbumTrackCount is the number of tracks on the provider release. Zero
	// means unknown.
	AlbumTrackCount int
	TrackNumber     int
	DiscNumber      int
	DurationMs      int64
	ISRC            string
}

// TrackIdentityProvider is an optional discovery capability: a provider that
// can resolve one exact track by provider ID or ISRC. Callers type-assert a
// discovery.Provider to this interface, so core code depends on behavior, not
// a concrete provider package (D-003 / AGENTS §1).
type TrackIdentityProvider interface {
	Provider

	// GetTrackIdentity resolves one provider track by its provider ID.
	// Returns (nil, nil) when the provider has no such track.
	GetTrackIdentity(ctx context.Context, providerTrackID string) (*TrackIdentity, error)

	// LookupTrackByISRC resolves a provider track by ISRC. Returns (nil, nil)
	// when the provider has no track for that ISRC.
	LookupTrackByISRC(ctx context.Context, isrc string) (*TrackIdentity, error)
}
