package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// Client implements metadata.Provider using the MusicBrainz public API.
// It provides cover art search (via Cover Art Archive), track enrichment
// (ISRC, genres, label, release date), artist image lookup (unavailable),
// and album track resolution for download plugins.
type Client struct {
	cfg       MusicBrainzConfig
	api       *APIClient
	log       *slog.Logger
	connected bool
	mu        sync.RWMutex // protects connected
}

// NewClient creates a MusicBrainz metadata provider.
func NewClient(cfg MusicBrainzConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg: cfg,
		api: NewAPIClient(cfg, logger),
		log: logger,
	}
}

// Compile-time interface checks.
var _ metadata.Provider = (*Client)(nil)
var _ metadata.ArtistNameProvider = (*Client)(nil)

// MusicBrainz is also a first-class discovery provider, so the catalog refresh
// engine (discovery.Registry.Configured()) can materialize MB-only albums.
var _ discovery.Provider = (*Client)(nil)
var _ discovery.ReleaseResolver = (*Client)(nil)

// ─── plugin.BasePlugin ─────────────────────────────────────────────────

func (c *Client) Name() string              { return pluginName }
func (c *Client) DisplayName() string       { return displayName }
func (c *Client) IsConfigured() bool        { return true } // no credentials required
func (c *Client) IsMetadataAvailable() bool { return true }

// logOpError logs an operation failure, choosing the level from the error. A
// rate-limit error (metadata.ErrRateLimited) is a routine, self-healing
// transient — the shared per-provider cooldown governs the retry — so it is
// logged at WARN to keep ERROR meaningful for real faults. Any other error stays
// at ERROR. Mirrors the API client's helper for the provider's own operations.
func (c *Client) logOpError(msg string, args ...any) {
	for _, a := range args {
		if err, ok := a.(error); ok && errors.Is(err, metadata.ErrRateLimited) {
			c.log.Warn(msg, args...)
			return
		}
	}
	c.log.Error(msg, args...)
}

// CapabilityStatus returns metadata + discovery status based on health check.
func (c *Client) CapabilityStatus() map[string]string {
	s := "configured"
	if c.Connected() {
		s = "connected"
	}
	return map[string]string{"metadata": s, "discovery": s}
}

func (c *Client) CheckConnection(ctx context.Context) error {
	_, err := c.api.SearchReleaseGroup(ctx, "test", "test")
	c.mu.Lock()
	if err != nil {
		c.connected = false
		c.mu.Unlock()
		c.logOpError("musicbrainz check connection failed", "error", err, "component", "musicbrainz")
		if errors.Is(err, metadata.ErrRateLimited) {
			return fmt.Errorf("musicbrainz: rate limited: %w", err)
		}
		return fmt.Errorf("musicbrainz: connectivity check failed: %w", err)
	}
	c.connected = true
	c.mu.Unlock()
	return nil
}

func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// ─── metadata.Provider ─────────────────────────────────────────────────

// SearchCover looks up album cover art by resolving the artist+album to a
// MusicBrainz release group, then constructing a Cover Art Archive URL.
// Returns nil, nil if no matching release group is found.
func (c *Client) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	rg, err := c.api.SearchReleaseGroup(ctx, artist, album)
	if err != nil {
		c.logOpError("musicbrainz search cover failed", "error", err, "artist", artist, "album", album, "component", "musicbrainz")
		return nil, err
	}
	if rg == nil {
		return nil, nil
	}

	return &metadata.CoverResult{
		ImageURL: fmt.Sprintf("https://coverartarchive.org/release-group/%s/front", rg.MBID),
		Width:    500,
		Height:   500,
		Source:   "coverartarchive",
		ThumbURL: fmt.Sprintf("https://coverartarchive.org/release-group/%s/front-250.jpg", rg.MBID),
	}, nil
}

// SearchArtistImage looks up an artist image. MusicBrainz does not host
// artist images directly — returns nil, nil.
func (c *Client) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}

