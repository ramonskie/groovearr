package discovery

import (
	"context"
	"errors"
)

// ErrNotFound is the provider-neutral sentinel for a missing provider entity.
// Providers wrap their own not-found/invalid-id errors with it so callers can
// map a missing track/release to a client error without importing a provider
// package (AGENTS §1).
var ErrNotFound = errors.New("discovery: not found")

// IdentityAvailability is an optional capability for TrackIdentityProvider
// implementations that can be configured but temporarily unable to perform
// identity lookups (for example Spotify free mode, which has no Web API).
// Providers that implement it are excluded from identity candidates when it
// returns false, while still remaining normal discovery providers.
type IdentityAvailability interface {
	IdentityAvailable() bool
}

// ReviewOnlyIdentity is an optional marker for a TrackIdentityProvider whose
// lookups are valid manual-review candidates but too slow or too costly for a
// bulk automatic resolve pass. The identity resolver excludes these providers
// from ResolveUnmatched while the review dialog still offers them.
type ReviewOnlyIdentity interface {
	IdentityReviewOnly() bool
}

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
//
// Optional release context must not fail a usable identity. Providers propagate
// rate-limit errors and context cancellation/deadline, but a 5xx without
// Retry-After (transient busy) or other optional-context failure keeps the
// identity with an unknown track count.
type TrackIdentityProvider interface {
	Provider

	// GetTrackIdentity resolves one provider track by its provider ID.
	// Returns (nil, nil) when the provider has no such track.
	GetTrackIdentity(ctx context.Context, providerTrackID string) (*TrackIdentity, error)

	// LookupTrackByISRC resolves a provider track by ISRC. Returns (nil, nil)
	// when the provider has no track for that ISRC.
	LookupTrackByISRC(ctx context.Context, isrc string) (*TrackIdentity, error)
}
