package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/strutil"
)

const defaultBaseURL = "https://musicbrainz.org/ws/2"

// defaultMinInterval paces requests to the MusicBrainz API. MusicBrainz
// enforces a hard 1 request/second per source IP and answers an all-or-nothing
// 503 above it, regardless of User-Agent. A pacer sitting at exactly 1s rides
// that boundary, so any sub-millisecond clock or scheduling skew can trip the
// limit; 1100ms keeps ~10% headroom. This is the client's own steady-state
// pacing only — the shared, reactive provider cooldown (AGENTS §8) stays the
// app-wide enforcement on top and is intentionally not weakened.
const defaultMinInterval = 1100 * time.Millisecond

// Release-group search window. MusicBrainz caps a single search response at
// maxReleaseGroupLimit results; a caller that asks for none gets the smaller
// defaultReleaseGroupLimit ranked window.
const (
	defaultReleaseGroupLimit = 5
	maxReleaseGroupLimit     = 100
)

// maxResponseBytes bounds a single MusicBrainz response body so a malformed or
// hostile upstream cannot stream unbounded bytes into memory.
const maxResponseBytes = 16 << 20 // 16 MiB

// ─── Public types ──────────────────────────────────────────────────────

// ReleaseGroupResult holds the key fields from a release group search result.
type ReleaseGroupResult struct {
	MBID             string
	Title            string
	FirstReleaseDate string
	PrimaryType      string
	ArtistName       string // release group's own artist credit ("" when absent)
}

// ReleaseInfo holds enriched release data from a lookup.
type ReleaseInfo struct {
	MBID           string
	Title          string
	Date           string
	Country        string
	ReleaseGroupID string
	Label          string
	Genres         []string
	ISRCs          []string // all ISRCs across all tracks
	Tracks         []ReleaseTrack
}

// ReleaseTrack represents a single track within a release lookup.
type ReleaseTrack struct {
	Position    int    // 1-based track position (parsed from Number)
	Number      string // display number from MusicBrainz (e.g. "1", "A1")
	Title       string
	Length      int64  // milliseconds
	Artist      string // per-track artist credit (for compilations)
	ISRCs       []string
	DiscNumber  int    // 1-based medium/disc index within the release
	RecordingID string // global recording MBID (used for track identity)
}

// ISRCRecording is one recording returned by an ISRC lookup.
type ISRCRecording struct {
	ID             string
	Title          string
	Length         int64
	ISRCs          []string
	ArtistCredit   []artistCreditEntry
	Disambiguation string
}

// RecordingInfo is one recording with its releases, media, and track context.
type RecordingInfo struct {
	ID             string
	Title          string
	Length         int64
	ISRCs          []string
	ArtistCredit   []artistCreditEntry
	Disambiguation string
	Releases       []RecordingRelease
}

// RecordingRelease is a release linked to a recording. Media contains only the
// tracks for that recording, while TrackCount is the full medium size.
type RecordingRelease struct {
	ID           string
	Title        string
	Status       string
	Date         string
	ReleaseGroup *RecordingReleaseGroup
	Media        []RecordingMedium
}

// RecordingReleaseGroup is the release group linked to a recording release.
type RecordingReleaseGroup struct {
	ID          string
	Title       string
	PrimaryType string
}

// RecordingMedium is one disc/volume of a release.
type RecordingMedium struct {
	Position   int
	TrackCount int
	Tracks     []RecordingTrack
}

// RecordingTrack is the release track that carries the recording.
type RecordingTrack struct {
	ID       string
	Number   string
	Position int
	Length   int64
	Title    string
}

// ─── API client ────────────────────────────────────────────────────────

// APIClient provides access to the MusicBrainz public API.
type APIClient struct {
	cfg        MusicBrainzConfig
	httpClient *http.Client
	userAgent  string
	baseURL    string // configurable for testing
	log        *slog.Logger

	mu          sync.Mutex
	lastCall    time.Time
	minInterval time.Duration
}