// CanonicalArtistName resolves the canonical MusicBrainz spelling for an
// artist name. Returns "" when MusicBrainz has no matching artist.
func (c *Client) CanonicalArtistName(ctx context.Context, name string) (string, error) {
	res, err := c.api.SearchArtist(ctx, name)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.Name, nil
}

// SearchAlbum finds the album title for a track by searching MusicBrainz
// recordings via artist+title. Returns ("", nil) if no match found. A failure
// (including metadata.ErrRateLimited) is propagated so the caller can park the
// provider on the shared cooldown bucket (AGENTS §8).
func (c *Client) SearchAlbum(ctx context.Context, artist, title string) (string, error) {
	if artist == "" || title == "" {
		return "", nil
	}
	rg, err := c.api.SearchRecording(ctx, artist, title)
	if err != nil {
		c.logOpError("musicbrainz search album failed", "error", err, "artist", artist, "title", title, "component", "musicbrainz")
		return "", err
	}
	if rg == nil {
		return "", nil
	}
	return rg.Title, nil
}

// EnrichTrack fetches ISRC codes, genres, label, release date, and external
// IDs from MusicBrainz for the given track.
func (c *Client) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	// If the track already has a MusicBrainz release MBID, use it directly.
	releaseMBID := track.ExternalIDs["musicbrainz_release"]
	if releaseMBID != "" {
		return c.enrichByRelease(ctx, releaseMBID)
	}

	// Otherwise, search by artist + album.
	// We need the artist name and album title — if not available, skip.
	// Track doesn't have artist/album names directly, only IDs.
	// The enrichment pipeline passes these separately, or we use track metadata.
	// For now: if we have a MusicBrainz release group ID, look it up.
	rgMBID := track.ExternalIDs["musicbrainz_release_group"]
	if rgMBID != "" {
		// Lookup the release group's first release and enrich from there.
		// For simplicity, search for releases by release group.
		return c.enrichByReleaseGroup(ctx, rgMBID)
	}

	return nil, nil
}

// ─── discovery.ReleaseResolver ─────────────────────────────────────────

// ResolveTracksForCount resolves an album's expected tracklist from
// MusicBrainz: it finds the release group for artist+album, picks the release
// whose track count best matches fileCount (torrentTitle breaks ties), then
// looks up that release's full track listing. Returns the tracks and the
// resolved release MBID.
//
// When fileCount <= 0 there is no count to match against, so a deterministic
// representative release is chosen (see representativeRelease) rather than
// trusting MusicBrainz's unspecified release ordering.
func (c *Client) ResolveTracksForCount(ctx context.Context, artist, album string, fileCount int, torrentTitle string) ([]discovery.TrackInfo, string, error) {
	if artist == "" || album == "" {
		return nil, "", fmt.Errorf("musicbrainz: artist and album are required")
	}

	// 1. Find the release group for the album.
	rg, err := c.api.SearchReleaseGroup(ctx, artist, album)
	if err != nil {
		return nil, "", fmt.Errorf("musicbrainz: search: %w", err)
	}
	if rg == nil {
		return nil, "", fmt.Errorf("musicbrainz: no release group found for %q - %q", artist, album)
	}

	// 2. Pick a release. When a file count is known, use the search endpoint
	// (one call, all releases with track counts). Otherwise fall back to the
	// release-group lookup (first release only, no track counts).
	var best ReleaseGroupRelease
	if fileCount > 0 {
		allReleases, err := c.api.SearchReleasesByGroup(ctx, rg.MBID)
		if err != nil {
			return nil, "", fmt.Errorf("musicbrainz: search releases: %w", err)
		}
		if allReleases == nil {
			return nil, "", fmt.Errorf("musicbrainz: search API returned no data for group %s (release group may not be indexed)", rg.MBID)
		}
		if len(allReleases) == 0 {
			return nil, "", fmt.Errorf("musicbrainz: release group %s exists but has no indexed releases", rg.MBID)
		}
		best = pickBestMatchingRelease(allReleases, fileCount, torrentTitle)
	} else {
		rgReleases, err := c.api.LookupReleaseGroup(ctx, rg.MBID)
		if err != nil {
			return nil, "", fmt.Errorf("musicbrainz: release group: %w", err)
		}
		if len(rgReleases) == 0 {
			return nil, "", fmt.Errorf("musicbrainz: no releases in group %s", rg.MBID)
		}
		best = representativeRelease(rgReleases)
		if best.ID == "" {
			return nil, "", fmt.Errorf("musicbrainz: no usable release in group %s", rg.MBID)
		}
	}

	// 3. Fetch the chosen release's full track listing.
	info, err := c.api.LookupRelease(ctx, best.ID)
	if err != nil {
		return nil, "", fmt.Errorf("musicbrainz: release: %w", err)
	}
	if info == nil || len(info.Tracks) == 0 {
		return nil, "", fmt.Errorf("musicbrainz: no tracks in release %s", best.ID)
	}

	// 4. Map to neutral discovery tracks via the same mapper GetAlbumTracks
	// uses, so ProviderID and DiscNumber are populated consistently. Fall back
	// to the requested album artist when MusicBrainz credits no per-track artist.
	tracks := make([]discovery.TrackInfo, 0, len(info.Tracks))
	for _, t := range info.Tracks {
		ti := trackInfoFromReleaseTrack(t, info.Title)
		if ti.ArtistName == "" {
			ti.ArtistName = artist
		}
		tracks = append(tracks, ti)
	}

	return tracks, best.ID, nil
}

