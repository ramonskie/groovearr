// Package prowlarr implements an AlbumProvider plugin that searches RuTracker
// via Prowlarr's Torznab API and resolves track listings via MusicBrainz.
package prowlarr

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/providers/musicbrainz"
)

const (
	pluginName        = "prowlarr"
	displayName       = "Prowlarr"
	coverArtBase      = "https://coverartarchive.org"
	defaultIndexerTag = "groovearr"
)

// Config holds the Prowlarr connection settings.
type Config struct {
	URL        string `json:"url"`         // e.g. http://localhost:9696
	APIKey     string `json:"api_key"`     // Prowlarr API key
	IndexerTag string `json:"indexer_tag"` // tag to filter indexers (default: "groovearr")
	Categories []int  `json:"categories"`  // Torznab categories (default: [3040])
}

// Plugin implements download.AlbumProvider for Prowlarr/RuTracker.
// Track metadata resolution delegates to the MusicBrainz API via the
// shared musicbrainz.APIClient for rate-limited, single-instance access.
type Plugin struct {
	cfg      Config
	torznab  *torznabClient
	mb       *musicbrainz.APIClient
	log      *slog.Logger
	mu        sync.Mutex
	connected bool
}

var _ download.AlbumProvider = (*Plugin)(nil)

func newPlugin(cfg Config, mbClient *musicbrainz.APIClient, logger *slog.Logger) (*Plugin, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.IndexerTag == "" {
		cfg.IndexerTag = defaultIndexerTag
	}

	p := &Plugin{
		cfg:     cfg,
		torznab: newTorznabClient(cfg.URL, cfg.APIKey),
		mb:      mbClient,
		log:     logger,
	}
	return p, nil
}

// ─── plugin.BasePlugin ───────────────────────────────────────────────

func (p *Plugin) Name() string                     { return pluginName }
func (p *Plugin) DisplayName() string               { return displayName }
func (p *Plugin) IsConfigured() bool                { return p.cfg.URL != "" && p.cfg.APIKey != "" }
func (p *Plugin) Connected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.connected
}
func (p *Plugin) CapabilityStatus() map[string]string { return map[string]string{"album_search": p.capStatus()} }

func (p *Plugin) capStatus() string {
	if !p.IsConfigured() { return "not_configured" }
	if p.Connected() { return "connected" }
	return "configured"
}

func (p *Plugin) CheckConnection(ctx context.Context) error {
	if !p.IsConfigured() {
		return fmt.Errorf("prowlarr: not configured")
	}
	_, err := p.torznab.indexers(ctx)
	p.mu.Lock()
	if err != nil {
		p.connected = false
		p.mu.Unlock()
		return fmt.Errorf("prowlarr: connection check failed: %w", err)
	}
	p.connected = true
	p.mu.Unlock()
	return nil
}

// ─── download.AlbumProvider ──────────────────────────────────────────

func (p *Plugin) SearchAlbum(ctx context.Context, query string) ([]domain.AlbumRelease, error) {
	indexers, err := p.findRuTrackerIndexers(ctx)
	if err != nil {
		return nil, err
	}

	var releases []domain.AlbumRelease
	for _, idx := range indexers {
		results, err := p.torznab.searchMusic(ctx, idx.ID, query, p.cfg.Categories)
		if err != nil {
			p.log.Warn("prowlarr: search failed on indexer",
				"indexer", idx.Name, "id", idx.ID, "error", err, "component", "prowlarr")
			continue
		}
		for _, r := range results {
			if isImageRelease(r.Title, r.Description) {
				continue
			}
			format := "flac"
			title := strings.ToLower(r.Title)
			if strings.Contains(title, "mp3") || strings.Contains(title, "320") {
				format = "mp3"
			}
			artist, album := r.Artist, r.Album
			// Prowlarr's own parsing sometimes misattributes format metadata to
			// the album field (e.g. "2025, MP3, 320 kbps"). When the parsed
			// album looks like format metadata or the artist starts with a path
			// separator, fall back to our local parseTitle.
			if artist == "" || album == "" || looksLikeFormatMetadata(album) || strings.HasPrefix(strings.TrimSpace(artist), "/") {
				artist, album = parseTitleFromResult(r)
			}
			year := r.Year
			if year == 0 {
				year = extractYearFromResult(r)
			}
			releases = append(releases, domain.AlbumRelease{
				SourceName: pluginName,
				Artist:     artist,
				Album:      album,
				Year:       year,
				Format:     format,
				Size:       r.Size,
				Seeders:    r.Seeders,
				MagnetURI:  r.Link,
			})
		}
	}
	if len(releases) == 0 {
		return nil, nil
	}
	return releases, nil
}

