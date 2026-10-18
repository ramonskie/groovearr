package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/metadata"
)

var _ discovery.TrackIdentityProvider = (*Client)(nil)
var _ discovery.ReviewOnlyIdentity = (*Client)(nil)

// IdentityReviewOnly keeps MusicBrainz out of the bulk automatic resolve pass.
// One ISRC needs an ISRC lookup, a recording lookup, and a release-group lookup
// at MusicBrainz's one-request-per-second pace, so it is useful for a manually
// reviewed album but too slow to probe across the whole parked backlog.
func (c *Client) IdentityReviewOnly() bool { return true }

// GetTrackIdentity resolves one MusicBrainz recording by its MBID. The recording
// lookup supplies the release group and track context needed to materialize a
// catalog release. Returns (nil, nil) when the recording or a usable release is
// missing.
func (c *Client) GetTrackIdentity(ctx context.Context, providerTrackID string) (*discovery.TrackIdentity, error) {
	providerTrackID = strings.TrimSpace(providerTrackID)
	if providerTrackID == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(providerTrackID); err != nil {
		return nil, fmt.Errorf("musicbrainz: invalid recording id %q: %w", providerTrackID, discovery.ErrNotFound)
	}
	recording, err := c.api.LookupRecording(ctx, providerTrackID)
	if err != nil {
		return nil, err
	}
	if recording == nil || recording.ID == "" {
		return nil, nil
	}
	return c.fillReleaseGroupTrackCount(ctx, recordingIdentity(recording, ""))
}

// LookupTrackByISRC resolves an ISRC to one MusicBrainz recording with release
// context. MusicBrainz's JSON ISRC endpoint omits releases, so a second recording
// lookup is required. Only an exact ISRC match is accepted. Returns (nil, nil)
// when no recording/release matches or when an ISRC clash yields more than one
// distinct recording/release identity.
func (c *Client) LookupTrackByISRC(ctx context.Context, isrc string) (*discovery.TrackIdentity, error) {
	isrc = matching.NormalizeISRC(isrc)
	if isrc == "" {
		return nil, nil
	}
	recordings, err := c.api.LookupISRC(ctx, isrc)
	if err != nil {
		return nil, err
	}
	var found *discovery.TrackIdentity
	for _, candidate := range recordings {
		if !hasISRC(candidate.ISRCs, isrc) {
			continue
		}
		recording, err := c.api.LookupRecording(ctx, candidate.ID)
		if err != nil {
			return nil, err
		}
		if recording == nil || recording.ID == "" {
			continue
		}
		id := recordingIdentity(recording, isrc)
		if id == nil {
			continue
		}
		if found != nil {
			if found.ProviderTrackID == id.ProviderTrackID && found.ProviderAlbumID == id.ProviderAlbumID {
				continue
			}
			return nil, nil // ISRC clash: leave the album for review/search
		}
		found = id
	}
	return c.fillReleaseGroupTrackCount(ctx, found)
}