// pickBestMatchingRelease picks the release whose track count is closest to
// targetCount. When multiple releases tie, word overlap between torrentTitle
// and the release title/disambiguation breaks the tie.
//
// This mirrors the selection logic historically kept in the prowlarr package;
// it now lives with the ReleaseResolver implementation.
func pickBestMatchingRelease(releases []ReleaseGroupRelease, targetCount int, torrentTitle string) ReleaseGroupRelease {
	if len(releases) == 0 {
		return ReleaseGroupRelease{}
	}

	type scored struct {
		release   ReleaseGroupRelease
		trackDiff int
		wordScore int
	}

	candidates := make([]scored, 0, len(releases))
	torrentLower := strings.ToLower(torrentTitle)

	for _, r := range releases {
		diff := r.TrackCount - targetCount
		if diff < 0 {
			diff = -diff
		}

		score := 0
		titleLower := strings.ToLower(r.Title)
		disambigLower := strings.ToLower(r.Disambiguation)
		for _, word := range strings.Fields(torrentLower) {
			if len(word) < 3 {
				continue
			}
			if strings.Contains(titleLower, word) {
				score++
			}
			if strings.Contains(disambigLower, word) {
				score += 2
			}
		}

		candidates = append(candidates, scored{release: r, trackDiff: diff, wordScore: score})
	}

	// Closest track count first, highest word score breaks ties.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.trackDiff < best.trackDiff {
			best = c
		} else if c.trackDiff == best.trackDiff && c.wordScore > best.wordScore {
			best = c
		}
	}

	return best.release
}

// firstISRC returns the first non-empty ISRC, or "" when none are present.
func firstISRC(isrcs []string) string {
	for _, s := range isrcs {
		if s != "" {
			return s
		}
	}
	return ""
}

// ─── discovery.Provider ────────────────────────────────────────────────
//
// MusicBrainz is a first-class discovery provider so the catalog refresh engine
// (discovery.Registry.Configured()) can materialize MusicBrainz-only albums.
// Every call goes through the shared, rate-limited *APIClient, so the app-wide
// provider cooldown (AGENTS §8) rides the same bucket as the metadata path.
//
// The shared APIClient predates discovery and does not expose artist MBIDs
// (SearchArtist drops them) or an artist→release-groups browse; SearchArtists
// and GetArtistAlbums issue those two queries directly through c.api.apiGet so
// this subtask stays scoped to the MusicBrainz provider package.

