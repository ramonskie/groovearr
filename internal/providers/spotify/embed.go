package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// SpotifyURL holds the parsed components of a Spotify URL.
type SpotifyURL struct {
	Type string // "track", "album", or "playlist"
	ID   string // Spotify resource identifier
}

// OEmbedResponse holds the JSON response from open.spotify.com/oembed.
type OEmbedResponse struct {
	HTML            string  `json:"html"`
	Width           *int    `json:"width"`
	Height          *int    `json:"height"`
	Version         string  `json:"version"`
	ProviderName    string  `json:"provider_name"`
	ProviderURL     string  `json:"provider_url"`
	Type            string  `json:"type"`
	Title           string  `json:"title"`
	ThumbnailURL    *string `json:"thumbnail_url"`
	ThumbnailWidth  *int    `json:"thumbnail_width"`
	ThumbnailHeight *int    `json:"thumbnail_height"`
}

// EmbedParseResult holds data extracted from a Spotify embed iframe HTML snippet.
type EmbedParseResult struct {
	Type   string // track, album, or playlist
	ID     string // Spotify resource identifier
	Title  string // title from iframe title attribute, if present
	Artist string // not available from embed HTML in free mode
	Album  string // not available from embed HTML in free mode
}

// TrackInfo holds basic track metadata extracted from oEmbed and/or embed HTML.
type TrackInfo struct {
	Title    string
	Artist   string
	CoverURL string
}

// PlaylistInfo holds basic playlist metadata extracted from oEmbed and/or embed HTML.
type PlaylistInfo struct {
	Title      string
	Owner      string
	CoverURL   string
	TrackCount int
}

// ---------------------------------------------------------------------------
// ParseSpotifyURL
// ---------------------------------------------------------------------------

// spotifyURLPattern matches open.spotify.com URLs with optional /intl-{locale} prefix.
var spotifyURLPattern = regexp.MustCompile(
	`^https?://open\.spotify\.com(?:/intl-[a-z]{2}(?:-[a-zA-Z]{2})?)?/(track|album|playlist)/([a-zA-Z0-9]+)(?:[?#].*)?$`,
)

// ParseSpotifyURL parses a Spotify URL into its resource type and ID.
// Handles query parameters (?si=..., ?go=1&sp_cid=...), localized prefixes
// (/intl-de, /intl-pt-BR), and trailing slashes.
func ParseSpotifyURL(rawURL string) (*SpotifyURL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("spotify: empty URL")
	}

	// Normalize: strip trailing slash before query/fragment for pattern matching.
	// We pre-process so the regex doesn't have to deal with trailing slashes.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("spotify: invalid URL: %w", err)
	}

	if parsed.Host != "open.spotify.com" {
		return nil, fmt.Errorf("spotify: unsupported host %q — only open.spotify.com is supported", parsed.Host)
	}

	// Rebuild the URL without query/fragment so the regex matches cleanly.
	cleanPath := strings.TrimRight(parsed.Path, "/")
	cleanURL := fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, cleanPath)

	matches := spotifyURLPattern.FindStringSubmatch(cleanURL)
	if matches == nil {
		return nil, fmt.Errorf("spotify: could not parse Spotify URL: %s", rawURL)
	}

	return &SpotifyURL{
		Type: matches[1],
		ID:   matches[2],
	}, nil
}

// ---------------------------------------------------------------------------
// FetchOEmbed
// ---------------------------------------------------------------------------

const oembedBaseURL = "https://open.spotify.com/oembed"

var spotifyIDRE = regexp.MustCompile(`^[a-zA-Z0-9]{22}$`)

// looksLikeID reports whether s looks like a Spotify resource ID (base62 alphanumeric, 22 chars).
func looksLikeID(s string) bool {
	return spotifyIDRE.MatchString(s)
}

// FetchOEmbed fetches oEmbed metadata for a Spotify URL.
// No authentication required — this is the open oEmbed endpoint.
func FetchOEmbed(ctx context.Context, spotifyURL string) (*OEmbedResponse, error) {
	return fetchOEmbed(ctx, http.DefaultClient, spotifyURL)
}

// FetchOEmbedWithClient fetches oEmbed metadata for a Spotify URL using a
// specific HTTP client. Useful for testing (httptest server).
func FetchOEmbedWithClient(ctx context.Context, client *http.Client, spotifyURL string) (*OEmbedResponse, error) {
	return fetchOEmbed(ctx, client, spotifyURL)
}

// fetchOEmbed performs the actual oEmbed request with the given HTTP client.
func fetchOEmbed(ctx context.Context, client *http.Client, spotifyURL string) (*OEmbedResponse, error) {
	reqURL := fmt.Sprintf("%s?url=%s", oembedBaseURL, url.QueryEscape(spotifyURL))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("spotify: oembed request failed: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify: oembed fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("spotify: oembed read failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify: oembed returned %d: %s", resp.StatusCode, string(body))
	}

	var o OEmbedResponse
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, fmt.Errorf("spotify: oembed parse failed: %w", err)
	}

	return &o, nil
}

// ---------------------------------------------------------------------------
// ParseEmbedHTML
// ---------------------------------------------------------------------------

