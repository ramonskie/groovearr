// Package prowlarr implements an AlbumProvider plugin that searches RuTracker
// via Prowlarr's Torznab API and resolves track listings through an injected
// discovery.ReleaseResolver (never a concrete sibling provider — AGENTS §1).
package prowlarr

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/plugin"
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
// Track metadata resolution is delegated to a discovery.ReleaseResolver
// injected at composition time via SetReleaseResolver, so prowlarr names no
// concrete metadata provider and holds no private MusicBrainz client.
type Plugin struct {
	cfg       Config
	torznab   *torznabClient
	log       *slog.Logger
	mu        sync.Mutex // guards connected and resolver
	resolver  discovery.ReleaseResolver
	connected bool
}

var _ download.AlbumProvider = (*Plugin)(nil)
var _ plugin.BasePlugin = (*Plugin)(nil)
var _ discovery.ReleaseResolverConsumer = (*Plugin)(nil)

func newPlugin(cfg Config, logger *slog.Logger) (*Plugin, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.IndexerTag == "" {
		cfg.IndexerTag = defaultIndexerTag
	}

	p := &Plugin{
		cfg:     cfg,
		torznab: newTorznabClient(cfg.URL, cfg.APIKey),
		log:     logger,
	}
	return p, nil
}

// SetReleaseResolver injects the shared release resolver. app.go calls this on
// every registered plugin after InitAll, so a consumer built in any order still
// receives the one process-wide resolver (discovery.ReleaseResolverConsumer).
func (p *Plugin) SetReleaseResolver(r discovery.ReleaseResolver) {
	p.mu.Lock()
	p.resolver = r
	p.mu.Unlock()
}

// ─── plugin.BasePlugin ───────────────────────────────────────────────

func (p *Plugin) Name() string        { return pluginName }
func (p *Plugin) DisplayName() string { return displayName }
func (p *Plugin) IsConfigured() bool  { return p.cfg.URL != "" && p.cfg.APIKey != "" }
func (p *Plugin) Connected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.connected
}
func (p *Plugin) CapabilityStatus() map[string]string {
	return map[string]string{"album_search": p.capStatus()}
}

func (p *Plugin) capStatus() string {
	if !p.IsConfigured() {
		return "not_configured"
	}
	if p.Connected() {
		return "connected"
	}
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

// ResolveTracks resolves the expected track listing for an album through the
// injected release resolver. The resolver uses the first release in the group.
func (p *Plugin) ResolveTracks(ctx context.Context, release domain.AlbumRelease) ([]domain.ExpectedTrack, error) {
	tracks, _, err := p.resolveTracks(ctx, release, 0, "")
	return tracks, err
}

// ResolveTracksForCount resolves tracks picking the release whose track count
// best matches fileCount. Returns the resolved MBID.
func (p *Plugin) ResolveTracksForCount(ctx context.Context, release domain.AlbumRelease, fileCount int, torrentTitle string) ([]domain.ExpectedTrack, string, error) {
	tracks, mbid, err := p.resolveTracks(ctx, release, fileCount, torrentTitle)
	return tracks, mbid, err
}

func (p *Plugin) resolveTracks(ctx context.Context, release domain.AlbumRelease, fileCountHint int, torrentTitle string) ([]domain.ExpectedTrack, string, error) {
	p.mu.Lock()
	resolver := p.resolver
	p.mu.Unlock()
	if resolver == nil {
		return nil, "", fmt.Errorf("prowlarr: release resolver not configured")
	}

	// Resolve through the injected capability. The resolver owns the
	// provider-specific search/lookup/pick logic; prowlarr only maps the
	// neutral discovery.TrackInfo values onto its domain track type.
	infos, mbid, err := resolver.ResolveTracksForCount(ctx, release.Artist, release.Album, fileCountHint, torrentTitle)
	if err != nil {
		return nil, "", fmt.Errorf("prowlarr: resolve tracks: %w", err)
	}

	tracks := make([]domain.ExpectedTrack, 0, len(infos))
	for _, t := range infos {
		artist := t.ArtistName
		if artist == "" {
			artist = release.Artist // fallback to album artist
		}
		tracks = append(tracks, domain.ExpectedTrack{
			TrackNumber: t.TrackNumber,
			Artist:      artist,
			Title:       t.Title,
			Duration:    int(t.DurationMs / 1000), // ms → seconds
		})
	}

	return tracks, mbid, nil
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