// NewAPIClient creates a MusicBrainz API client.
func NewAPIClient(cfg MusicBrainzConfig, logger *slog.Logger) *APIClient {
	if logger == nil {
		logger = slog.Default()
	}
	ua := "Groovearr/0.1.0 ( github.com/ramonskie/groovearr )"
	if cfg.Email != "" {
		ua = "Groovearr/0.1.0 ( " + cfg.Email + " )"
	}
	return &APIClient{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		userAgent:   ua,
		baseURL:     defaultBaseURL,
		log:         logger,
		minInterval: defaultMinInterval, // MB limit is 1 req/sec per IP; see const for the margin
	}
}

// SetBaseURL overrides the API base URL. Intended for testing.
func (c *APIClient) SetBaseURL(u string) { c.mu.Lock(); c.baseURL = u; c.mu.Unlock() }

// SetHTTPClient overrides the HTTP client. Intended for testing.
func (c *APIClient) SetHTTPClient(client *http.Client) {
	c.mu.Lock()
	c.httpClient = client
	c.mu.Unlock()
}

// SetMinInterval overrides the rate-limit interval. Intended for testing.
func (c *APIClient) SetMinInterval(d time.Duration) { c.mu.Lock(); c.minInterval = d; c.mu.Unlock() }

// SearchReleaseGroups returns the ranked MusicBrainz release-group results for
// artist+album, best match first (MusicBrainz scores results by relevance,
// descending). A limit <= 0 defaults to defaultReleaseGroupLimit, and larger
// requests are clamped to maxReleaseGroupLimit. Returns nil, nil when there is
// no match.
func (c *APIClient) SearchReleaseGroups(ctx context.Context, artist, album string, limit int) ([]ReleaseGroupResult, error) {
	if artist == "" || album == "" {
		return nil, nil
	}
	query := fmt.Sprintf(`artist:"%s" AND releasegroup:"%s"`, escapeLucene(artist), escapeLucene(album))
	return c.searchReleaseGroupQuery(ctx, query, limit)
}

// SearchReleaseGroupsByTitle runs a ranked release-group search by title with an
// OPTIONAL artist constraint, for callers that hold a structured title but may
// not have an artist. When artist is empty the query is a bare
// releasegroup:"…" term; when it is non-empty an artist:"…" term is ANDed in.
// This is what lets a library album with no/unknown artist still search its
// title instead of issuing an empty query. A blank album returns nil, nil
// without a request; limit follows the same default/clamp rule as
// SearchReleaseGroups.
func (c *APIClient) SearchReleaseGroupsByTitle(ctx context.Context, artist, album string, limit int) ([]ReleaseGroupResult, error) {
	album = strings.TrimSpace(album)
	if album == "" {
		return nil, nil
	}
	query := fmt.Sprintf(`releasegroup:"%s"`, escapeLucene(album))
	if artist = strings.TrimSpace(artist); artist != "" {
		query = fmt.Sprintf(`artist:"%s" AND %s`, escapeLucene(artist), query)
	}
	return c.searchReleaseGroupQuery(ctx, query, limit)
}

// SearchReleaseGroupsQuery runs a single ranked release-group search for a
// caller-supplied free-text term. Unlike SearchReleaseGroups it applies no
// artist/title field structure: the text is trimmed, escaped, and used as a bare
// general query term, so a free-text caller (e.g. the Discover UI) costs exactly
// one request instead of guessing an artist/title split. A blank term returns
// nil, nil without a request. Returns nil, nil when there is no match.
func (c *APIClient) SearchReleaseGroupsQuery(ctx context.Context, rawQuery string, limit int) ([]ReleaseGroupResult, error) {
	rawQuery = strings.TrimSpace(rawQuery)
	if rawQuery == "" {
		return nil, nil
	}
	return c.searchReleaseGroupQuery(ctx, escapeLucene(rawQuery), limit)
}