// SearchAlbums maps an album query to a MusicBrainz release-group search and
// returns up to limit distinct results. The structured branch is taken whenever
// the query carries an artist OR a title (see discovery.AlbumQuery); the
// release-group MBID is the provider album id with its primary type as the album
// type.
//
//   - Structured (a usable q.Title is present): the caller carries the
//     artist/title boundary, so the fields are searched in ONE release-group
//     query — no candidate splitting and no word cap. The releasegroup term is
//     the title run through matching.TrimEditionSuffix, so a library title
//     carrying a delimited format suffix ("Échame la culpa - Single") queries the
//     bare release group the provider actually lists, while the title's case and
//     punctuation ("Don't Stop", "AC/DC") are kept for MusicBrainz to tokenize.
//     The artist:"…" term is included ONLY when q.Artist is non-empty, so a
//     title-only query (an album with no/unknown artist) still sends
//     releasegroup:"…" alone instead of an empty query. This is the path the
//     catalog refresh engine uses, and it must not guess the boundary.
//   - Free-text / no usable title: a single bare release-group search built from
//     the rendered phrase (SearchReleaseGroupsQuery) — one request, no
//     artist/title boundary guessing. The phrase is q.Query() (Artist, else
//     Title, else Text), so an artist-only AlbumQuery still searches instead of
//     no-oping. This replaces the old multi-request split heuristic, which cost
//     several requests per query.
func (c *Client) SearchAlbums(ctx context.Context, q discovery.AlbumQuery, limit int) ([]discovery.AlbumResult, error) {
	// Clamp before collectRankedAlbums pre-allocates its result slice, so a
	// caller-supplied huge limit cannot force a huge allocation.
	limit = clampAlbumSearchLimit(limit)

	artist := strings.TrimSpace(q.Artist)
	// Strip a trailing edition/format suffix without lowercasing or dropping
	// punctuation: MusicBrainz tokenizes the term, and keeping the title
	// verbatim ("Don't Stop", "AC/DC") improves recall.
	if term := matching.TrimEditionSuffix(strings.TrimSpace(q.Title)); term != "" {
		return c.collectRankedAlbums(ctx, artist, []string{term}, func(cctx context.Context, t string) ([]ReleaseGroupResult, error) {
			return c.api.SearchReleaseGroupsByTitle(cctx, artist, t, limit)
		}, limit)
	}

	// No usable title: fall back to the same rendered phrase every q.Query()
	// provider uses (Artist, else Title, else Text) as a single bare query, so an
	// artist-only AlbumQuery still searches instead of no-oping.
	query := q.Query()
	if query == "" {
		return nil, nil
	}
	return c.collectRankedAlbums(ctx, "", []string{query}, func(cctx context.Context, t string) ([]ReleaseGroupResult, error) {
		return c.api.SearchReleaseGroupsQuery(cctx, t, limit)
	}, limit)
}

// clampAlbumSearchLimit bounds a caller-supplied album-search limit to
// [1, maxReleaseGroupLimit]. It validates the limit before any slice is
// pre-allocated from it (a huge limit must not allocate megabytes) and keeps the
// eventual API request within MusicBrainz's response cap.
func clampAlbumSearchLimit(limit int) int {
	if limit <= 0 {
		return defaultReleaseGroupLimit
	}
	if limit > maxReleaseGroupLimit {
		return maxReleaseGroupLimit
	}
	return limit
}