// ResolveTracks resolves the expected track listing for an album by searching
// MusicBrainz. Uses the first release in the release group.
func (p *Plugin) ResolveTracks(ctx context.Context, release domain.AlbumRelease) ([]domain.ExpectedTrack, error) {
	tracks, _, err := p.resolveTracks(ctx, release, 0, "")
	return tracks, err
}

// ResolveTracksForCount resolves tracks picking the MusicBrainz release whose
// track count best matches fileCount. Returns the resolved MBID.
func (p *Plugin) ResolveTracksForCount(ctx context.Context, release domain.AlbumRelease, fileCount int, torrentTitle string) ([]domain.ExpectedTrack, string, error) {
	tracks, mbid, err := p.resolveTracks(ctx, release, fileCount, torrentTitle)
	return tracks, mbid, err
}

func (p *Plugin) resolveTracks(ctx context.Context, release domain.AlbumRelease, fileCountHint int, torrentTitle string) ([]domain.ExpectedTrack, string, error) {
	if p.mb == nil {
		return nil, "", fmt.Errorf("prowlarr: musicbrainz client not available")
	}

	// 1. Search MusicBrainz for the release group by artist + album.
	rg, err := p.mb.SearchReleaseGroup(ctx, release.Artist, release.Album)
	if err != nil {
		return nil, "", fmt.Errorf("prowlarr: musicbrainz search: %w", err)
	}
	if rg == nil {
		return nil, "", fmt.Errorf("prowlarr: musicbrainz: no release group found for %q - %q", release.Artist, release.Album)
	}

	// 2. Look up releases. When we know the file count, use the search
	// endpoint (one call, all releases with track counts). Otherwise fall
	// back to release-group lookup (first release only, no track counts).
	var bestRelease musicbrainz.ReleaseGroupRelease
	if fileCountHint > 0 {
		allReleases, err := p.mb.SearchReleasesByGroup(ctx, rg.MBID)
		if err != nil {
			return nil, "", fmt.Errorf("prowlarr: musicbrainz search releases: %w", err)
		}
		if allReleases == nil {
			return nil, "", fmt.Errorf("prowlarr: musicbrainz: search API returned no data for group %s (release group may not be indexed)", rg.MBID)
		}
		if len(allReleases) == 0 {
			return nil, "", fmt.Errorf("prowlarr: musicbrainz: release group %s exists but has no indexed releases", rg.MBID)
		}
		bestRelease = pickBestMatchingRelease(allReleases, fileCountHint, torrentTitle)
	} else {
		rgReleases, err := p.mb.LookupReleaseGroup(ctx, rg.MBID)
		if err != nil {
			return nil, "", fmt.Errorf("prowlarr: musicbrainz release group: %w", err)
		}
		if len(rgReleases) == 0 {
			return nil, "", fmt.Errorf("prowlarr: musicbrainz: no releases in group %s", rg.MBID)
		}
		bestRelease = rgReleases[0]
	}

	// 3. Look up the best release with full track details (recordings + artist-credits).
	releaseInfo, err := p.mb.LookupRelease(ctx, bestRelease.ID)
	if err != nil {
		return nil, "", fmt.Errorf("prowlarr: musicbrainz release: %w", err)
	}
	if releaseInfo == nil || len(releaseInfo.Tracks) == 0 {
		return nil, "", fmt.Errorf("prowlarr: musicbrainz: no tracks in release %s", bestRelease.ID)
	}

	// 4. Map to ExpectedTrack list.
	tracks := make([]domain.ExpectedTrack, 0, len(releaseInfo.Tracks))
	for _, t := range releaseInfo.Tracks {
		artist := t.Artist
		if artist == "" {
			artist = release.Artist // fallback to album artist
		}
		duration := int(t.Length) / 1000 // ms → seconds
		tracks = append(tracks, domain.ExpectedTrack{
			TrackNumber: t.Position,
			Artist:      artist,
			Title:       t.Title,
			Duration:    duration,
		})
	}

	return tracks, bestRelease.ID, nil
}

