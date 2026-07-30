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
	"strings"
	"sync"
	"time"
)

const defaultBaseURL = "https://musicbrainz.org/ws/2"

// ErrRateLimited is returned when MusicBrainz responds with HTTP 503.
var ErrRateLimited = errors.New("musicbrainz: rate limited")

// ─── Public types ──────────────────────────────────────────────────────

// ReleaseGroupResult holds the key fields from a release group search result.
type ReleaseGroupResult struct {
	MBID             string
	Title            string
	FirstReleaseDate string
	PrimaryType      string
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
	Position int    // 1-based track position (parsed from Number)
	Number   string // display number from MusicBrainz (e.g. "1", "A1")
	Title    string
	Length   int64  // milliseconds
	Artist   string // per-track artist credit (for compilations)
	ISRCs    []string
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
		minInterval: time.Second, // 1 req/sec
	}
}

// SetBaseURL overrides the API base URL. Intended for testing.
func (c *APIClient) SetBaseURL(u string) { c.baseURL = u }

// SetHTTPClient overrides the HTTP client. Intended for testing.
func (c *APIClient) SetHTTPClient(client *http.Client) { c.httpClient = client }

// SetMinInterval overrides the rate-limit interval. Intended for testing.
func (c *APIClient) SetMinInterval(d time.Duration) { c.mu.Lock(); c.minInterval = d; c.mu.Unlock() }

// SearchReleaseGroup returns the first MusicBrainz result for the given artist and album.
// MusicBrainz orders results by relevance score (descending), so the first result is
// typically the best match.
// Returns nil, nil if no match found.
func (c *APIClient) SearchReleaseGroup(ctx context.Context, artist, album string) (*ReleaseGroupResult, error) {
	if artist == "" || album == "" {
		return nil, nil
	}
	query := fmt.Sprintf(`artist:"%s" AND releasegroup:"%s"`, escapeLucene(artist), escapeLucene(album))
	data, err := c.apiGet(ctx, "/release-group/", map[string]string{
		"query": query,
		"limit": "5",
		"fmt":   "json",
	})
	if err != nil {
		c.log.Error("musicbrainz search release group failed", "error", err, "artist", artist, "album", album, "component", "musicbrainz_api")
		return nil, err
	}
	if data == nil {
		return nil, nil // 404 or empty response
	}

	var resp struct {
		Count         int `json:"count"`
		ReleaseGroups []struct {
			ID               string `json:"id"`
			Title            string `json:"title"`
			FirstReleaseDate string `json:"first-release-date"`
			PrimaryType      string `json:"primary-type"`
		} `json:"release-groups"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.log.Error("musicbrainz unmarshal search release group failed", "error", err, "artist", artist, "album", album, "component", "musicbrainz_api")
		return nil, err
	}
	if len(resp.ReleaseGroups) == 0 {
		return nil, nil
	}

	rg := resp.ReleaseGroups[0]
	return &ReleaseGroupResult{
		MBID:             rg.ID,
		Title:            rg.Title,
		FirstReleaseDate: rg.FirstReleaseDate,
		PrimaryType:      rg.PrimaryType,
	}, nil
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
		c.log.Error("musicbrainz search recording failed", "error", err, "artist", artist, "title", title, "component", "musicbrainz_api")
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
		c.log.Error("musicbrainz unmarshal search recording failed", "error", err, "component", "musicbrainz_api")
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

// LookupRelease fetches full release info including ISRCs, genres, and labels.
func (c *APIClient) LookupRelease(ctx context.Context, mbid string) (*ReleaseInfo, error) {
	data, err := c.apiGet(ctx, "/release/"+mbid, map[string]string{
		"inc": "recordings+isrcs+labels+genres+artist-credits",
		"fmt": "json",
	})
	if err != nil {
		c.log.Error("musicbrainz lookup release failed", "error", err, "mbid", mbid, "component", "musicbrainz_api")
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
			Tracks []struct {
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
		c.log.Error("musicbrainz unmarshal lookup release failed", "error", err, "mbid", mbid, "component", "musicbrainz_api")
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

	// Extract tracks and ISRCs.
	for _, medium := range resp.Media {
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
				Position: pos,
				Number:   t.Number,
				Title:    t.Title,
				Length:   t.Length,
				Artist:   artist,
				ISRCs:    t.Recording.ISRCs,
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

// ReleaseGroupRelease is a release within a release group.
type ReleaseGroupRelease struct {
	ID             string
	Title          string
	Disambiguation string
	TrackCount     int // total tracks across all media
}

// LookupReleaseGroup retrieves all releases within a release group.
func (c *APIClient) LookupReleaseGroup(ctx context.Context, rgMBID string) ([]ReleaseGroupRelease, error) {
	data, err := c.apiGet(ctx, "/release-group/"+rgMBID, map[string]string{
		"inc": "releases",
		"fmt": "json",
	})
	if err != nil {
		c.log.Error("musicbrainz lookup release group failed", "error", err, "mbid", rgMBID, "component", "musicbrainz_api")
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
		} `json:"releases"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.log.Error("musicbrainz unmarshal release group failed", "error", err, "mbid", rgMBID, "component", "musicbrainz_api")
		return nil, err
	}

	releases := make([]ReleaseGroupRelease, len(resp.Releases))
	for i, r := range resp.Releases {
		releases[i] = ReleaseGroupRelease{ID: r.ID, Title: r.Title, Disambiguation: r.Disambiguation}
	}
	return releases, nil
}

// SearchReleasesByGroup returns all releases in a release group with their
// track counts. Uses the MusicBrainz search endpoint — a single API call.
// Limited to 100 results; groups larger than this are truncated.
func (c *APIClient) SearchReleasesByGroup(ctx context.Context, rgMBID string) ([]ReleaseGroupRelease, error) {
	data, err := c.apiGet(ctx, "/release/", map[string]string{
		"query": `rgid:` + rgMBID,
		"limit": "100",
		"fmt":   "json",
	})
	if err != nil {
		c.log.Error("musicbrainz search releases by group failed", "error", err, "rgid", rgMBID, "component", "musicbrainz_api")
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
		c.log.Error("musicbrainz unmarshal search releases failed", "error", err, "rgid", rgMBID, "component", "musicbrainz_api")
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
	// Rate limit with mutex for concurrent-safety.
	c.mu.Lock()
	elapsed := time.Since(c.lastCall)
	if elapsed < c.minInterval {
		c.mu.Unlock()
		select {
		case <-time.After(c.minInterval - elapsed):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		c.mu.Lock()
	}
	c.lastCall = time.Now()
	c.mu.Unlock()

	u, err := url.Parse(c.baseURL)
	if err != nil {
		c.log.Error("musicbrainz URL parse failed", "error", err, "path", path, "component", "musicbrainz_api")
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
		c.log.Error("musicbrainz create request failed", "error", err, "path", path, "component", "musicbrainz_api")
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Error("musicbrainz HTTP request failed", "error", err, "path", path, "component", "musicbrainz_api")
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.log.Error("musicbrainz read response failed", "error", err, "path", path, "component", "musicbrainz_api")
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // not found is not an error
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		c.log.Warn("musicbrainz rate limited", "path", path, "component", "musicbrainz_api")
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		c.log.Error("musicbrainz non-OK status", "status", resp.StatusCode, "body", string(body)[:min(len(string(body)), 200)], "component", "musicbrainz_api")
		return nil, fmt.Errorf("musicbrainz: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
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
