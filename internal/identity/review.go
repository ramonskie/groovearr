package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// Review errors are client-visible through the admin API. They carry no server
// paths or provider response text (AGENTS §10).
var (
	// ErrAlbumNotUnmatched means the album is unknown or was already resolved.
	ErrAlbumNotUnmatched = errors.New("album is not unmatched")
	// ErrProviderUnavailable means the named provider is not a configured
	// discovery provider with the track-identity capability.
	ErrProviderUnavailable = errors.New("identity provider is not available")
	// ErrProviderCoolingDown means the shared cooldown bucket has parked the
	// provider; the caller should retry after it clears (AGENTS §8).
	ErrProviderCoolingDown = errors.New("identity provider is cooling down")
	// ErrCandidateNotFound means the requested provider track/release does not
	// exist or cannot be linked to a release.
	ErrCandidateNotFound = errors.New("identity candidate not found")
	// ErrReleaseEmpty means the provider release returned no tracks.
	ErrReleaseEmpty = errors.New("identity release has no tracks")
	// ErrReleaseTooSmall means a multi-track local album was matched to a
	// provider release with fewer tracks, which would drop catalog rows.
	ErrReleaseTooSmall = errors.New("provider release has fewer tracks than the local album")
	// ErrReleaseMatchRequired means a multi-track local album was offered a
	// single-track manual match. A whole release is the safe unit.
	ErrReleaseMatchRequired = errors.New("multi-track album requires a release match")
	// ErrTrackMatchRequired means a one-track local album was offered a
	// multi-track release. Use a single-track match so the local single does not
	// expand into an album.
	ErrTrackMatchRequired = errors.New("single-track album requires a track match")
)

// ReviewSignals is the evidence behind one candidate score. It is the public
// wire shape of matching.CandidateSignals.
type ReviewSignals struct {
	ISRCExact               bool `json:"isrc_exact"`
	ProviderTrackIDExact    bool `json:"provider_track_id_exact"`
	TitleMatches            bool `json:"title_matches"`
	ArtistMatches           bool `json:"artist_matches"`
	DurationKnown           bool `json:"duration_known"`
	DurationWithinTolerance bool `json:"duration_within_tolerance"`
	AlbumTitleMatches       bool `json:"album_title_matches"`
	TrackCountMatches       bool `json:"track_count_matches"`
	TrackNumberMatches      bool `json:"track_number_matches"`
}

// ReviewCandidate is one provider track the admin can apply to a local track.
type ReviewCandidate struct {
	Provider        string        `json:"provider"`
	ProviderTrackID string        `json:"provider_track_id"`
	ProviderAlbumID string        `json:"provider_album_id"`
	Title           string        `json:"title"`
	Artist          string        `json:"artist"`
	Album           string        `json:"album"`
	AlbumType       string        `json:"album_type,omitempty"`
	AlbumTrackCount int           `json:"album_track_count,omitempty"`
	TrackNumber     int           `json:"track_number,omitempty"`
	DiscNumber      int           `json:"disc_number,omitempty"`
	DurationMs      int64         `json:"duration_ms,omitempty"`
	ISRC            string        `json:"isrc,omitempty"`
	Score           int           `json:"score"`
	Auto            bool          `json:"auto"`
	Signals         ReviewSignals `json:"signals"`
}

// ReviewTrack is one local track with its candidate list. Candidates are only
// populated for a one-track local release: multi-track review applies a full
// provider release selected through the discovery search fallback.
type ReviewTrack struct {
	TrackID     int64             `json:"track_id"`
	Title       string            `json:"title"`
	Artist      string            `json:"artist"`
	Album       string            `json:"album"`
	TrackNumber int               `json:"track_number,omitempty"`
	DiscNumber  int               `json:"disc_number,omitempty"`
	DurationMs  int64             `json:"duration_ms,omitempty"`
	ISRC        string            `json:"isrc,omitempty"`
	Candidates  []ReviewCandidate `json:"candidates"`
}

