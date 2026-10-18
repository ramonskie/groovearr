package identity

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
)

// tieDurationTolerance is the extra check used only when two providers return
// the same normalized title and artist at the same score. It is deliberately
// tighter than the scoring tolerance: an equivalent-tie shortcut must not merge
// two different edits of the same song.
const tieDurationTolerance = 2 * time.Second

// scoredCandidate is one provider identity with its evidence and score.
type scoredCandidate struct {
	identity *discovery.TrackIdentity
	signals  matching.CandidateSignals
	score    int
	// rank is the position of the provider in the shared configured order.
	// It is the deterministic tie-break for equivalent recordings, never a
	// provider name (AGENTS §1).
	rank int
}

// candidates collects and deduplicates the identity candidates for one local
// track. A provider-specific external id is tried first; when it yields a
// non-auto candidate the ISRC is also tried, so a bad stored id cannot hide a
// correct ISRC (and vice versa).
func (r *Resolver) candidates(ctx context.Context, album catalog.UnmatchedAlbum, track domain.Track,
	localTrackCount int, providers []discovery.TrackIdentityProvider) []scoredCandidate {
	seen := make(map[string]*scoredCandidate)
	for rank, p := range providers {
		if err := ctx.Err(); err != nil {
			break
		}
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
				c.rank = rank
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
			c := r.score(album, track, localTrackCount, id)
			c.rank = rank
			addCandidate(seen, c)
		}
	}
	out := make([]scoredCandidate, 0, len(seen))
	for _, c := range seen {
		out = append(out, *c)
	}
	// Equal scores keep the shared provider order (the earlier provider wins)
	// instead of inheriting map iteration order. That makes an equivalent
	// cross-provider tie deterministic without naming a provider here.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		// Same provider and score: use the provider track id as a stable
		// final key. The review list must not reshuffle on every refresh.
		if out[i].identity.ProviderTrackID != out[j].identity.ProviderTrackID {
			return out[i].identity.ProviderTrackID < out[j].identity.ProviderTrackID
		}
		return out[i].identity.ProviderAlbumID < out[j].identity.ProviderAlbumID
	})
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
		if c.rank < prev.rank {
			prev.rank = c.rank
		}
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
// different recordings tie at the top (ambiguity is a review case). Candidates
// with the same score and the same recording are equivalent: the slice is
// already ordered by provider rank, so keeping the first one lets the configured
// provider order decide which title spelling/format materializes.
func pickBest(candidates []scoredCandidate) *scoredCandidate {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		if candidates[i].score != best.score {
			continue
		}
		if sameIdentity(candidates[i].identity, best.identity) {
			continue
		}
		if equivalentRecordings(candidates[i].identity, best.identity) {
			continue
		}
		return nil
	}
	return best
}

func sameIdentity(a, b *discovery.TrackIdentity) bool {
	return a.ProviderName == b.ProviderName && a.ProviderTrackID == b.ProviderTrackID && a.ProviderAlbumID == b.ProviderAlbumID
}

// equivalentRecordings reports whether two provider identities describe the
// same recording. It is used only to break exact-score ties.
//
// The same non-empty ISRC is authoritative. If both sides carry an ISRC and
// they differ, the recordings are NOT equivalent even when the title matches:
// a stale/wrong stored id must stay a review case. Without ISRC evidence, the
// normalized title and artist must agree AND both durations must be known and
// within tieDurationTolerance. An unknown duration is not positive equivalence
// evidence, so the tie stays ambiguous.
func equivalentRecordings(a, b *discovery.TrackIdentity) bool {
	if a == nil || b == nil {
		return false
	}
	aISRC := strings.TrimSpace(a.ISRC)
	bISRC := strings.TrimSpace(b.ISRC)
	if aISRC != "" && bISRC != "" {
		return strings.EqualFold(aISRC, bISRC)
	}
	aTitle := matching.NormalizeTitle(a.Title)
	bTitle := matching.NormalizeTitle(b.Title)
	if aTitle == "" || aTitle != bTitle {
		return false
	}
	if !matching.ArtistCompatible(a.ArtistName, b.ArtistName) {
		return false
	}
	if a.DurationMs <= 0 || b.DurationMs <= 0 {
		return false
	}
	return matching.DurationWithinTolerance(a.DurationMs, b.DurationMs, tieDurationTolerance)
}