// collectRankedAlbums runs one release-group search per term and merges the
// ranked results in order, deduplicated by release-group MBID and capped at
// limit. artist only fills a release group's missing artist credit.
//
// A rate-limit error (metadata.ErrRateLimited) ALWAYS propagates, even when some
// results were already collected, so the caller can park the provider app-wide
// through the sentinel (AGENTS §8). Any other transient error is best-effort:
// accumulated results are returned with a nil error when there are any, and the
// error propagates only when nothing was collected. Both current callers pass a
// single term; the accumulation rule keeps a future multi-term caller from
// losing earlier hits to a later transient failure.
func (c *Client) collectRankedAlbums(
	ctx context.Context,
	artist string,
	terms []string,
	search func(context.Context, string) ([]ReleaseGroupResult, error),
	limit int,
) ([]discovery.AlbumResult, error) {
	results := make([]discovery.AlbumResult, 0, limit)
	seen := make(map[string]bool, limit)
	for _, term := range terms {
		if len(results) >= limit {
			break
		}
		rgs, err := search(ctx, term)
		if err != nil {
			c.logOpError("musicbrainz search albums failed", "error", err, "term", term, "component", "musicbrainz")
			if errors.Is(err, metadata.ErrRateLimited) {
				// The sentinel must reach the caller so the shared cooldown can
				// park the provider, partial results or not.
				return results, err
			}
			if len(results) > 0 {
				// Best-effort partial: a transient per-term failure must not
				// discard the results already collected.
				return results, nil
			}
			return nil, err
		}
		for i := range rgs {
			if len(results) >= limit {
				break
			}
			if rgs[i].MBID == "" || seen[rgs[i].MBID] {
				continue
			}
			seen[rgs[i].MBID] = true
			results = append(results, albumResultFromReleaseGroup(&rgs[i], artist))
		}
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results, nil
}

// GetAlbumTracks returns a release group's tracklist from a representative
// release. providerAlbumID is a MusicBrainz release-group MBID; the
// representative release is chosen deterministically (official status, then
// earliest date) because MusicBrainz does not guarantee release ordering.
func (c *Client) GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]discovery.TrackInfo, error) {
	providerAlbumID = strings.TrimSpace(providerAlbumID)
	if providerAlbumID == "" {
		return nil, nil
	}
	releases, err := c.api.LookupReleaseGroup(ctx, providerAlbumID)
	if err != nil {
		c.logOpError("musicbrainz get album tracks release-group failed", "error", err, "releaseGroup", providerAlbumID, "component", "musicbrainz")
		return nil, fmt.Errorf("musicbrainz: release group %s: %w", providerAlbumID, err)
	}
	rep := representativeRelease(releases)
	if rep.ID == "" {
		return nil, nil // group not found or has no releases
	}
	info, err := c.api.LookupRelease(ctx, rep.ID)
	if err != nil {
		c.logOpError("musicbrainz get album tracks release failed", "error", err, "release", rep.ID, "component", "musicbrainz")
		return nil, fmt.Errorf("musicbrainz: release %s: %w", rep.ID, err)
	}
	if info == nil {
		return nil, nil
	}
	tracks := make([]discovery.TrackInfo, 0, len(info.Tracks))
	for _, t := range info.Tracks {
		tracks = append(tracks, trackInfoFromReleaseTrack(t, info.Title))
	}
	return tracks, nil
}