// searchReleaseGroupQuery issues one /release-group/ search for an already-built,
// already-escaped Lucene query and parses the ranked result window. A limit <= 0
// defaults to defaultReleaseGroupLimit and larger requests are clamped to
// maxReleaseGroupLimit. Returns nil, nil when there is no match.
func (c *APIClient) searchReleaseGroupQuery(ctx context.Context, query string, limit int) ([]ReleaseGroupResult, error) {
	if limit <= 0 {
		limit = defaultReleaseGroupLimit
	}
	if limit > maxReleaseGroupLimit {
		limit = maxReleaseGroupLimit
	}
	data, err := c.apiGet(ctx, "/release-group/", map[string]string{
		"query": query,
		"limit": strconv.Itoa(limit),
		"fmt":   "json",
	})
	if err != nil {
		c.logOpError("musicbrainz search release group failed", "error", err, "query", query, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil // 404 or empty response
	}

	var resp struct {
		Count         int `json:"count"`
		ReleaseGroups []struct {
			ID               string              `json:"id"`
			Title            string              `json:"title"`
			FirstReleaseDate string              `json:"first-release-date"`
			PrimaryType      string              `json:"primary-type"`
			ArtistCredit     []artistCreditEntry `json:"artist-credit"`
		} `json:"release-groups"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal search release group failed", "error", err, "query", query, "component", "musicbrainz_api")
		return nil, err
	}
	if len(resp.ReleaseGroups) == 0 {
		return nil, nil
	}

	out := make([]ReleaseGroupResult, 0, len(resp.ReleaseGroups))
	for _, rg := range resp.ReleaseGroups {
		out = append(out, ReleaseGroupResult{
			MBID:             rg.ID,
			Title:            rg.Title,
			FirstReleaseDate: rg.FirstReleaseDate,
			PrimaryType:      rg.PrimaryType,
			ArtistName:       firstArtistCreditName(rg.ArtistCredit),
		})
	}
	return out, nil
}

// SearchReleaseGroup returns the first (best-ranked) MusicBrainz result for the
// given artist and album. MusicBrainz orders results by relevance score
// (descending), so the first result is typically the best match.
// Returns nil, nil if no match found.
//
// It is a thin wrapper over SearchReleaseGroups for callers that need only the
// top hit.
func (c *APIClient) SearchReleaseGroup(ctx context.Context, artist, album string) (*ReleaseGroupResult, error) {
	results, err := c.SearchReleaseGroups(ctx, artist, album, 0)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return &results[0], nil
}

// firstArtistCreditName returns the first artist-credit entry's name, or "" when
// the release group carries no credit. MusicBrainz lists the primary artist
// first, so credit[0] identifies the release group's real artist.
func firstArtistCreditName(credit []artistCreditEntry) string {
	if len(credit) == 0 {
		return ""
	}
	return credit[0].Name
}

// SearchRecording finds the album title for a track by searching MusicBrainz
// recordings via artist+title, then aggregating release-group titles across
// all matching recordings. The most frequent Album-type release group is
// returned — compilations appear once per recording, while the original album
// appears many times (different editions, formats, regions).
//
// Returns nil, nil if no match found.
func (c *APIClient) SearchRecording(ctx context.Context, artist, title string) (*ReleaseGroupResult, error) {
	if artist == "" || title == "" {
		return nil, nil
	}

	query := fmt.Sprintf(`artist:"%s" AND recording:"%s"`, escapeLucene(artist), escapeLucene(title))
	data, err := c.apiGet(ctx, "/recording/", map[string]string{
		"query": query,
		"limit": "100",
		"fmt":   "json",
	})
	if err != nil {
		c.logOpError("musicbrainz search recording failed", "error", err, "artist", artist, "title", title, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	var resp struct {
		Recordings []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Releases []struct {
				ID           string `json:"id"`
				ReleaseGroup *struct {
					ID          string `json:"id"`
					Title       string `json:"title"`
					PrimaryType string `json:"primary-type"`
				} `json:"release-group,omitempty"`
			} `json:"releases"`
		} `json:"recordings"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal search recording failed", "error", err, "component", "musicbrainz_api")
		return nil, err
	}

	// Aggregate release-group titles by frequency.
	// Prefer Album type; fall back to any type if no Albums found.
	counts := make(map[string]int)
	anyCounts := make(map[string]int)
	for _, rec := range resp.Recordings {
		seen := make(map[string]bool) // dedupe per recording
		for _, rel := range rec.Releases {
			if rel.ReleaseGroup == nil {
				continue
			}
			key := rel.ReleaseGroup.Title
			if rel.ReleaseGroup.PrimaryType == "Album" {
				if !seen[key] {
					counts[key]++
					seen[key] = true
				}
			} else if !seen[key] {
				anyCounts[key]++
				seen[key] = true
			}
		}
	}

	// Prefer most frequent Album; fall back to any type.
	pick := pickMostFrequent(counts)
	if pick == "" {
		pick = pickMostFrequent(anyCounts)
	}
	if pick == "" {
		return nil, nil
	}

	return &ReleaseGroupResult{
		Title: pick,
	}, nil
}

// pickMostFrequent returns the key with the highest count from a map.
// On ties, the alphabetically first key wins for deterministic results.
// Returns empty string if the map is empty.
func pickMostFrequent(counts map[string]int) string {
	var best string
	var bestCount int
	for k, v := range counts {
		if v > bestCount || (v == bestCount && (best == "" || k < best)) {
			best = k
			bestCount = v
		}
	}
	return best
}

// ArtistResult is a canonical artist found by MusicBrainz artist search.
type ArtistResult struct {
	Name string
}

// SearchArtist finds the canonical MusicBrainz spelling for an artist name.
// MusicBrainz orders results by relevance; this returns the first hit whose
// normalized name matches the query (so case-only typos like "Acda en De
// Munnik" resolve to the canonical "Acda en de Munnik"). Returns nil, nil
// when nothing in the top results matches.
func (c *APIClient) SearchArtist(ctx context.Context, name string) (*ArtistResult, error) {
	if name == "" {
		return nil, nil
	}
	query := fmt.Sprintf(`artist:"%s"`, escapeLucene(name))
	data, err := c.apiGet(ctx, "/artist/", map[string]string{
		"query": query,
		"limit": "5",
		"fmt":   "json",
	})
	if err != nil {
		c.logOpError("musicbrainz search artist failed", "error", err, "artist", name, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	var resp struct {
		Count   int `json:"count"`
		Artists []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Score int    `json:"score"`
		} `json:"artists"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal search artist failed", "error", err, "artist", name, "component", "musicbrainz_api")
		return nil, err
	}
	want := strutil.NormalizeName(name)
	for _, a := range resp.Artists {
		if strutil.NormalizeName(a.Name) != want {
			continue
		}
		return &ArtistResult{Name: a.Name}, nil
	}
	return nil, nil
}

// LookupRelease fetches full release info including ISRCs, genres, and labels.
func (c *APIClient) LookupRelease(ctx context.Context, mbid string) (*ReleaseInfo, error) {
	data, err := c.apiGet(ctx, "/release/"+url.PathEscape(mbid), map[string]string{
		"inc": "recordings+isrcs+labels+genres+artist-credits",
		"fmt": "json",
	})
	if err != nil {
		c.logOpError("musicbrainz lookup release failed", "error", err, "mbid", mbid, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil // 404 or empty response
	}

	var resp struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Date         string `json:"date"`
		Country      string `json:"country"`
		ReleaseGroup struct {
			ID string `json:"id"`
		} `json:"release-group"`
		LabelInfo []struct {
			CatalogNumber string `json:"catalog-number"`
			Label         struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"label"`
		} `json:"label-info"`
		Media []struct {
			Position int `json:"position"` // 1-based medium/disc index
			Tracks   []struct {
				Number    string `json:"number"`
				Title     string `json:"title"`
				Length    int64  `json:"length"` // milliseconds
				Recording struct {
					ID           string   `json:"id"`
					Title        string   `json:"title"`
					ISRCs        []string `json:"isrcs"`
					ArtistCredit []struct {
						Name       string `json:"name"`
						Joinphrase string `json:"joinphrase"`
					} `json:"artist-credit"`
				} `json:"recording"`
			} `json:"tracks"`
		} `json:"media"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal lookup release failed", "error", err, "mbid", mbid, "component", "musicbrainz_api")
		return nil, err
	}

	info := &ReleaseInfo{
		MBID:           resp.ID,
		Title:          resp.Title,
		Date:           resp.Date,
		Country:        resp.Country,
		ReleaseGroupID: resp.ReleaseGroup.ID,
	}

	// Extract label name from first label-info entry.
	if len(resp.LabelInfo) > 0 && resp.LabelInfo[0].Label.Name != "" {
		info.Label = resp.LabelInfo[0].Label.Name
	}

	// Extract genres.
	for _, g := range resp.Genres {
		if g.Name != "" {
			info.Genres = append(info.Genres, g.Name)
		}
	}

	// Extract tracks and ISRCs. MusicBrainz flattens tracks per medium, so the
	// medium index is threaded onto every track to preserve the disc number.
	for mediaIdx, medium := range resp.Media {
		disc := medium.Position
		if disc <= 0 {
			disc = mediaIdx + 1 // fall back to the 1-based medium order
		}
		for _, t := range medium.Tracks {
			pos := 0
			if n, _ := fmt.Sscanf(t.Number, "%d", &pos); n != 1 {
				// Non-numeric track number (e.g. "A1" for vinyl, "1-1" for multi-disc).
				// Extract any leading digit as best-effort position.
				for _, ch := range t.Number {
					if ch >= '0' && ch <= '9' {
						pos = pos*10 + int(ch-'0')
					} else if pos > 0 {
						break
					}
				}
			}
			artist := ""
			if len(t.Recording.ArtistCredit) > 0 {
				artist = t.Recording.ArtistCredit[0].Name
			}
			for i := 1; i < len(t.Recording.ArtistCredit); i++ {
				artist += t.Recording.ArtistCredit[i-1].Joinphrase
				artist += t.Recording.ArtistCredit[i].Name
			}
			track := ReleaseTrack{
				Position:    pos,
				Number:      t.Number,
				Title:       t.Title,
				Length:      t.Length,
				Artist:      artist,
				ISRCs:       t.Recording.ISRCs,
				DiscNumber:  disc,
				RecordingID: t.Recording.ID,
			}
			info.Tracks = append(info.Tracks, track)
			for _, isrc := range t.Recording.ISRCs {
				if isrc != "" {
					info.ISRCs = append(info.ISRCs, isrc)
				}
			}
		}
	}

	return info, nil
}

// LookupISRC resolves an ISRC to its recording(s). MusicBrainz's JSON ISRC
// response does not include releases, so callers that need release context must
// follow up with LookupRecording for the chosen recording MBID.
func (c *APIClient) LookupISRC(ctx context.Context, isrc string) ([]ISRCRecording, error) {
	isrc = matching.NormalizeISRC(isrc)
	if isrc == "" {
		return nil, nil
	}
	data, err := c.apiGet(ctx, "/isrc/"+url.PathEscape(isrc), map[string]string{
		"inc": "artist-credits+isrcs",
		"fmt": "json",
	})
	if err != nil {
		c.logOpError("musicbrainz lookup isrc failed", "error", err, "isrc", isrc, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil // 404 or empty response
	}

	var resp struct {
		ISRC       string `json:"isrc"`
		Recordings []struct {
			ID             string              `json:"id"`
			Title          string              `json:"title"`
			Length         int64               `json:"length"`
			ISRCs          []string            `json:"isrcs"`
			ArtistCredit   []artistCreditEntry `json:"artist-credit"`
			Disambiguation string              `json:"disambiguation"`
		} `json:"recordings"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal lookup isrc failed", "error", err, "isrc", isrc, "component", "musicbrainz_api")
		return nil, err
	}
	out := make([]ISRCRecording, 0, len(resp.Recordings))
	for _, rec := range resp.Recordings {
		out = append(out, ISRCRecording{
			ID:             rec.ID,
			Title:          rec.Title,
			Length:         rec.Length,
			ISRCs:          rec.ISRCs,
			ArtistCredit:   rec.ArtistCredit,
			Disambiguation: rec.Disambiguation,
		})
	}
	return out, nil
}

// LookupRecording fetches one recording with its releases, media, and track
// context. linked entities are capped by MusicBrainz (25 by default).
func (c *APIClient) LookupRecording(ctx context.Context, mbid string) (*RecordingInfo, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return nil, nil
	}
	data, err := c.apiGet(ctx, "/recording/"+url.PathEscape(mbid), map[string]string{
		"inc": "artist-credits+releases+release-groups+media+isrcs",
		"fmt": "json",
	})
	if err != nil {
		c.logOpError("musicbrainz lookup recording failed", "error", err, "mbid", mbid, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil // 404 or empty response
	}

	var resp struct {
		ID             string              `json:"id"`
		Title          string              `json:"title"`
		Length         int64               `json:"length"`
		ISRCs          []string            `json:"isrcs"`
		ArtistCredit   []artistCreditEntry `json:"artist-credit"`
		Disambiguation string              `json:"disambiguation"`
		Releases       []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			Status       string `json:"status"`
			Date         string `json:"date"`
			ReleaseGroup *struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				PrimaryType string `json:"primary-type"`
			} `json:"release-group"`
			Media []struct {
				Position   int `json:"position"`
				TrackCount int `json:"track-count"`
				Tracks     []struct {
					ID       string `json:"id"`
					Number   string `json:"number"`
					Position int    `json:"position"`
					Length   int64  `json:"length"`
					Title    string `json:"title"`
				} `json:"tracks"`
			} `json:"media"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal lookup recording failed", "error", err, "mbid", mbid, "component", "musicbrainz_api")
		return nil, err
	}

	info := &RecordingInfo{
		ID:             resp.ID,
		Title:          resp.Title,
		Length:         resp.Length,
		ISRCs:          resp.ISRCs,
		ArtistCredit:   resp.ArtistCredit,
		Disambiguation: resp.Disambiguation,
	}
	for _, rel := range resp.Releases {
		release := RecordingRelease{
			ID:     rel.ID,
			Title:  rel.Title,
			Status: rel.Status,
			Date:   rel.Date,
		}
		if rel.ReleaseGroup != nil {
			release.ReleaseGroup = &RecordingReleaseGroup{
				ID:          rel.ReleaseGroup.ID,
				Title:       rel.ReleaseGroup.Title,
				PrimaryType: rel.ReleaseGroup.PrimaryType,
			}
		}
		for _, medium := range rel.Media {
			m := RecordingMedium{
				Position:   medium.Position,
				TrackCount: medium.TrackCount,
			}
			for _, track := range medium.Tracks {
				m.Tracks = append(m.Tracks, RecordingTrack{
					ID:       track.ID,
					Number:   track.Number,
					Position: track.Position,
					Length:   track.Length,
					Title:    track.Title,
				})
			}
			release.Media = append(release.Media, m)
		}
		info.Releases = append(info.Releases, release)
	}
	return info, nil
}

// ReleaseGroupRelease is a release within a release group.
type ReleaseGroupRelease struct {
	ID             string
	Title          string
	Disambiguation string
	Status         string // release status, e.g. "Official", "Bootleg"
	Date           string // release date (YYYY, YYYY-MM, or YYYY-MM-DD)
	PrimaryType    string // the release group's primary type (e.g. "Album")
	TrackCount     int    // total tracks across all media
}

// LookupReleaseGroup retrieves all releases within a release group, including
// each release's total track count from its media. The media subquery is what
// lets identity resolution report a real release-group size instead of guessing
// from the one medium that carries a recording.
func (c *APIClient) LookupReleaseGroup(ctx context.Context, rgMBID string) ([]ReleaseGroupRelease, error) {
	data, err := c.apiGet(ctx, "/release-group/"+url.PathEscape(rgMBID), map[string]string{
		"inc": "releases+media",
		"fmt": "json",
	})
	if err != nil {
		c.logOpError("musicbrainz lookup release group failed", "error", err, "mbid", rgMBID, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	var resp struct {
		PrimaryType string `json:"primary-type"`
		Releases    []struct {
			ID             string `json:"id"`
			Title          string `json:"title"`
			Disambiguation string `json:"disambiguation"`
			Status         string `json:"status"`
			Date           string `json:"date"`
			Media          []struct {
				TrackCount int `json:"track-count"`
			} `json:"media"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal release group failed", "error", err, "mbid", rgMBID, "component", "musicbrainz_api")
		return nil, err
	}

	releases := make([]ReleaseGroupRelease, len(resp.Releases))
	for i, r := range resp.Releases {
		// Only trust the total when every medium reports a count. A missing
		// medium count would otherwise become a smaller, known-wrong total.
		trackCount := 0
		if len(r.Media) > 0 {
			for _, medium := range r.Media {
				if medium.TrackCount <= 0 {
					trackCount = 0
					break
				}
				trackCount += medium.TrackCount
			}
		}
		releases[i] = ReleaseGroupRelease{
			ID:             r.ID,
			Title:          r.Title,
			Disambiguation: r.Disambiguation,
			Status:         r.Status,
			Date:           r.Date,
			PrimaryType:    resp.PrimaryType,
			TrackCount:     trackCount,
		}
	}
	return releases, nil
}