// AlbumReview is the payload behind the manual unmatched-review dialog.
type AlbumReview struct {
	AlbumID    int64         `json:"album_id"`
	Artist     string        `json:"artist"`
	Title      string        `json:"title"`
	TrackCount int           `json:"track_count"`
	Tracks     []ReviewTrack `json:"tracks"`
	// IdentityProviders lists the providers that supported track-identity
	// lookups when the review was built. A provider can cool down while
	// candidates are probed; applying through it then returns
	// ErrProviderCoolingDown. Release matches work for any discovery provider,
	// but a single-track match needs one of these.
	IdentityProviders []string `json:"identity_providers"`
}

// ReviewAlbum returns the local tracks and, for a one-track release, the
// provider identity candidates. It never materializes metadata; the admin
// explicitly applies one candidate through ApplyTrackMatch / ApplyReleaseMatch.
//
// Multi-track reviews intentionally skip per-track identity candidates: the
// safe action is a complete provider release chosen from search, and probing
// every identity for a 20-track album would exceed the request budget. The
// local tracks are still returned so the UI can play them.
func (r *Resolver) ReviewAlbum(ctx context.Context, albumID int64) (AlbumReview, error) {
	album, err := r.catalog.GetUnmatchedAlbum(ctx, albumID)
	if err != nil {
		return AlbumReview{}, fmt.Errorf("identity: review album: %w", err)
	}
	if album == nil {
		return AlbumReview{}, ErrAlbumNotUnmatched
	}
	tracks, err := r.lib.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		return AlbumReview{}, fmt.Errorf("identity: review tracks: %w", err)
	}
	review := AlbumReview{
		AlbumID:           album.AlbumID,
		Artist:            album.Artist,
		Title:             album.Title,
		TrackCount:        len(tracks),
		Tracks:            make([]ReviewTrack, 0, len(tracks)),
		IdentityProviders: []string{},
	}
	providers := r.reviewTrackProviders()
	for _, p := range providers {
		review.IdentityProviders = append(review.IdentityProviders, p.Name())
	}
	for _, track := range tracks {
		review.Tracks = append(review.Tracks, ReviewTrack{
			TrackID:     track.ID,
			Title:       track.Title,
			Artist:      album.Artist,
			Album:       album.Title,
			TrackNumber: track.TrackNumber,
			DiscNumber:  track.DiscNumber,
			DurationMs:  track.Duration,
			ISRC:        track.ISRC,
			Candidates:  []ReviewCandidate{},
		})
	}
	if len(tracks) != 1 {
		return review, nil
	}

	// Probe only this album's missing durations, exactly like the resolver, so
	// review and auto-resolution score the same evidence.
	probeEnabled := true
	if err := r.probeDurations(ctx, tracks, &probeEnabled); err != nil {
		return AlbumReview{}, err
	}
	review.Tracks[0].DurationMs = tracks[0].Duration
	for _, c := range r.candidates(ctx, *album, tracks[0], 1, providers) {
		review.Tracks[0].Candidates = append(review.Tracks[0].Candidates, reviewCandidate(c))
	}
	if err := ctx.Err(); err != nil {
		return AlbumReview{}, err
	}
	return review, nil
}

