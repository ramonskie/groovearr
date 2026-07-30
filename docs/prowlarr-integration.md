# Prowlarr + qBittorrent Integration — Phase 3 ✅ Complete

> Depends on Phase 1 architecture: `AlbumProvider`, `DownloadClient`, `AlbumRecord`, `AlbumImportHandler`, `compilation_template`.
> See `docs/album-architecture.md` for the foundation interfaces and data model.

## Overview

Phase 3 implements two plugins against the Phase 1 interfaces:

| Plugin | Implements | Purpose |
|--------|-----------|---------|
| `internal/providers/prowlarr/` | `download.AlbumProvider` | Search RuTracker via Prowlarr Torznab, resolve tracks via MusicBrainz |
| `internal/providers/qbittorrent/` | `download.DownloadClient` | Download torrents via qBittorrent WebUI API |

```
Search:    prowlarr.SearchAlbum("Metallica Master of Puppets")
              → Prowlarr Torznab API → RuTracker
              → AlbumRelease{magnet, size, seeders}

Resolve:   prowlarr.ResolveTracks(release)
              → MusicBrainz API (MBID by artist+album)
              → []ExpectedTrack{{n:1, artist:"Metallica", title:"Battery"}, ...}

Download:  qbittorrent.AddDownload(magnet, "music", "./downloads/")
              → torrent hash → monitors progress → reports completion

Import:    AlbumImportHandler (from Phase 1)
              → folder scan → match → tag → rename → library
```

## Prowlarr Plugin

### Architecture

```
internal/providers/prowlarr/
  ├── plugin.go       — AlbumProvider implementation
  ├── factory.go      — Factory + ConfigSchema
  ├── torznab.go      — Torznab XML/HTTP client (search, caps)
  └── factory_test.go — Config validation tests
```

### Implements

```go
var _ download.AlbumProvider = (*Plugin)(nil)
var _ plugin.ConfigSchemaProvider = (*factory)(nil)
```

### Config

```json
"prowlarr": {
  "url": "http://localhost:9696",
  "api_key": "...",
  "indexer_tag": "groovearr",
  "categories": [3040]
}
```

### SearchAlbum Flow

```
SearchAlbum("Metallica Master of Puppets")
  │
  ├─ GET /api/v1/indexer → find indexers tagged "groovearr"
  │   → filter by name matching "rutracker" (case-insensitive)
  │
  ├─ GET /api/v1/indexer/{id}/newznab?t=music&q=Metallica+Master+of+Puppets&cat=3040
  │   → parse Torznab RSS XML → []TorznabResult
  │
  └─ Map each TorznabResult → domain.AlbumRelease{
        Artist: torznab.Artist,
        Album:  torznab.Album,
        Year:   torznab.Year,
        Size:   torznab.Size,
        Seeders: torznab.Seeders,
        MagnetURI: derive from infohash,
        Format: "flac",
      }
```

### ResolveTracks Flow

```
ResolveTracks(release)
  │
  ├─ MusicBrainz: GET /ws/2/release-group/?query=artist:"Metallica"+release:"Master of Puppets"
  │   → get MBID
  │
  ├─ MusicBrainz: GET /ws/2/release-group/{mbid}?inc=releases
  │   → get first release ID
  │
  └─ MusicBrainz: GET /ws/2/release/{releaseID}?inc=recordings+artist-credits
      → for each track:
          ExpectedTrack{
            TrackNumber: position,
            Artist:      artist-credit.name,
            Title:       recording.title,
            Duration:    recording.length / 1000,
          }
```

AlbumType and CoverURL also come from MusicBrainz:
- `release-group.type` → "Album" or "Compilation"
- `release.cover-art-archive.front` → CoverURL from Cover Art Archive

### Torznab client (internal to prowlarr package)

