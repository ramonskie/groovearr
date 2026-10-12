package refresh

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/domain"
)

// WarmTier is the priority class of an album in a warm-library pass. A lower
// value is drained first: new > never-materialized > incomplete > complete.
type WarmTier int

const (
	// WarmTierNew means the album has never been catalogued at all (no
	// album_metadata row): a freshly added album the metadata catalog has not
	// seen yet. This is the highest priority.
	WarmTierNew WarmTier = iota
	// WarmTierNeverMaterialized means the album is catalogued but no provider
	// tracklist has been materialized for it yet.
	WarmTierNeverMaterialized
	// WarmTierIncomplete means a provider tracklist exists but the local library
	// holds fewer files than the provider knows about.
	WarmTierIncomplete
	// WarmTierComplete means the local library holds at least as many files as
	// the provider tracklist. Complete albums are excluded from a pass by
	// default — re-materializing them would spend provider calls for no gain.
	WarmTierComplete
)

// WarmCandidate is the pure, testable input for tiering one album. It carries an
// album id plus the three facts the tier depends on and no store handle, so the
// classification can be reasoned about (and tested) without any IO.
type WarmCandidate struct {
	AlbumID int64
	// HasMetadata is true when album_metadata has a row for the album.
	HasMetadata bool
	// ProviderTrackCount is the number of materialized provider-origin rows.
	ProviderTrackCount int
	// LocalTrackCount is the number of local files on disk for the album.
	LocalTrackCount int
	// RefreshStatus is the album_metadata status ("" when there is no row).
	RefreshStatus catalog.RefreshStatus
	// StatusUpdatedAt is when that status was last written. Together with
	// RefreshStatus it decides whether a terminal unmatched/failed album is
	// still inside its long retry window.
	StatusUpdatedAt time.Time
}

// Excluded reports whether the candidate must be left out of a warm pass: a
// terminal unmatched/failed album whose long retry has not yet elapsed.
// Re-warming such an album would spend a provider call and re-arm a queue row
// the engine deliberately parked, causing exactly the churn the 90-day retry
// exists to prevent (catalog.UnmatchedRetry). A zero StatusUpdatedAt (or a
// non-terminal status) is never excluded.
func (c WarmCandidate) Excluded(now time.Time) bool {
	if !c.RefreshStatus.IsTerminal() || c.StatusUpdatedAt.IsZero() {
		return false
	}
	return now.Sub(c.StatusUpdatedAt) < catalog.UnmatchedRetry
}

// Tier classifies the candidate. A known provider tracklist wins: complete when
// the local library is not short, otherwise incomplete. With no provider
// tracklist, a catalogued album is never-materialized and an uncatalogued one is
// new.
func (c WarmCandidate) Tier() WarmTier {
	switch {
	case c.ProviderTrackCount > 0 && c.LocalTrackCount >= c.ProviderTrackCount:
		return WarmTierComplete
	case c.ProviderTrackCount > 0:
		return WarmTierIncomplete
	case c.HasMetadata:
		return WarmTierNeverMaterialized
	default:
		return WarmTierNew
	}
}

// WarmOptions bounds one warm-library pass. Zero fields fall back to
// DefaultWarmOptions so a caller sets only what it cares about. Subtask 14 wires
// these from the live config closure; this package never caches config (AGENTS
// §9).
type WarmOptions struct {
	// Limit is the maximum number of albums returned — and therefore warmed — in
	// one pass. It bounds provider work per invocation; a larger library is
	// covered by re-triggering (candidates are recomputed).
	Limit int
	// ScanLimit caps how many albums are classified per pass. It bounds the
	// catalog/local read cost so a huge library cannot make a single pass
	// unbounded.
	ScanLimit int
	// IncludeComplete includes complete albums. Off by default.
	IncludeComplete bool

	// now is a test seam; nil means time.Now.
	now func() time.Time
}

// DefaultWarmOptions returns the bounds used when a caller leaves them unset.
// ScanLimit is generous enough to classify the whole of a large production
// library (live env ~8k albums) in one pass while still being a hard cap, so a
// pathological library cannot make a single pass unbounded.
func DefaultWarmOptions() WarmOptions {
	return WarmOptions{
		Limit:     50,
		ScanLimit: 10000,
	}
}