// ApplyTrackMatch materializes one chosen provider track for a one-track local
// release. The admin override bypasses the auto-confidence policy, but the
// provider identity must still resolve to a real release and the album must
// still be unmatched. The write goes through refresh's MaterializeResolved so
// the queue/coalescing/SSE behavior is identical to an automatic resolution.
func (r *Resolver) ApplyTrackMatch(ctx context.Context, albumID, localTrackID int64,
	providerName, providerTrackID string) (bool, error) {
	album, err := r.catalog.GetUnmatchedAlbum(ctx, albumID)
	if err != nil {
		return false, fmt.Errorf("identity: apply track album: %w", err)
	}
	if album == nil {
		return false, ErrAlbumNotUnmatched
	}
	tracks, err := r.lib.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		return false, fmt.Errorf("identity: apply track tracks: %w", err)
	}
	if len(tracks) != 1 {
		return false, ErrReleaseMatchRequired
	}
	if localTrackID != 0 && tracks[0].ID != localTrackID {
		return false, ErrCandidateNotFound
	}
	providerName = strings.TrimSpace(providerName)
	providerTrackID = strings.TrimSpace(providerTrackID)
	if providerName == "" || !validProviderID(providerTrackID) {
		return false, ErrCandidateNotFound
	}
	provider, err := r.identityProvider(providerName)
	if err != nil {
		return false, err
	}
	id, err := r.getTrackIdentity(ctx, provider, providerTrackID)
	if err != nil {
		r.handleProviderError(providerName, err)
		if errors.Is(err, discovery.ErrNotFound) {
			return false, ErrCandidateNotFound
		}
		return false, fmt.Errorf("identity: apply track lookup: %w", err)
	}
	if id == nil || strings.TrimSpace(id.ProviderTrackID) == "" || strings.TrimSpace(id.ProviderAlbumID) == "" {
		return false, ErrCandidateNotFound
	}
	partial := id.AlbumTrackCount > 1
	applied, err := r.materializer.MaterializeResolved(ctx, albumID, providerName, id.ProviderAlbumID,
		[]discovery.TrackInfo{trackInfoFromIdentity(id)}, partial)
	if err != nil {
		return false, fmt.Errorf("identity: apply track materialize: %w", err)
	}
	if applied {
		r.cleanupQueue(ctx, albumID)
		r.log.Info("identity manually matched track", "album_id", albumID, "provider", providerName,
			"provider_track_id", id.ProviderTrackID, "partial", partial, "component", "identity")
	}
	return applied, nil
}

// ApplyReleaseMatch materializes a complete provider release chosen by the
// admin. It is the multi-track action (and may be used for a one-track local
// release only when the chosen provider release also has one track). No
// per-track merge is attempted: catalog tracks are replaced by the provider
// release through the standard materialization path.
func (r *Resolver) ApplyReleaseMatch(ctx context.Context, albumID int64,
	providerName, providerAlbumID string) (int, bool, error) {
	album, err := r.catalog.GetUnmatchedAlbum(ctx, albumID)
	if err != nil {
		return 0, false, fmt.Errorf("identity: apply release album: %w", err)
	}
	if album == nil {
		return 0, false, ErrAlbumNotUnmatched
	}
	localTracks, err := r.lib.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		return 0, false, fmt.Errorf("identity: apply release tracks: %w", err)
	}
	providerName = strings.TrimSpace(providerName)
	providerAlbumID = strings.TrimSpace(providerAlbumID)
	if providerName == "" || !validProviderID(providerAlbumID) {
		return 0, false, ErrCandidateNotFound
	}
	// A release match only needs the base discovery capability. Providers
	// without track-identity support can still supply a complete tracklist for
	// the search fallback.
	provider, err := r.releaseProvider(providerName)
	if err != nil {
		return 0, false, err
	}
	full, err := r.fetchAlbumTracks(ctx, provider, providerAlbumID)
	if err != nil {
		r.handleProviderError(providerName, err)
		if errors.Is(err, discovery.ErrNotFound) {
			return 0, false, ErrCandidateNotFound
		}
		return 0, false, fmt.Errorf("identity: apply release fetch: %w", err)
	}
	if len(full) == 0 {
		return 0, false, ErrReleaseEmpty
	}
	if len(localTracks) == 1 && len(full) > 1 {
		return 0, false, ErrTrackMatchRequired
	}
	if len(localTracks) > 1 && len(full) < len(localTracks) {
		return 0, false, ErrReleaseTooSmall
	}
	applied, err := r.materializer.MaterializeResolved(ctx, albumID, providerName, providerAlbumID, full, false)
	if err != nil {
		return 0, false, fmt.Errorf("identity: apply release materialize: %w", err)
	}
	if applied {
		r.cleanupQueue(ctx, albumID)
		r.log.Info("identity manually matched release", "album_id", albumID, "provider", providerName,
			"provider_album_id", providerAlbumID, "tracks", len(full), "component", "identity")
	}
	return len(full), applied, nil
}