```go
// torznab.go — not a separate package, just a file within prowlarr/
type torznabClient struct {
    baseURL string
    apiKey  string
    http    *http.Client
}

func (c *torznabClient) caps(ctx context.Context, indexerID int) (*torznabCaps, error)
func (c *torznabClient) searchMusic(ctx context.Context, indexerID int, query string, categories []int) ([]torznabResult, error)
func (c *torznabClient) indexers(ctx context.Context) ([]prowlarrIndexer, error)

// XML parsing using encoding/xml
type torznabResult struct {
    Title    string
    GUID     string
    Size     int64
    Link     string
    Seeders  int
    Peers    int
    Infohash string
    Artist   string
    Album    string
    Year     int
}

// Prowlarr REST JSON
type prowlarrIndexer struct {
    ID     int              `json:"id"`
    Name   string           `json:"name"`
    Tags   []prowlarrTag    `json:"tags"`
}
```

### Indexer Tag Filtering

User tags RuTracker in Prowlarr with `groovearr` tag. Plugin discovers tagged indexers:

```go
func (p *Plugin) findRuTrackerIndexers(ctx context.Context) ([]prowlarrIndexer, error) {
    all, _ := p.torznab.indexers(ctx)
    var matches []prowlarrIndexer
    for _, idx := range all {
        for _, t := range idx.Tags {
            if strings.EqualFold(t.Name, p.cfg.IndexerTag) {
                matches = append(matches, idx)
                break
            }
        }
    }
    if len(matches) == 0 {
        return nil, fmt.Errorf("prowlarr: no indexers with tag %q", p.cfg.IndexerTag)
    }
    return matches, nil
}
```

## qBittorrent Plugin

### Architecture

```
internal/providers/qbittorrent/
  ├── plugin.go       — DownloadClient implementation
  ├── factory.go      — Factory + ConfigSchema
  └── factory_test.go — Config validation tests
```

### Implements

```go
var _ download.DownloadClient = (*Plugin)(nil)
var _ plugin.ConfigSchemaProvider = (*factory)(nil)
```

### Config

```json
"qbittorrent": {
  "url": "http://localhost:8080",
  "username": "admin",
  "password": "...",
  "category": "music",
  "remove_completed": true
}
```

### API

Uses qBittorrent WebUI API v2. All endpoints return JSON.

| Method | API Call | Purpose |
|--------|----------|---------|
| `CheckConnection` | `GET /api/v2/app/version` | Reachability check |
| `AddDownload` | `POST /api/v2/torrents/add` (multipart) | Add magnet URI |
| `GetStatus` | `GET /api/v2/torrents/info?hashes=` | Progress, state, file path |
| `GetProgress` | `GET /api/v2/torrents/info?hashes=` | Bytes transferred, speed |
| `Cancel` | `POST /api/v2/torrents/delete` | Remove torrent (+ files) |

**Auth**: Cookie-based. `POST /api/v2/auth/login` returns SID cookie. Auto re-auth on 403.

**State mapping** (qBittorrent → download.State):

| qBittorrent | Groovearr |
|-------------|-----------|
| `amount_left == 0` | `StateImportPending` |
| `downloading`, `forcedDL` | `StateDownloading` |
| `error`, `missingFiles` | `StateFailed` |
| metadata stalled / initial | `StateDownloading` |

**Completion detection**:

```go
func (t *Torrent) IsComplete() bool {
    return t.AmountLeft == 0 && t.CompletionOn > 0
}
```

### HTTP Client

```go
type qbitClient struct {
    baseURL string
    http    *http.Client  // with cookie jar for SID
}

func (c *qbitClient) login(ctx context.Context, user, pass string) error
func (c *qbitClient) addTorrent(ctx context.Context, magnet, category, savepath string) (string, error)
func (c *qbitClient) getInfo(ctx context.Context, hashes ...string) ([]torrentInfo, error)
func (c *qbitClient) deleteTorrents(ctx context.Context, hashes []string, deleteFiles bool) error

type torrentInfo struct {
    Hash         string  `json:"hash"`
    Name         string  `json:"name"`
    State        string  `json:"state"`
    Progress     float64 `json:"progress"`
    Size         int64   `json:"size"`
    TotalSize    int64   `json:"total_size"`
    SavePath     string  `json:"save_path"`
    ContentPath  string  `json:"content_path"`
    Category     string  `json:"category"`
    DlSpeed      int64   `json:"dlspeed"`
    AmountLeft   int64   `json:"amount_left"`
    CompletionOn int64   `json:"completion_on"`
    MagnetURI    string  `json:"magnet_uri"`
}
```