// extract embed URL patterns, e.g. src="https://open.spotify.com/embed/track/{id}"
var embedSrcRE = regexp.MustCompile(`src\s*=\s*"(https://open\.spotify\.com/embed/(track|album|playlist)/([a-zA-Z0-9]+))`)

// iframeTitleRE extracts the title attribute from an iframe tag.
var iframeTitleRE = regexp.MustCompile(`title\s*=\s*"([^"]*)"`)

// ParseEmbedHTML extracts Spotify resource metadata from an oEmbed HTML iframe snippet.
// The HTML field from oEmbed typically contains a single <iframe> tag pointing to
// open.spotify.com/embed/{type}/{id}.
func ParseEmbedHTML(html string) *EmbedParseResult {
	result := &EmbedParseResult{}

	// Extract type and ID from the iframe src URL.
	if matches := embedSrcRE.FindStringSubmatch(html); len(matches) == 4 {
		result.Type = matches[2]
		result.ID = matches[3]
	}

	// Extract the iframe title attribute, which sometimes carries the track/album name.
	if matches := iframeTitleRE.FindStringSubmatch(html); len(matches) == 2 {
		result.Title = matches[1]
	}

	return result
}

// ---------------------------------------------------------------------------
// ExtractTrackInfo / ExtractPlaylistInfo
// ---------------------------------------------------------------------------

// ExtractTrackInfo builds a TrackInfo from oEmbed and embed-parsed data.
// When both sources are available the function merges them; oEmbed.Title
// takes priority over the embed-parsed title.
func ExtractTrackInfo(oembed *OEmbedResponse, embed *EmbedParseResult) *TrackInfo {
	info := &TrackInfo{}

	if oembed != nil {
		info.Title = oembed.Title
		if oembed.ThumbnailURL != nil {
			info.CoverURL = *oembed.ThumbnailURL
		}
	}

	// Fall back to embed-parsed title if oembed is not available.
	if info.Title == "" && embed != nil {
		info.Title = embed.Title
	}

	// Artist is not available from oEmbed for tracks in free mode;
	// the embed HTML iframe also doesn't carry artist metadata.
	if embed != nil {
		info.Artist = embed.Artist
	}

	return info
}

// ExtractPlaylistInfo builds a PlaylistInfo from oEmbed and embed-parsed data.
func ExtractPlaylistInfo(oembed *OEmbedResponse, embed *EmbedParseResult) *PlaylistInfo {
	info := &PlaylistInfo{}

	if oembed != nil {
		info.Title = oembed.Title
		if oembed.ThumbnailURL != nil {
			info.CoverURL = *oembed.ThumbnailURL
		}
	}

	if info.Title == "" && embed != nil {
		info.Title = embed.Title
	}

	return info
}

// ─── Embed Playlist Parser ───────────────────────────────────────────

// nextDataRE extracts the __NEXT_DATA__ JSON blob from Spotify embed pages.
var nextDataRE = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)

// nextDataEntity holds entity fields from __NEXT_DATA__. Only fields we need.
type nextDataEntity struct {
	Name      string          `json:"name"`
	TrackList []nextDataTrack `json:"trackList"`
}

type nextDataTrack struct {
	URI      string `json:"uri"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Duration int    `json:"duration"`
}

// EmbedPlaylist holds the parsed result from a Spotify embed playlist page.
type EmbedPlaylist struct {
	Name   string
	Tracks []EmbedTrack
}

// EmbedTrack holds an individual track parsed from a Spotify embed page.
type EmbedTrack struct {
	ID       string
	Title    string
	Artist   string
	Duration int // milliseconds
}

// FetchEmbedPlaylist fetches and parses a Spotify embed playlist page.
// Extracts structured data from the __NEXT_DATA__ JSON blob — no auth required.
func FetchEmbedPlaylist(ctx context.Context, client *http.Client, playlistID string) (*EmbedPlaylist, error) {
	u := "https://open.spotify.com/embed/playlist/" + playlistID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("spotify: embed request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify: embed fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify: embed returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("spotify: embed read: %w", err)
	}

	m := nextDataRE.FindSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("spotify: could not find __NEXT_DATA__ in embed page")
	}

	var wrapper struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity nextDataEntity `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(m[1], &wrapper); err != nil {
		return nil, fmt.Errorf("spotify: parse __NEXT_DATA__: %w", err)
	}

	entity := wrapper.Props.PageProps.State.Data.Entity
	if entity.Name == "" {
		return nil, fmt.Errorf("spotify: embed page missing playlist name")
	}

	ep := &EmbedPlaylist{Name: entity.Name}
	for _, t := range entity.TrackList {
		id := trackIDFromURI(t.URI)
		if id == "" || t.Title == "" {
			continue
		}
		ep.Tracks = append(ep.Tracks, EmbedTrack{
			ID:       id,
			Title:    t.Title,
			Artist:   t.Subtitle,
			Duration: t.Duration,
		})
	}

	return ep, nil
}

// trackIDFromURI extracts the track ID from a Spotify URI like "spotify:track:XXXX".
func trackIDFromURI(uri string) string {
	if strings.HasPrefix(uri, "spotify:track:") {
		return strings.TrimPrefix(uri, "spotify:track:")
	}
	return ""
}