// pickBestMatchingRelease picks the release whose track count is closest to
// the actual file count. When multiple releases tie, word overlap between
// the torrent title and the release title/disambiguation breaks the tie.
func pickBestMatchingRelease(releases []musicbrainz.ReleaseGroupRelease, targetCount int, torrentTitle string) musicbrainz.ReleaseGroupRelease {
	if len(releases) == 0 {
		return musicbrainz.ReleaseGroupRelease{}
	}

	type scored struct {
		release   musicbrainz.ReleaseGroupRelease
		trackDiff int
		wordScore int
	}

	var candidates []scored
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

	// Sort: closest track count first, highest word score breaks ties.
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

// ─── Indexer filtering ───────────────────────────────────────────────

func (p *Plugin) findRuTrackerIndexers(ctx context.Context) ([]prowlarrIndexer, error) {
	all, err := p.torznab.indexers(ctx)
	if err != nil {
		return nil, err
	}

	tag := p.cfg.IndexerTag
	if tag == "" {
		return all, nil
	}

	// Try to resolve tag name to ID. If the tag doesn't exist, fall through
	// to returning all indexers rather than failing — the user may have
	// removed the tag from Prowlarr.
	tagList, err := p.torznab.tags(ctx)
	if err != nil {
		p.log.Warn("prowlarr: tag lookup failed, searching all indexers", "error", err, "component", "prowlarr")
		return all, nil
	}
	var tagID int
	for _, t := range tagList {
		if strings.EqualFold(t.Label, tag) {
			tagID = t.ID
			break
		}
	}
	if tagID == 0 {
		p.log.Warn("prowlarr: tag not found, searching all indexers", "tag", tag, "component", "prowlarr")
		return all, nil
	}

	var matches []prowlarrIndexer
	for _, idx := range all {
		for _, t := range idx.Tags {
			if t == tagID {
				matches = append(matches, idx)
				break
			}
		}
	}
	if len(matches) == 0 {
		p.log.Warn("prowlarr: no indexers with tag, searching all", "tag", tag, "component", "prowlarr")
		return all, nil
	}
	return matches, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────

func parseTitleFromResult(r torznabResult) (artist, album string) {
	return parseTitle(r.Title)
}

// looksLikeFormatMetadata returns true when a string looks like audio format
// metadata rather than an actual album title (e.g. "2025, MP3, 320 kbps").
func looksLikeFormatMetadata(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 30 {
		return false
	}
	// Match patterns like "2025, mp3, 320 kbps", "2025, flac", etc.
	hasFormat := strings.Contains(s, "mp3") || strings.Contains(s, "flac") || strings.Contains(s, "kbps")
	if !hasFormat {
		return false
	}
	// Must also start with a year or be very short.
	parts := strings.SplitN(s, ",", 2)
	if len(parts[0]) == 4 {
		for _, c := range parts[0] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return len(s) < 15
}

func extractYearFromResult(r torznabResult) int {
	return extractYear(r.Title)
}

// Ensure plugin compatibility.
var _ plugin.BasePlugin = (*Plugin)(nil)
