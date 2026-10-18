package identity

import (
	"context"
	"sort"
	"strings"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
)

// scoredCandidate is one provider identity with its evidence and score.
type scoredCandidate struct {
	identity *discovery.TrackIdentity
	signals  matching.CandidateSignals
	score    int
}

// candidates collects and deduplicates the identity candidates for one local
// track. A provider-specific external id is tried first; when it yields a
// non-auto candidate the ISRC is also tried, so a bad stored id cannot hide a
// correct ISRC (and vice versa).
func (r *Resolver) candidates(ctx context.Context, album catalog.UnmatchedAlbum, track domain.Track,
	localTrackCount int, providers []discovery.TrackIdentityProvider) []scoredCandidate {
	seen := make(map[string]*scoredCandidate)
	for _, p := range providers {
		name := p.Name()
		if r.cooldown != nil && r.cooldown.CoolingDown(name) {
			continue
		}
		pid := strings.TrimSpace(track.ExternalIDs[name])
		if pid != "" {
			id, err := r.getTrackIdentity(ctx, p, pid)
			if err != nil {
				// Only a parked provider (rate limit) skips the ISRC branch;
				// any other failure (bad/expired id, 404, timeout) must fall
				// through so a correct ISRC can still resolve the track.
				if r.handleProviderError(name, err) {
					continue
				}
			} else if id != nil {
				c := r.score(album, track, localTrackCount, id)
				addCandidate(seen, c)
				if autoAllowed(c) {
					continue
				}
			}
		}
		isrc := strings.TrimSpace(track.ISRC)
		if isrc == "" {
			continue
		}
		id, err := r.lookupTrackByISRC(ctx, p, isrc)
		if err != nil {
			r.handleProviderError(name, err)
			continue
		}
		if id != nil {
			addCandidate(seen, r.score(album, track, localTrackCount, id))
		}
	}
	out := make([]scoredCandidate, 0, len(seen))
	for _, c := range seen {
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

func addCandidate(seen map[string]*scoredCandidate, c scoredCandidate) {
	key := c.identity.ProviderName + "\x00" + c.identity.ProviderTrackID
	if prev, ok := seen[key]; ok {
		prev.signals.ISRCExact = prev.signals.ISRCExact || c.signals.ISRCExact
		prev.signals.ProviderTrackIDExact = prev.signals.ProviderTrackIDExact || c.signals.ProviderTrackIDExact
		prev.signals.TitleMatches = prev.signals.TitleMatches || c.signals.TitleMatches
		prev.signals.ArtistMatches = prev.signals.ArtistMatches || c.signals.ArtistMatches
		prev.signals.DurationKnown = prev.signals.DurationKnown || c.signals.DurationKnown
		prev.signals.DurationWithinTolerance = prev.signals.DurationWithinTolerance || c.signals.DurationWithinTolerance
		prev.signals.AlbumTitleMatches = prev.signals.AlbumTitleMatches || c.signals.AlbumTitleMatches
		prev.signals.TrackCountMatches = prev.signals.TrackCountMatches || c.signals.TrackCountMatches
		prev.signals.TrackNumberMatches = prev.signals.TrackNumberMatches || c.signals.TrackNumberMatches
		prev.score = matching.CandidateScore(prev.signals)
		// Prefer a concrete release link when the duplicate identities differ
		// (a track appears on both a single and an album).
		if prev.identity.ProviderAlbumID == "" && c.identity.ProviderAlbumID != "" {
			prev.identity = c.identity
		}
		return
	}
	cp := c
	seen[key] = &cp
}

func (r *Resolver) score(album catalog.UnmatchedAlbum, track domain.Track, localTrackCount int,
	id *discovery.TrackIdentity) scoredCandidate {
	titleKey := matching.NormalizeTitle(track.Title)
	albumKey := matching.NormalizeTitle(album.Title)
	isrc := strings.TrimSpace(track.ISRC)
	candISRC := strings.TrimSpace(id.ISRC)
	extID := strings.TrimSpace(track.ExternalIDs[id.ProviderName])
	candTrackID := strings.TrimSpace(id.ProviderTrackID)
	signals := matching.CandidateSignals{
		ISRCExact:               isrc != "" && strings.EqualFold(isrc, candISRC),
		ProviderTrackIDExact:    extID != "" && extID == candTrackID,
		TitleMatches:            titleKey != "" && titleKey == matching.NormalizeTitle(id.Title),
		ArtistMatches:           artistMatches(album.Artist, id.ArtistName),
		DurationKnown:           track.Duration > 0 && id.DurationMs > 0,
		DurationWithinTolerance: matching.DurationWithinTolerance(track.Duration, id.DurationMs, r.opts.DurationTolerance),
		AlbumTitleMatches:       albumKey != "" && albumKey == matching.NormalizeTitle(id.AlbumTitle),
		TrackCountMatches:       id.AlbumTrackCount > 0 && id.AlbumTrackCount == localTrackCount,
		TrackNumberMatches:      track.TrackNumber > 0 && id.TrackNumber == track.TrackNumber,
	}
	return scoredCandidate{identity: id, signals: signals, score: matching.CandidateScore(signals)}
}

// artistMatches treats an unknown local artist as "no contradiction" but
// rejects the compilation placeholder. A real mismatch is only auto-applied
// when duration confirms the recording (see autoAllowed).
func artistMatches(local, candidate string) bool {
	local = strings.TrimSpace(local)
	if local == "" {
		return true
	}
	if strings.EqualFold(local, "various artists") {
		return false
	}
	return matching.ArtistCompatible(local, candidate)
}

// autoAllowed applies the resolver's safety policy on top of CandidateScore:
// the title must agree, a known duration conflict always parks the candidate,
// and an artist mismatch needs a duration match before it can auto-apply. This
// keeps a wrong ISRC that happens to share a generic title out of the catalog.
func autoAllowed(c scoredCandidate) bool {
	if c.identity == nil || c.identity.ProviderTrackID == "" || c.identity.ProviderAlbumID == "" {
		return false
	}
	if c.score < matching.AutoApplyThreshold {
		return false
	}
	if !c.signals.TitleMatches {
		return false
	}
	if c.signals.DurationKnown && !c.signals.DurationWithinTolerance {
		return false
	}
	if !c.signals.ArtistMatches && !c.signals.DurationWithinTolerance {
		return false
	}
	return true
}

// pickBest returns the single highest-scoring candidate, or nil when two
// different identities tie at the top (ambiguity is a review case).
func pickBest(candidates []scoredCandidate) *scoredCandidate {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		if candidates[i].score != best.score {
			continue
		}
		if !sameIdentity(candidates[i].identity, best.identity) {
			return nil
		}
	}
	return best
}

func sameIdentity(a, b *discovery.TrackIdentity) bool {
	return a.ProviderName == b.ProviderName && a.ProviderTrackID == b.ProviderTrackID && a.ProviderAlbumID == b.ProviderAlbumID
}