// fillReleaseGroupTrackCount fills the release-group size from the same
// representative release GetAlbumTracks would materialize. The recording lookup
// only includes the medium carrying the recording, so summing it would
// undercount multi-disc releases. Release-group media counts are authoritative
// for the whole release.
func (c *Client) fillReleaseGroupTrackCount(ctx context.Context, id *discovery.TrackIdentity) (*discovery.TrackIdentity, error) {
	if id == nil || id.ProviderAlbumID == "" {
		return id, nil
	}
	releases, err := c.api.LookupReleaseGroup(ctx, id.ProviderAlbumID)
	if err != nil {
		// A rate-limit response must reach the shared bucket even when the
		// caller's context is ending. A transient busy/5xx response is only
		// optional context: the recording identity is still usable, so keep it
		// with an unknown track count.
		if errors.Is(err, metadata.ErrRateLimited) {
			return nil, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if c.log != nil {
			c.log.Debug("musicbrainz release-group size unavailable", "release_group", id.ProviderAlbumID,
				"error", err, "component", "musicbrainz")
		}
		return id, nil
	}
	rep := representativeRelease(releases)
	if rep.TrackCount > 0 {
		id.AlbumTrackCount = rep.TrackCount
	}
	return id, nil
}

// recordingIdentity maps a recording lookup onto the provider-neutral identity.
// The release-group MBID is the album id because MusicBrainz discovery and
// GetAlbumTracks resolve releases through their release group.
func recordingIdentity(recording *RecordingInfo, requestedISRC string) *discovery.TrackIdentity {
	release, medium, track := bestRecordingRelease(recording.Releases)
	if release == nil || release.ReleaseGroup == nil || release.ReleaseGroup.ID == "" {
		return nil
	}
	duration := recording.Length
	if duration <= 0 && track != nil {
		duration = track.Length
	}
	isrc := matching.NormalizeISRC(requestedISRC)
	if isrc == "" {
		isrc = matching.NormalizeISRC(firstISRC(recording.ISRCs))
	}
	albumTitle := release.ReleaseGroup.Title
	if albumTitle == "" {
		albumTitle = release.Title
	}
	discNumber := 1
	if medium != nil && medium.Position > 0 {
		discNumber = medium.Position
	}
	trackNumber := 0
	if track != nil {
		trackNumber = track.Position
	}
	return &discovery.TrackIdentity{
		ProviderName:    pluginName,
		ProviderTrackID: recording.ID,
		ProviderAlbumID: release.ReleaseGroup.ID,
		Title:           recording.Title,
		ArtistName:      joinArtistCredit(recording.ArtistCredit),
		AlbumTitle:      albumTitle,
		AlbumType:       strings.ToLower(release.ReleaseGroup.PrimaryType),
		// Filled from the release-group media counts by
		// fillReleaseGroupTrackCount; the recording lookup only returns the
		// medium that carries the recording.
		AlbumTrackCount: 0,
		TrackNumber:     trackNumber,
		DiscNumber:      discNumber,
		DurationMs:      duration,
		ISRC:            isrc,
	}
}

// bestRecordingRelease picks the deterministic release context for one
// recording: official status first, then earliest date, then release-group type,
// then release id. It also returns the matching medium/track. Releases without a
// usable release group are skipped so a sparse release cannot hide a usable one.
func bestRecordingRelease(releases []RecordingRelease) (*RecordingRelease, *RecordingMedium, *RecordingTrack) {
	var best *RecordingRelease
	var bestMedium *RecordingMedium
	var bestTrack *RecordingTrack
	for i := range releases {
		release := &releases[i]
		if release.ReleaseGroup == nil || release.ReleaseGroup.ID == "" {
			continue
		}
		medium, track := recordingTrack(release)
		if medium == nil || track == nil {
			continue
		}
		if best == nil || preferRecordingRelease(release, best) {
			best, bestMedium, bestTrack = release, medium, track
		}
	}
	return best, bestMedium, bestTrack
}

func recordingTrack(release *RecordingRelease) (*RecordingMedium, *RecordingTrack) {
	for i := range release.Media {
		if len(release.Media[i].Tracks) > 0 {
			return &release.Media[i], &release.Media[i].Tracks[0]
		}
	}
	return nil, nil
}

func preferRecordingRelease(candidate, current *RecordingRelease) bool {
	candidateOfficial := strings.EqualFold(strings.TrimSpace(candidate.Status), "Official")
	currentOfficial := strings.EqualFold(strings.TrimSpace(current.Status), "Official")
	if candidateOfficial != currentOfficial {
		return candidateOfficial
	}
	if d := compareReleaseDates(candidate.Date, current.Date); d != 0 {
		return d < 0
	}
	if candidate.ReleaseGroup != nil && current.ReleaseGroup != nil {
		ct := strings.ToLower(candidate.ReleaseGroup.PrimaryType)
		cu := strings.ToLower(current.ReleaseGroup.PrimaryType)
		if ct != cu {
			return ct < cu
		}
	}
	return candidate.ID < current.ID
}

func hasISRC(isrcs []string, want string) bool {
	want = matching.NormalizeISRC(want)
	if want == "" {
		return false
	}
	for _, isrc := range isrcs {
		if matching.NormalizeISRC(isrc) == want {
			return true
		}
	}
	return false
}