// SearchArtists searches MusicBrainz for artists matching a free-form query and
// returns up to limit results, each carrying the artist MBID as its provider id.
//
// This does not reuse c.api.SearchArtist: that helper is an exact-name
// canonicalizer (used by CanonicalArtistName) that returns one name and drops
// the MBID, whereas discovery browse needs the scored result set and the id.
func (c *Client) SearchArtists(ctx context.Context, query string, limit int) ([]discovery.ArtistSummary, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	data, err := c.api.apiGet(ctx, "/artist/", map[string]string{
		"query": fmt.Sprintf(`artist:"%s"`, escapeLucene(query)),
		"limit": strconv.Itoa(limit),
		"fmt":   "json",
	})
	if err != nil {
		c.logOpError("musicbrainz search artists failed", "error", err, "query", query, "component", "musicbrainz")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var resp struct {
		Artists []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"artists"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal search artists failed", "error", err, "component", "musicbrainz")
		return nil, err
	}
	out := make([]discovery.ArtistSummary, 0, len(resp.Artists))
	for _, a := range resp.Artists {
		out = append(out, discovery.ArtistSummary{
			ProviderID:   a.ID,
			ProviderName: pluginName,
			Name:         a.Name,
		})
	}
	return out, nil
}

// GetArtistAlbums returns an artist's release groups. providerArtistID is the
// MusicBrainz artist MBID returned by SearchArtists.
func (c *Client) GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]discovery.AlbumResult, error) {
	providerArtistID = strings.TrimSpace(providerArtistID)
	if providerArtistID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 25
	}
	data, err := c.api.apiGet(ctx, "/release-group/", map[string]string{
		"query": "arid:" + escapeLucene(providerArtistID),
		"limit": strconv.Itoa(limit),
		"fmt":   "json",
	})
	if err != nil {
		c.logOpError("musicbrainz get artist albums failed", "error", err, "artist", providerArtistID, "component", "musicbrainz")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var resp struct {
		ReleaseGroups []struct {
			ID               string              `json:"id"`
			Title            string              `json:"title"`
			FirstReleaseDate string              `json:"first-release-date"`
			PrimaryType      string              `json:"primary-type"`
			ArtistCredit     []artistCreditEntry `json:"artist-credit"`
		} `json:"release-groups"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal get artist albums failed", "error", err, "component", "musicbrainz")
		return nil, err
	}
	out := make([]discovery.AlbumResult, 0, len(resp.ReleaseGroups))
	for _, rg := range resp.ReleaseGroups {
		out = append(out, discovery.AlbumResult{
			ProviderID:   rg.ID,
			ProviderName: pluginName,
			ArtistName:   joinArtistCredit(rg.ArtistCredit),
			Title:        rg.Title,
			Year:         parseYear(rg.FirstReleaseDate),
			CoverURL:     releaseGroupCoverURL(rg.ID),
			Type:         strings.ToLower(rg.PrimaryType),
		})
	}
	return out, nil
}

// artistCreditEntry is one name + join phrase in a MusicBrainz artist credit.
type artistCreditEntry struct {
	Name       string `json:"name"`
	Joinphrase string `json:"joinphrase"`
}

// representativeRelease picks a deterministic stand-in release for a release
// group. MusicBrainz does not guarantee release ordering, so releases[0] can be
// a regional or alternate edition. Releases are ranked by:
//  1. official status (status == "Official"),
//  2. earliest release date (undated releases rank last),
//  3. release-group primary type,
//  4. original order, for stability.
func representativeRelease(releases []ReleaseGroupRelease) ReleaseGroupRelease {
	best := -1
	for i := range releases {
		if releases[i].ID == "" {
			continue
		}
		if best == -1 || preferRepresentative(releases[i], releases[best]) {
			best = i
		}
	}
	if best == -1 {
		return ReleaseGroupRelease{}
	}
	return releases[best]
}

// preferRepresentative reports whether candidate is a better stand-in release
// than current, using the ranking documented on representativeRelease.
func preferRepresentative(candidate, current ReleaseGroupRelease) bool {
	if co, cu := isOfficialRelease(candidate), isOfficialRelease(current); co != cu {
		return co
	}
	if d := compareReleaseDates(candidate.Date, current.Date); d != 0 {
		return d < 0
	}
	return strings.ToLower(candidate.PrimaryType) < strings.ToLower(current.PrimaryType)
}

// isOfficialRelease reports whether a release carries MusicBrainz's "Official"
// status, ignoring surrounding whitespace and case.
func isOfficialRelease(r ReleaseGroupRelease) bool {
	return strings.EqualFold(strings.TrimSpace(r.Status), "Official")
}

// compareReleaseDates orders MusicBrainz ISO-ish dates (YYYY, YYYY-MM,
// YYYY-MM-DD) ascending. An empty date sorts last. Returns -1, 0, or 1.
func compareReleaseDates(a, b string) int {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == b:
		return 0
	case a == "":
		return 1 // undated releases rank last
	case b == "":
		return -1
	default:
		return strings.Compare(a, b)
	}
}

// trackInfoFromReleaseTrack maps a MusicBrainz release track to a neutral
// discovery track. MusicBrainz's release lookup flattens all media into one
// track slice, but each ReleaseTrack carries its 1-based medium index as
// DiscNumber; default to 1 when it is absent.
func trackInfoFromReleaseTrack(t ReleaseTrack, albumTitle string) discovery.TrackInfo {
	disc := t.DiscNumber
	if disc < 1 {
		disc = 1
	}
	return discovery.TrackInfo{
		ProviderID:  releaseTrackID(t),
		ArtistName:  t.Artist,
		AlbumTitle:  albumTitle,
		Title:       t.Title,
		TrackNumber: t.Position,
		DiscNumber:  disc,
		DurationMs:  t.Length,
		ISRC:        firstISRC(t.ISRCs),
	}
}

// releaseTrackID returns a provider-scoped track identifier. The shared
// APIClient does not surface the recording MBID from a release lookup, so the
// release-scoped track "number" (e.g. "1", "A1") is the best stable handle;
// fall back to the numeric position when the number is absent.
func releaseTrackID(t ReleaseTrack) string {
	if t.Number != "" {
		return t.Number
	}
	return strconv.Itoa(t.Position)
}

// albumResultFromReleaseGroup maps a release-group search hit to a neutral
// album result. It prefers the release group's own artist credit (a tribute or
// karaoke hit keeps its distinct artist) and falls back to the artist the caller
// searched for when the release group carries no credit.
func albumResultFromReleaseGroup(rg *ReleaseGroupResult, artist string) discovery.AlbumResult {
	artistName := rg.ArtistName
	if artistName == "" {
		artistName = artist
	}
	return discovery.AlbumResult{
		ProviderID:   rg.MBID,
		ProviderName: pluginName,
		ArtistName:   artistName,
		Title:        rg.Title,
		Year:         parseYear(rg.FirstReleaseDate),
		CoverURL:     releaseGroupCoverURL(rg.MBID),
		Type:         strings.ToLower(rg.PrimaryType),
	}
}

// joinArtistCredit flattens a MusicBrainz artist credit into one display name,
// inserting each entry's join phrase (e.g. " & ", " feat. ").
func joinArtistCredit(credit []artistCreditEntry) string {
	var b strings.Builder
	for i, c := range credit {
		if i > 0 {
			b.WriteString(credit[i-1].Joinphrase)
		}
		b.WriteString(c.Name)
	}
	return b.String()
}

// releaseGroupCoverURL is the front cover for a release group on the Cover Art
// Archive (mirrors SearchCover).
func releaseGroupCoverURL(rgMBID string) string {
	if rgMBID == "" {
		return ""
	}
	return fmt.Sprintf("https://coverartarchive.org/release-group/%s/front-250", rgMBID)
}

// parseYear extracts the leading 4-digit year from a MusicBrainz date
// (e.g. "1991-09-24", "1991"). Returns 0 when the date has no usable year.
func parseYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return year
}

// ─── Internal helpers ──────────────────────────────────────────────────

func (c *Client) enrichByRelease(ctx context.Context, releaseMBID string) (*metadata.TrackMetadata, error) {
	info, err := c.api.LookupRelease(ctx, releaseMBID)
	if err != nil {
		c.logOpError("musicbrainz enrich by release failed", "error", err, "releaseMBID", releaseMBID, "component", "musicbrainz")
		return nil, err
	}
	if info == nil {
		return nil, nil
	}

	return releaseInfoToMetadata(info), nil
}

func (c *Client) enrichByReleaseGroup(ctx context.Context, rgMBID string) (*metadata.TrackMetadata, error) {
	// Release group → release resolution requires browsing the release list.
	// Placeholder for now — will be implemented when the enrichment pipeline
	// supplies artist+album names for MBID lookups.
	return nil, nil
}

func releaseInfoToMetadata(info *ReleaseInfo) *metadata.TrackMetadata {
	m := &metadata.TrackMetadata{
		ReleaseDate: info.Date,
		Label:       info.Label,
		ExternalIDs: map[string]string{
			"musicbrainz_release":       info.MBID,
			"musicbrainz_release_group": info.ReleaseGroupID,
		},
	}
	if len(info.Genres) > 0 {
		m.Genres = info.Genres
	}
	if len(info.ISRCs) > 0 {
		// ISRCs are aggregated across all tracks in the release.
		// Use the first as a best-guess — when track-number matching is available,
		// this should look up the ISRC for the specific track number.
		m.ISRC = info.ISRCs[0]
	}
	return m
}
