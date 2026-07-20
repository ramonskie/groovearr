package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const baseURL = "https://musicbrainz.org/ws/2"

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
	Number  string
	Title   string
	Length  int64  // milliseconds
	ISRCs   []string
}

// ─── API client ────────────────────────────────────────────────────────

// apiClient provides access to the MusicBrainz public API.
type apiClient struct {
	cfg        MusicBrainzConfig
	httpClient *http.Client
	userAgent  string

	mu         sync.Mutex
	lastCall   time.Time
	minInterval time.Duration
}

// newAPIClient creates a MusicBrainz API client.
func newAPIClient(cfg MusicBrainzConfig) *apiClient {
	ua := "Groovearr/0.1.0 ( github.com/ramonskie/groovearr )"
	if cfg.Email != "" {
		ua = "Groovearr/0.1.0 ( " + cfg.Email + " )"
	}
	return &apiClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		userAgent:  ua,
		minInterval: time.Second, // 1 req/sec
	}
}

// SearchReleaseGroup returns the first MusicBrainz result for the given artist and album.
// MusicBrainz orders results by relevance score (descending), so the first result is
// typically the best match.
// Returns nil, nil if no match found.
func (c *apiClient) SearchReleaseGroup(ctx context.Context, artist, album string) (*ReleaseGroupResult, error) {
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

// LookupRelease fetches full release info including ISRCs, genres, and labels.
func (c *apiClient) LookupRelease(ctx context.Context, mbid string) (*ReleaseInfo, error) {
	data, err := c.apiGet(ctx, "/release/"+mbid, map[string]string{
		"inc":  "recordings+isrcs+labels+genres+artist-credits",
		"fmt":  "json",
	})
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil // 404 or empty response
	}

	var resp struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Date    string `json:"date"`
		Country string `json:"country"`
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
					ID    string   `json:"id"`
					Title string   `json:"title"`
					ISRCs []string `json:"isrcs"`
				} `json:"recording"`
			} `json:"tracks"`
		} `json:"media"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
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
			track := ReleaseTrack{
				Number: t.Number,
				Title:  t.Title,
				Length: t.Length,
				ISRCs:  t.Recording.ISRCs,
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

// ─── Internal HTTP ─────────────────────────────────────────────────────

func (c *apiClient) apiGet(ctx context.Context, path string, params map[string]string) (json.RawMessage, error) {
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

	u, err := url.Parse(baseURL)
	if err != nil {
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
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // not found is not an error
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
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
