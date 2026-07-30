// Package prowlarr implements an AlbumProvider plugin that searches RuTracker
// via Prowlarr's Torznab API and resolves track listings via MusicBrainz.
package prowlarr

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ─── Torznab client ──────────────────────────────────────────────────

type torznabClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newTorznabClient(baseURL, apiKey string) *torznabClient {
	return &torznabClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// indexers returns all Prowlarr indexers.
func (c *torznabClient) indexers(ctx context.Context) ([]prowlarrIndexer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/indexer?apikey="+url.QueryEscape(c.apiKey), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: list indexers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("prowlarr: list indexers: %d: %s", resp.StatusCode, string(body))
	}

	var indexers []prowlarrIndexer
	if err := json.NewDecoder(resp.Body).Decode(&indexers); err != nil {
		return nil, fmt.Errorf("prowlarr: decode indexers: %w", err)
	}
	return indexers, nil
}

// tags returns all Prowlarr tags (id → label mapping for name-based filtering).
func (c *torznabClient) tags(ctx context.Context) ([]prowlarrTag, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/tag?apikey="+url.QueryEscape(c.apiKey), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: list tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("prowlarr: list tags: %d: %s", resp.StatusCode, string(body))
	}

	var tagList []prowlarrTag
	if err := json.NewDecoder(resp.Body).Decode(&tagList); err != nil {
		return nil, fmt.Errorf("prowlarr: decode tags: %w", err)
	}
	return tagList, nil
}

// searchMusic queries a Torznab indexer for music releases.
func (c *torznabClient) searchMusic(ctx context.Context, indexerID int, query string, categories []int) ([]torznabResult, error) {
	u := fmt.Sprintf("%s/%d/api?apikey=%s&t=music&q=%s",
		c.baseURL, indexerID, url.QueryEscape(c.apiKey),
		url.QueryEscape(query))
	if len(categories) > 0 {
		cats := make([]string, len(categories))
		for i, cat := range categories {
			cats[i] = fmt.Sprintf("%d", cat)
		}
		u += "&cat=" + url.QueryEscape(strings.Join(cats, ","))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/xml")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("prowlarr: search: %d: %s", resp.StatusCode, string(body))
	}

	var rss torznabRSS
	if err := xml.NewDecoder(resp.Body).Decode(&rss); err != nil {
		return nil, fmt.Errorf("prowlarr: parse torznab XML: %w", err)
	}

	var results []torznabResult
	for _, item := range rss.Channel.Items {
		if isImageRelease(item.Title, item.Description) {
			continue
		}
		result := torznabResult{
			Title:       item.Title,
			GUID:        item.GUID,
			Size:        item.Size,
			Link:        item.Enclosure.URL,
			Description: item.Description,
			Seeders:     item.Seeders,
			Peers:       item.Peers,
		}
		if result.Link == "" {
			result.Link = item.Link
		}
		// Parse attr elements for metadata and fallback fields.
		// Prowlarr emits seeders/peers/infohash as attrs, not direct children.
		for _, attr := range item.Attrs {
			switch strings.ToLower(attr.Name) {
			case "artist":
				result.Artist = attr.Value
			case "album":
				result.Album = attr.Value
			case "year":
				fmt.Sscanf(attr.Value, "%d", &result.Year)
			case "seeders":
				fmt.Sscanf(attr.Value, "%d", &result.Seeders)
			case "peers":
				fmt.Sscanf(attr.Value, "%d", &result.Peers)
			case "infohash":
				result.Infohash = attr.Value
			}
		}

		results = append(results, result)
	}
	return results, nil
}

// ─── XML types ───────────────────────────────────────────────────────

type torznabRSS struct {
	XMLName xml.Name      `xml:"rss"`
	Channel torznabChannel `xml:"channel"`
}

type torznabChannel struct {
	Title string        `xml:"title"`
	Items []torznabItem `xml:"item"`
}

type torznabItem struct {
	Title       string           `xml:"title"`
	GUID        string           `xml:"guid"`
	Size        int64            `xml:"size"`
	Link        string           `xml:"link"`
	Description string           `xml:"description"`
	Seeders     int              `xml:"seeders"`
	Peers       int              `xml:"peers"`
	Infohash    string           `xml:"infohash"`
	Enclosure   torznabEnclosure `xml:"enclosure"`
	Attrs       []torznabAttr    `xml:"attr"`
}

type torznabEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// ─── API types ───────────────────────────────────────────────────────

type torznabResult struct {
	Title       string
	GUID        string
	Size        int64
	Link        string
	Description string
	Seeders     int
	Peers       int
	Infohash    string
	Artist      string
	Album       string
	Year        int
}

type prowlarrIndexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Tags     []int  `json:"tags"`
}

type prowlarrTag struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// ─── Title helpers ───────────────────────────────────────────────────