## Config (full)

```json
{
  "album_sources": ["prowlarr"],
  "track_sources": ["deezer", "soulseek"],
  "download_client": "qbittorrent",

  "prowlarr": {
    "url": "http://localhost:9696",
    "api_key": "your-prowlarr-api-key",
    "indexer_tag": "groovearr",
    "categories": [3040]
  },
  "qbittorrent": {
    "url": "http://localhost:8080",
    "username": "admin",
    "password": "your-qbittorrent-password",
    "category": "music",
    "remove_completed": true
  },
  "soulseek": { "...": "..." },
  "deezer": { "...": "..." }
}
```

## Wiring (main.go)

```go
import (
    "github.com/ramonskie/groovearr/internal/providers/prowlarr"
    "github.com/ramonskie/groovearr/internal/providers/qbittorrent"
)

// Register album providers
albumRegistry.RegisterFactory(prowlarr.Factory)

// Register download clients
downloadClientRegistry.RegisterFactory(qbittorrent.Factory)

// Existing track plugins
pluginRegistry.RegisterFactory(soulseek.Factory)
pluginRegistry.RegisterFactory(deezer.Factory)
```

## Full End-to-End Flow

```
1. User downloads album "Master of Puppets"

2. Orchestrator album pass:
   album_sources = ["prowlarr"]
   → prowlarr.SearchAlbum("Metallica Master of Puppets")
     → Prowlarr → find RuTracker indexer (tag="groovearr")
     → Torznab search → AlbumRelease{magnet, 350MB, 45 seeds}

3. Resolve tracks:
   → prowlarr.ResolveTracks(release)
     → MusicBrainz → mbid → recordings → []ExpectedTrack{
         {n:1, artist:"Metallica", title:"Battery"},
         {n:2, artist:"Metallica", title:"Master of Puppets"}, ...
       }
     → AlbumType = "Album" (from release-group type)

4. Queue:
   → QueueAlbum(release, tracks, "qbittorrent")
   → 1 AlbumRecord

5. Dispatch:
   → DownloadClientRegistry.Get("qbittorrent").AddDownload(magnet, "music", "./downloads/")
   → returns torrent hash

6. Monitor:
   → qbittorrent.GetStatus(hash) → progress 45% → ... → 100%
   → IsComplete() → StateImportPending

7. Import (AlbumImportHandler — Phase 1):
   → Scan folder → 8 FLAC files
   → Match to ExpectedTracks
   → Tag, rename, library insert
   → 8 tracks in library ✓

8. Cleanup:
   → qbittorrent.Cancel(hash, remove_completed=true) — removes from client
```

## Implementation Order

| Seq | Component | Est. lines | Status |
|-----|-----------|------------|--------|
| 1 | `internal/providers/qbittorrent/` — qbitClient + Plugin + factory | ~300 | ✅ |
| 2 | `internal/providers/prowlarr/` — torznabClient + Plugin + factory | ~400 | ✅ |
| 3 | MusicBrainz track listing in prowlarr.ResolveTracks() | ~150 | ✅ |
| 4 | Wire in main.go | ~10 | ✅ |
| 5 | Config: prowlarr + qbittorrent sections | ~20 | ✅ |

Total: ~880 lines. ✅ Complete.

## Dependencies (Phase 1 must be complete) ✅ All present

- `download.AlbumProvider` interface
- `download.DownloadClient` interface + registry
- `download.Record` album fields
- `domain.AlbumRelease` + `ExpectedTrack`
- `download.QueueAlbum()` method
- `download.AlbumImportHandler`
- `compilation_template` config + Renamer support
- Album-aware orchestrator + dispatch
