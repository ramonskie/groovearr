package catalog

import (
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
)

// MergedTrack is one row of the album-detail view: a materialized expected
// track plus its local download status.
type MergedTrack struct {
	DiscNumber  int
	TrackNumber int
	Title       string
	ArtistName  string
	DurationMs  int64
	ISRC        string
	Downloaded  bool
	Local       *domain.Track // nil when no local file matches
}

// discTrackKey is the normalized (disc, track) lookup key. Zero-or-absent disc
// numbers are normalized to 1 so a provider row tagged disc 1 still matches a
// local file whose tag omitted the disc entirely (recorded as 0).
type discTrackKey struct {
	disc   int
	number int
}

// normalize lowercases, trims and collapses internal whitespace so titles that
// differ only in spacing or case compare equal. It is deliberately local to the
// catalog package (no dependency on internal/strutil or internal/api).
func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// normalizeDisc maps an absent/zero disc number to 1; positive values are
// returned unchanged.
func normalizeDisc(disc int) int {
	if disc <= 0 {
		return 1
	}
	return disc
}

// MergeTracks merges materialized expected tracks with local library tracks.
// It is the back-compat single-pool form of MergeTracksScoped: the whole local
// set is treated as this album's own tracks, so (disc, track) and title
// matching may use any local track.
func MergeTracks(materialized []AlbumTrack, local []domain.Track) []MergedTrack {
	return MergeTracksScoped(materialized, local, nil)
}

// MergeTracksScoped merges materialized expected tracks with local library
// tracks, scoping the weaker identity signals to the album under view. Pure: no
// IO, no globals, no DB.
//
// Matching precedence per materialized track:
//
//  1. ISRC exact (when the materialized track has a non-empty ISRC) — matched
//     against the artist-wide pool (albumLocal ∪ artistLocal), because an ISRC
//     is globally unique and a track may legitimately live under another album.
//  2. normalized (disc_number, track_number) when track_number > 0 — matched
//     only against albumLocal, so a position from a different album cannot
//     mark this album's row downloaded.
//  3. normalized title — matched only against albumLocal, for the same reason.
//
// Output preserves the input order of materialized. A local track can satisfy
// at most one materialized entry (first match wins); duplicate tracks are
// consumed in input order, so the earliest is used first. Album-local entries
// take precedence over artist-wide duplicates of the same track id.
//
// Matching is index-driven: the combined pool is indexed once by ISRC, and the
// album-local subset is indexed by (disc, track) and normalized title. A shared
// "consumed" marker keeps the indexes from handing the same local track to a
// later entry.
func MergeTracksScoped(materialized []AlbumTrack, albumLocal, artistLocal []domain.Track) []MergedTrack {
	out := make([]MergedTrack, 0, len(materialized))
	if len(materialized) == 0 {
		return out
	}

	// Build the combined pool: album-local tracks first (so they win dedup),
	// then the artist-wide tracks. isAlbum marks which pool entries may satisfy
	// a (disc,track)/title match.
	pool := make([]domain.Track, 0, len(albumLocal)+len(artistLocal))
	isAlbum := make([]bool, 0, len(albumLocal)+len(artistLocal))
	seen := make(map[int64]struct{}, len(albumLocal)+len(artistLocal))
	appendPool := func(tracks []domain.Track, album bool) {
		for i := range tracks {
			t := tracks[i]
			if t.ID != 0 {
				if _, dup := seen[t.ID]; dup {
					continue
				}
				seen[t.ID] = struct{}{}
			}
			pool = append(pool, t)
			isAlbum = append(isAlbum, album)
		}
	}
	appendPool(albumLocal, true)
	appendPool(artistLocal, false)

	byISRC := make(map[string][]int, len(pool))
	albumByDiscTrack := make(map[discTrackKey][]int, len(albumLocal))
	albumByTitle := make(map[string][]int, len(albumLocal))
	for i := range pool {
		t := &pool[i]
		if t.ISRC != "" {
			byISRC[t.ISRC] = append(byISRC[t.ISRC], i)
		}
		if !isAlbum[i] {
			continue
		}
		if t.TrackNumber > 0 {
			k := discTrackKey{disc: normalizeDisc(t.DiscNumber), number: t.TrackNumber}
			albumByDiscTrack[k] = append(albumByDiscTrack[k], i)
		}
		if key := normalize(t.Title); key != "" {
			albumByTitle[key] = append(albumByTitle[key], i)
		}
	}

	consumed := make([]bool, len(pool))

	for _, m := range materialized {
		entry := MergedTrack{
			DiscNumber:  m.DiscNumber,
			TrackNumber: m.TrackNumber,
			Title:       m.Title,
			ArtistName:  m.ArtistName,
			DurationMs:  m.DurationMs,
			ISRC:        m.ISRC,
		}
		if idx, ok := matchLocalScoped(m, byISRC, albumByDiscTrack, albumByTitle, consumed); ok {
			entry.Downloaded = true
			entry.Local = &pool[idx]
		}
		out = append(out, entry)
	}

	return out
}

// matchLocalScoped finds the first unconsumed local track for one materialized
// row, trying ISRC against the artist-wide pool, then (disc, track), then
// normalized title against the album-local subset only, and marking the matched
// track consumed so no later row can reuse it.
func matchLocalScoped(
	m AlbumTrack,
	byISRC map[string][]int,
	albumByDiscTrack map[discTrackKey][]int,
	albumByTitle map[string][]int,
	consumed []bool,
) (int, bool) {
	if m.ISRC != "" {
		if idx, ok := take(byISRC[m.ISRC], consumed); ok {
			return idx, true
		}
	}
	if m.TrackNumber > 0 {
		k := discTrackKey{disc: normalizeDisc(m.DiscNumber), number: m.TrackNumber}
		if idx, ok := take(albumByDiscTrack[k], consumed); ok {
			return idx, true
		}
	}
	if key := normalize(m.Title); key != "" {
		if idx, ok := take(albumByTitle[key], consumed); ok {
			return idx, true
		}
	}
	return 0, false
}

// take returns the first unconsumed local index from ids and marks it consumed.
func take(ids []int, consumed []bool) (int, bool) {
	for _, id := range ids {
		if !consumed[id] {
			consumed[id] = true
			return id, true
		}
	}
	return 0, false
}