// SearchReleasesByGroup returns all releases in a release group with their
// track counts. Uses the MusicBrainz search endpoint — a single API call.
// Limited to 100 results; groups larger than this are truncated.
func (c *APIClient) SearchReleasesByGroup(ctx context.Context, rgMBID string) ([]ReleaseGroupRelease, error) {
	data, err := c.apiGet(ctx, "/release/", map[string]string{
		"query": `rgid:` + escapeLucene(rgMBID),
		"limit": "100",
		"fmt":   "json",
	})
	if err != nil {
		c.logOpError("musicbrainz search releases by group failed", "error", err, "rgid", rgMBID, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	var resp struct {
		Releases []struct {
			ID             string `json:"id"`
			Title          string `json:"title"`
			Disambiguation string `json:"disambiguation"`
			TrackCount     int    `json:"track-count"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.logOpError("musicbrainz unmarshal search releases failed", "error", err, "rgid", rgMBID, "component", "musicbrainz_api")
		return nil, err
	}

	releases := make([]ReleaseGroupRelease, len(resp.Releases))
	for i, r := range resp.Releases {
		releases[i] = ReleaseGroupRelease{ID: r.ID, Title: r.Title, Disambiguation: r.Disambiguation, TrackCount: r.TrackCount}
	}
	return releases, nil
}

// ─── Internal HTTP ─────────────────────────────────────────────────────

func (c *APIClient) apiGet(ctx context.Context, path string, params map[string]string) (json.RawMessage, error) {
	// Rate limit with a proper token bucket: recompute the wait against the
	// latest lastCall under the mutex, so concurrent callers queue up and are
	// paced one minInterval apart instead of sleeping the same gap in parallel
	// and bursting through the server-side limit.
	c.mu.Lock()
	for {
		wait := c.minInterval - time.Since(c.lastCall)
		if wait <= 0 {
			break
		}
		c.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		c.mu.Lock()
	}
	c.lastCall = time.Now()
	// Snapshot the configurable fields under the same lock so a concurrent
	// SetBaseURL/SetHTTPClient (test-only setters) cannot race the request.
	baseURL := c.baseURL
	httpClient := c.httpClient
	userAgent := c.userAgent
	c.mu.Unlock()

	u, err := url.Parse(baseURL)
	if err != nil {
		c.logOpError("musicbrainz URL parse failed", "error", err, "path", path, "component", "musicbrainz_api")
		return nil, err
	}
	u = u.JoinPath(path)

	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		c.logOpError("musicbrainz create request failed", "error", err, "path", path, "component", "musicbrainz_api")
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		c.logOpError("musicbrainz HTTP request failed", "error", err, "path", path, "component", "musicbrainz_api")
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		c.logOpError("musicbrainz read response failed", "error", err, "path", path, "component", "musicbrainz_api")
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // not found is not an error
	}
	if resp.StatusCode == http.StatusBadRequest {
		// MusicBrainz answers 400 for a malformed MBID/ISRC. The provider-neutral
		// contract treats an unresolvable id as not found, so the admin API can
		// answer 404 instead of leaking a 500.
		return nil, fmt.Errorf("musicbrainz: invalid request: %w", discovery.ErrNotFound)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		ra := metadata.ParseRetryAfterHeader(resp.Header.Get("Retry-After"))
		c.log.Warn("musicbrainz rate limited", "path", path, "retry_after", ra.String(), "component", "musicbrainz_api")
		return nil, metadata.NewRateLimitError("musicbrainz", ra, "HTTP 429")
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		// MusicBrainz answers 503 for two distinct conditions:
		//   - a genuine throttle/ban, which carries a Retry-After header; and
		//   - a transient capacity blip ("The MusicBrainz web server is
		//     currently busy"), which does NOT and recurs intermittently even at
		//     multi-second spacing (observed live).
		// Only the former is a shared-bucket rate-limit (AGENTS §8). The latter
		// must not park the provider: treating every 503 as a rate-limit
		// escalated a 2m→4h app-wide cooldown and froze the whole refresh batch
		// on a momentary upstream blip.
		if ra := metadata.ParseRetryAfterHeader(resp.Header.Get("Retry-After")); ra > 0 {
			c.log.Warn("musicbrainz rate limited", "path", path, "retry_after", ra.String(), "component", "musicbrainz_api")
			return nil, metadata.NewRateLimitError("musicbrainz", ra, "HTTP 503")
		}
		c.log.Warn("musicbrainz busy (503, no Retry-After)", "path", path, "component", "musicbrainz_api")
		return nil, fmt.Errorf("musicbrainz: %w: HTTP 503 (server busy)", metadata.ErrProviderBusy)
	}
	if resp.StatusCode != http.StatusOK {
		c.logOpError("musicbrainz non-OK status", "status", resp.StatusCode, "body", string(body)[:min(len(string(body)), 200)], "component", "musicbrainz_api")
		return nil, fmt.Errorf("musicbrainz: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}

// logOpError logs an operation failure, choosing the level from the error. A
// rate-limit error (metadata.ErrRateLimited) is a routine, self-healing
// transient — the shared per-provider cooldown governs the retry — so it is
// logged at WARN to keep ERROR meaningful for real faults. Any other error (a
// transport, parse, or unexpected-status failure) stays at ERROR.
func (c *APIClient) logOpError(msg string, args ...any) {
	for _, a := range args {
		if err, ok := a.(error); ok && errors.Is(err, metadata.ErrRateLimited) {
			c.log.Warn(msg, args...)
			return
		}
	}
	c.log.Error(msg, args...)
}

// ─── Helpers ───────────────────────────────────────────────────────────

// escapeLucene escapes special characters in a Lucene query string.
// MusicBrainz uses Lucene syntax for search queries.
func escapeLucene(s string) string {
	// Characters that have special meaning in Lucene:
	// + - && || ! ( ) { } [ ] ^ " ~ * ? : \ /
	replacer := strings.NewReplacer(
		"+", `\+`,
		"-", `\-`,
		"&", `\&`,
		"|", `\|`,
		"!", `\!`,
		"(", `\(`,
		")", `\)`,
		"{", `\{`,
		"}", `\}`,
		"[", `\[`,
		"]", `\]`,
		"^", `\^`,
		`"`, `\"`,
		"~", `\~`,
		"*", `\*`,
		"?", `\?`,
		":", `\:`,
		"\\", `\\`,
		"/", `\/`,
	)
	return replacer.Replace(s)
}