// parseTitle extracts artist and album from a RuTracker torrent title.
// Used for display metadata and MusicBrainz resolution.
// The matching engine handles confidence scoring separately.
func parseTitle(title string) (artist, album string) {
	// Strip leading (Genre...) [Format...] prefixes.
	cleaned := title
	for {
		cleaned = strings.TrimSpace(cleaned)
		if len(cleaned) == 0 {
			break
		}
		start := cleaned[0]
		var end byte
		if start == '(' {
			end = ')'
		} else if start == '[' {
			end = ']'
		} else {
			break
		}
		// Find the closing bracket.
		closeIdx := strings.IndexByte(cleaned[1:], end)
		if closeIdx < 0 {
			break
		}
		closeIdx++ // relative to cleaned

		// Check what follows: space → strip prefix, another bracket → strip and continue.
		after := cleaned[closeIdx+1:]
		if strings.HasPrefix(after, " ") {
			cleaned = strings.TrimSpace(after)
		} else if len(after) > 0 && (after[0] == '[' || after[0] == '(') {
			cleaned = strings.TrimSpace(after)
		} else {
			break
		}
	}

	sep := " - "
	idx := strings.Index(cleaned, sep)
	if idx < 0 {
		return "", cleaned
	}
	artist = strings.TrimSpace(cleaned[:idx])
	album = strings.TrimSpace(cleaned[idx+len(sep):])

	// Strip trailing year, format, catalog info (aggressive — RuTracker titles
	// are verbose). Loop to handle multiple bracket/paren blocks.
	album = stripTrailingMetadata(album)

	return artist, album
}

// stripTrailingMetadata removes common RuTracker suffix patterns:
// catalog numbers, format tags, years, etc. from the right side of an album title.
func stripTrailingMetadata(album string) string {
	for {
		before := album

		// Strip trailing " - " (orphaned separator after bracket stripping).
		album = strings.TrimSuffix(album, " - ")
		album = strings.TrimRight(album, " -")

		// Strip " - YYYY" or " - YYYY/YYYY" or " - YYYY, FLAC"
		if idx := strings.LastIndex(album, " - "); idx > 0 {
			after := album[idx+3:]
			if len(after) >= 4 && after[0] >= '0' && after[0] <= '9' {
				album = strings.TrimSpace(album[:idx])
			}
		}

		// Strip trailing [...]
		if idx := strings.LastIndex(album, " ["); idx > 0 {
			rest := album[idx+1:]
			if closeIdx := strings.IndexRune(rest, ']'); closeIdx >= 0 {
				// Strip "[...]". Any content after the closing bracket
				// (e.g. "- year, format") is handled by subsequent iterations.
				after := strings.TrimSpace(rest[closeIdx+1:])
				album = strings.TrimSpace(album[:idx] + " " + after)
			}
		}

		// Strip trailing (...)
		if idx := strings.LastIndex(album, " ("); idx > 0 {
			rest := album[idx+1:]
			if closeIdx := strings.IndexRune(rest, ')'); closeIdx >= 0 {
				after := strings.TrimSpace(rest[closeIdx+1:])
				album = strings.TrimSpace(album[:idx] + " " + after)
			}
		}

		if album == before {
			break
		}
	}
	return album
}

// imageTitleTerms are lower-case substrings that indicate an ISO or disc-image
// release rather than extractable audio files.
var imageTitleTerms = []string{
	".iso", "(iso)", "[iso]", " iso ","iso image", "образ диска",
	"sacd-r", "sacd iso",
	"dvd-a", "dvd-audio",
	"dts-cd", "dts cd ",
	"bd-a",
	"blu-ray audio", "bluray audio",
	"image+.cue", "image + .cue",
	"dsd 128", "dsd 256", "dsd 64", "dsd128", "dsd256", "dsd64",
}

// isImageRelease returns true if the title or description indicates a disc
// image release unlikely to contain extractable audio files.
func isImageRelease(title, description string) bool {
	t := strings.ToLower(title + " " + description)
	for _, term := range imageTitleTerms {
		if strings.Contains(t, term) {
			return true
		}
	}
	return false
}
func extractYear(title string) int {
	for i := 0; i < len(title)-4; i++ {
		open := title[i]
		if open != '(' && open != '[' {
			continue
		}
		close := byte(')')
		if open == '[' {
			close = ']'
		}
		var y int
		n, _ := fmt.Sscanf(title[i+1:], "%d", &y)
		if n == 1 && y >= 1900 && y <= 2100 {
			// Verify the number is followed by the matching closing bracket.
			rest := title[i+1:]
			digitsLen := 0
			for d := y; d > 0; d /= 10 {
				digitsLen++
			}
			if digitsLen > 0 && len(rest) > digitsLen && rest[digitsLen] == close {
				return y
			}
		}
	}
	return 0
}