// identityProvider returns the named provider only when it is configured, has
// the track-identity capability and is currently available. Order is not
// needed here: the admin chose the provider.
func (r *Resolver) identityProvider(name string) (discovery.TrackIdentityProvider, error) {
	for _, p := range metadata.Order(r.order, r.providers()) {
		if p.Name() != name {
			continue
		}
		tp, ok := p.(discovery.TrackIdentityProvider)
		if !ok {
			return nil, ErrProviderUnavailable
		}
		if avail, ok := tp.(discovery.IdentityAvailability); ok && !avail.IdentityAvailable() {
			return nil, ErrProviderUnavailable
		}
		if r.cooldown != nil && r.cooldown.CoolingDown(name) {
			return nil, ErrProviderCoolingDown
		}
		return tp, nil
	}
	return nil, ErrProviderUnavailable
}

// releaseProvider returns any configured discovery provider; GetAlbumTracks is
// on the base interface, so a release match does not require identity lookups.
func (r *Resolver) releaseProvider(name string) (discovery.Provider, error) {
	for _, p := range metadata.Order(r.order, r.providers()) {
		if p.Name() != name {
			continue
		}
		if r.cooldown != nil && r.cooldown.CoolingDown(name) {
			return nil, ErrProviderCoolingDown
		}
		return p, nil
	}
	return nil, ErrProviderUnavailable
}

// validProviderID rejects strings that would alter a provider URL path or
// query. Provider ids are opaque, but none of the supported providers use
// '/', '?', '#' or '%'; forwarding them would be a client-controlled URL
// injection. Dot segments are rejected explicitly because url.PathEscape leaves
// them intact.
func validProviderID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 256 {
		return false
	}
	return !strings.ContainsAny(id, "/?#% \t\r\n")
}

func reviewCandidate(c scoredCandidate) ReviewCandidate {
	id := c.identity
	return ReviewCandidate{
		Provider:        id.ProviderName,
		ProviderTrackID: id.ProviderTrackID,
		ProviderAlbumID: id.ProviderAlbumID,
		Title:           id.Title,
		Artist:          id.ArtistName,
		Album:           id.AlbumTitle,
		AlbumType:       id.AlbumType,
		AlbumTrackCount: id.AlbumTrackCount,
		TrackNumber:     id.TrackNumber,
		DiscNumber:      id.DiscNumber,
		DurationMs:      id.DurationMs,
		ISRC:            id.ISRC,
		Score:           c.score,
		Auto:            autoAllowed(c) && !c.reviewOnly,
		Signals: ReviewSignals{
			ISRCExact:               c.signals.ISRCExact,
			ProviderTrackIDExact:    c.signals.ProviderTrackIDExact,
			TitleMatches:            c.signals.TitleMatches,
			ArtistMatches:           c.signals.ArtistMatches,
			DurationKnown:           c.signals.DurationKnown,
			DurationWithinTolerance: c.signals.DurationWithinTolerance,
			AlbumTitleMatches:       c.signals.AlbumTitleMatches,
			TrackCountMatches:       c.signals.TrackCountMatches,
			TrackNumberMatches:      c.signals.TrackNumberMatches,
		},
	}
}

func trackInfoFromIdentity(id *discovery.TrackIdentity) discovery.TrackInfo {
	return discovery.TrackInfo{
		ProviderID:  id.ProviderTrackID,
		ArtistName:  id.ArtistName,
		AlbumTitle:  id.AlbumTitle,
		Title:       id.Title,
		TrackNumber: id.TrackNumber,
		DiscNumber:  id.DiscNumber,
		DurationMs:  id.DurationMs,
		ISRC:        id.ISRC,
	}
}