// withDefaults fills every unset field from DefaultWarmOptions.
func (o WarmOptions) withDefaults() WarmOptions {
	d := DefaultWarmOptions()
	if o.Limit <= 0 {
		o.Limit = d.Limit
	}
	if o.ScanLimit <= 0 {
		o.ScanLimit = d.ScanLimit
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o
}

// WarmStore is the catalog read slice the warm-library pass needs. It is a
// subset of the catalog Store, declared here so a caller (and a test) depends
// only on what it uses (AGENTS §3). *catalog/sqlite.Store satisfies it.
type WarmStore interface {
	ListAlbumsDueForRefresh(ctx context.Context, olderThan time.Time, limit int) ([]int64, error)
	GetAlbumMetadata(ctx context.Context, albumID int64) (*catalog.AlbumMetadata, error)
	GetAlbumTracks(ctx context.Context, albumID int64) ([]catalog.AlbumTrack, error)
}

// WarmLibrary is the local library read slice the pass needs: the files on disk
// per album. *library/sqlite.Store satisfies it.
type WarmLibrary interface {
	GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error)
}

// WarmCandidates returns the album ids that need warming, priority-ordered
// (new > never-materialized > incomplete > complete). Complete albums are
// excluded unless opts.IncludeComplete.
//
// It reads only the catalog and the local library — no provider is ever called
// here, so selecting candidates never spends a provider call. The pass is
// bounded: at most opts.ScanLimit albums are classified and at most opts.Limit
// ids are returned. Recomputing candidates on every invocation is what makes
// the job resumable — an album materialized on an earlier run is now complete
// and drops out, so a persisted cursor is unnecessary.
func WarmCandidates(ctx context.Context, store WarmStore, lib WarmLibrary, opts WarmOptions) ([]int64, error) {
	opts = opts.withDefaults()

	// Passing "now" as the cutoff lists every album that is not already
	// refreshed in the future — i.e. all albums — because the tier filter below
	// is what excludes complete ones. Enumeration is cheap; classification tells
	// us which actually need a provider call.
	ids, err := store.ListAlbumsDueForRefresh(ctx, opts.now(), opts.ScanLimit)
	if err != nil {
		return nil, fmt.Errorf("refresh: warm candidates: list albums: %w", err)
	}

	cands := make([]WarmCandidate, 0, len(ids))
	for _, id := range ids {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		c, err := classifyWarm(ctx, store, lib, id)
		if err != nil {
			return nil, err
		}
		cands = append(cands, c)
	}
	return selectWarm(cands, opts), nil
}

// classifyWarm reads the three facts a WarmCandidate needs for one album. It
// counts provider-origin materialized rows (not all rows) so library-origin rows
// written by the import/scan path do not masquerade as a provider tracklist.
func classifyWarm(ctx context.Context, store WarmStore, lib WarmLibrary, albumID int64) (WarmCandidate, error) {
	meta, err := store.GetAlbumMetadata(ctx, albumID)
	if err != nil {
		return WarmCandidate{}, fmt.Errorf("refresh: warm: album %d metadata: %w", albumID, err)
	}
	tracks, err := store.GetAlbumTracks(ctx, albumID)
	if err != nil {
		return WarmCandidate{}, fmt.Errorf("refresh: warm: album %d tracks: %w", albumID, err)
	}
	providerCount := 0
	for i := range tracks {
		if tracks[i].Origin == catalog.OriginProvider {
			providerCount++
		}
	}
	local, err := lib.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		return WarmCandidate{}, fmt.Errorf("refresh: warm: album %d local tracks: %w", albumID, err)
	}
	cand := WarmCandidate{
		AlbumID:            albumID,
		HasMetadata:        meta != nil,
		ProviderTrackCount: providerCount,
		LocalTrackCount:    len(local),
	}
	if meta != nil {
		cand.RefreshStatus = meta.RefreshStatus
		cand.StatusUpdatedAt = meta.UpdatedAt
	}
	return cand, nil
}

// selectWarm orders candidates by tier (then album id, for a deterministic
// order) and returns the first opts.Limit ids, dropping complete albums unless
// asked. Pure: no IO, same input yields the same output.
func selectWarm(cands []WarmCandidate, opts WarmOptions) []int64 {
	sort.SliceStable(cands, func(i, j int) bool {
		ti, tj := cands[i].Tier(), cands[j].Tier()
		if ti != tj {
			return ti < tj
		}
		return cands[i].AlbumID < cands[j].AlbumID
	})

	now := opts.now
	if now == nil {
		now = time.Now
	}
	nowTime := now()
	out := make([]int64, 0, opts.Limit)
	for _, c := range cands {
		if c.Excluded(nowTime) {
			continue
		}
		if c.Tier() == WarmTierComplete && !opts.IncludeComplete {
			continue
		}
		out = append(out, c.AlbumID)
		if len(out) >= opts.Limit {
			break
		}
	}
	return out
}
