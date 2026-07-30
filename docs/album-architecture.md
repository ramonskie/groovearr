# Album Download Architecture — Phase 1: Foundation

## What This Covers

Phase 1 introduces the architectural foundation for album-level downloads: new interfaces, data model, import pipeline, and compilation support. **No Prowlarr or qBittorrent plugins.** Existing plugins (soulseek, deezer) remain unchanged.

Phase 2: Delete leftover packages from earlier attempt.
Phase 3: Prowlarr + qBittorrent plugins — see `docs/prowlarr-integration.md`. ✅ **Complete**.

## Problem

Groovearr's download pipeline is track-based: 1 download record = 1 audio file. Torrent sources provide full albums in a single download unit. This creates four problems:

1. **Queue mismatch**: Downloading an album queues N individual track records, but torrents are 1 download = N tracks
2. **Import mismatch**: 1 torrent = a folder of files, not 1 file
3. **Source routing**: Album sources (prowlarr) and track sources (deezer, soulseek) need separate ordering
4. **VA compilations**: Multiple artists per album. `{artist}/{album}` template scatters tracks

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                        SEARCH LAYER                          │
├──────────────────────────┬───────────────────────────────────┤
│      AlbumProvider       │          TrackPlugin              │
│   (new interface)        │      (existing download.Plugin)   │
│                          │                                   │
│  SearchAlbum()           │      Search()                     │
│  ResolveTracks()         │                                   │
│                          │                                   │
│  (prowlarr — Phase 3)    │      deezer, soulseek             │
└──────────────────────────┴───────────────────────────────────┘
                    │                       │
                    ▼                       ▼
┌──────────────────────────────────────────────────────────────┐
│                    ORCHESTRATOR                              │
│                                                              │
│  Album request → album_sources first, fallback to track     │
│  Track request → track_sources only                         │
└──────────────────────────────────────────────────────────────┘
                    │
                    ▼
┌──────────────────────────────────────────────────────────────┐
│                   DOWNLOAD LAYER                             │
├──────────────────────────────────────────────────────────────┤
│                  DownloadClient                              │
│                 (new interface)                              │
│                                                              │
│  AddDownload(uri, category, savepath) → providerID           │
│  GetStatus(providerID) → State, Progress, FilePath           │
│  GetProgress(providerID) → bytes, speed                      │
│  Cancel(providerID, remove)                                  │
│  MaxConcurrent() / DownloadTimeout()                         │
│                                                              │
│  (qbittorrent — Phase 3)                                     │
└──────────────────────────────────────────────────────────────┘
                    │
                    ▼
┌──────────────────────────────────────────────────────────────┐
│                   IMPORT LAYER                               │
├──────────────────────────────────────────────────────────────┤
│  Album Import (new)          Track Import (existing)         │
│                                                              │
│  1. Detect compilation                                        │
│  2. Select template         1 file → rename → tag → library   │
│  3. Scan folder                                               │
│  4. Match to ExpectedTracks                                   │
│  5. Per-file: tag, rename                                     │
│     (compilation-aware)                                       │
│  6. Link N track IDs → 1 record                               │
│  7. StateImported                                              │
└──────────────────────────────────────────────────────────────┘
```

## New Interfaces

### AlbumProvider

```go
// internal/download/album.go
type AlbumProvider interface {
    plugin.BasePlugin

    SearchAlbum(ctx context.Context, query string) ([]domain.AlbumRelease, error)
    ResolveTracks(ctx context.Context, release domain.AlbumRelease) ([]ExpectedTrack, error)
}

type ExpectedTrack struct {
    TrackNumber int    `json:"track_number"`
    Artist      string `json:"artist"`      // empty = same as album artist
    Title       string `json:"title"`
    Duration    int    `json:"duration"`    // seconds, 0 if unknown
}
```

### DownloadClient

```go
// internal/download/client.go
type DownloadClient interface {
    plugin.BasePlugin

    AddDownload(ctx context.Context, uri, category, savepath string) (string, error)
    GetStatus(ctx context.Context, providerID string) (*Record, error)
    GetProgress(ctx context.Context, providerID string) (*Progress, error)
    Cancel(ctx context.Context, providerID string, remove bool) error

    MaxConcurrent() int
    DownloadTimeout() time.Duration
}
```

### DownloadClientRegistry

```go
// internal/download/client_registry.go
type DownloadClientRegistry struct {
    mu      sync.RWMutex
    clients map[string]DownloadClient
}
func (r *DownloadClientRegistry) Register(factory plugin.PluginFactory) {}
func (r *DownloadClientRegistry) Get(name string) DownloadClient        {}
func (r *DownloadClientRegistry) InitAll(configs, resources) error     {}
```

## Data Model

### download.Record — add album fields

```go
type Record struct {
    // ... existing fields ...

    AlbumType       string          `json:"album_type,omitempty"`       // "Album", "Compilation"
    AlbumTracks     []ExpectedTrack  `json:"album_tracks,omitempty"`    // expected tracks
    DownloadClient  string          `json:"download_client,omitempty"`  // dispatch target
    MagnetURI       string          `json:"magnet_uri,omitempty"`       // for torrent sources
    FolderPath      string          `json:"folder_path,omitempty"`      // set by import
    ImportedTrackIDs []int64        `json:"imported_track_ids,omitempty"`
}

func (r *Record) IsAlbum() bool { return r.AlbumType != "" }
```

### domain.AlbumRelease (new)

```go
// internal/domain/album.go
type AlbumRelease struct {
    SourceName string          `json:"source_name"`
    Artist     string          `json:"artist"`
    Album      string          `json:"album"`
    Year       int             `json:"year"`
    Format     string          `json:"format"`
    Size       int64           `json:"size"`
    Seeders    int             `json:"seeders"`
    MagnetURI  string          `json:"magnet_uri"`
    CoverURL   string          `json:"cover_url,omitempty"`
    AlbumType  string          `json:"album_type"`
    MBID       string          `json:"mbid,omitempty"`
    Tracks     []ExpectedTrack `json:"tracks,omitempty"`
}
```

## Compilation Handling

### Template

```json
"library": {
  "folder_template": "{artist}/{album} ({year})/{track:02d} - {title}",
  "compilation_template": "Various Artists/{album} ({year})/{track:02d}. {artist} - {title}"
}
```

### Detection

```go
func (r *Record) IsCompilation() bool {
    if r.AlbumType == "Compilation" { return true }
    artists := make(map[string]bool)
    for _, t := range r.AlbumTracks {
        if t.Artist != "" { artists[t.Artist] = true }
    }
    return len(artists) > 1
}
```

### Import Handler

```go
// internal/download/handler_album_import.go

type AlbumImportHandler struct {
    scanner     *library.Scanner
    tagger      *tagging.Writer
    renamer     *library.Renamer       // standard template
    compRenamer *library.Renamer       // compilation template
    libStore    library.Store
    dlStore     Store
}

func (h *AlbumImportHandler) Handle(ctx context.Context, record *Record) error {
    isComp := record.IsCompilation()

    files, _ := h.scanner.ScanFolder(record.FolderPath)
    matches := h.matchFiles(files, record.AlbumTracks)

    var importedIDs []int64
    for _, m := range matches {
        artist := m.Track.Artist
        if artist == "" { artist = record.Artist }

        h.tagger.Write(m.FilePath, tagging.Meta{
            Artist: artist, Album: record.Album,
            Title: m.Track.Title, TrackNumber: m.Track.TrackNumber,
        })

        rn := h.renamer
        if isComp { rn = h.compRenamer }
        dest, _ := rn.Rename(m.FilePath, library.FileMeta{
            Artist: artist, Album: record.Album,
            Title: m.Track.Title, TrackNum: m.Track.TrackNumber, Year: record.Year,
        })

        trackID, _ := h.libStore.UpsertTrack(ctx, library.Track{
            Artist: artist, Album: record.Album,
            Title: m.Track.Title, TrackNumber: m.Track.TrackNumber,
            Year: record.Year, FilePath: dest,
        })
        importedIDs = append(importedIDs, trackID)
    }
    return h.dlStore.SetAlbumImportedTracks(ctx, record.ID, importedIDs)
}
```

## Service Changes

### download.Service — new QueueAlbum method

```go
func (s *Service) QueueAlbum(ctx context.Context, release domain.AlbumRelease, tracks []ExpectedTrack, downloadClient string) (string, error) {
    record := &Record{
        ID:             uuid.New().String(),
        SourceName:     release.SourceName,
        State:          StateQueued,
        AlbumType:      release.AlbumType,
        AlbumTracks:    tracks,
        DownloadClient: downloadClient,
        Artist:         release.Artist,
        Album:          release.Album,
        Year:           release.Year,
        MagnetURI:      release.MagnetURI,
        Size:           release.Size,
        CoverURL:       release.CoverURL,
    }
    // ... store, publish event ...
}
```

### MonitoringService — album-aware dispatch

```go
func (m *MonitoringService) startQueuedDownloads() {
    for _, rec := range queued {
        if rec.IsAlbum() {
            m.startAlbumDownload(rec)  // → DownloadClientRegistry.Get(downloadClient)
        } else {
            m.startSingleDownload(rec)  // existing flow
        }
    }
}
```

## Config

```json
{
  "album_sources": [],
  "track_sources": ["deezer", "soulseek"],
  "download_client": "",

  "library": {
    "download_path": "./downloads",
    "library_path": "./music",
    "folder_template": "{artist}/{album} ({year})/{track:02d} - {title}",
    "compilation_template": "Various Artists/{album} ({year})/{track:02d}. {artist} - {title}"
  }
}
```

`album_sources` empty until Phase 3. `download_client` empty until Phase 3. Phase 1 ships with the architecture ready but no album providers or download clients installed — nothing breaks.

## Full Flow: Single-Artist Album (Phase 3+)

```
1. User downloads "Master of Puppets"

2. Orchestrator album pass:
   album_sources = ["prowlarr"]
   → prowlarr.SearchAlbum("Metallica Master of Puppets")
     → Prowlarr → find RuTracker indexer (tag="groovearr")
     → Torznab search → AlbumRelease{magnet, 350MB, 45 seeds}

3. Resolve tracks:
   → prowlarr.ResolveTracks(release)
     → MusicBrainz → mbid → recordings → []ExpectedTrack{...}

4. QueueAlbum → 1 AlbumRecord{DownloadClient:"qbittorrent"}

5. Dispatch:
   → qbittorrent.AddDownload(magnet, "music", "./downloads/")
   → torrent hash

6. Monitor: qbittorrent.GetStatus(hash) → progress → 100%

7. AlbumImportHandler → scan folder → match → tag → rename → library

8. Cleanup: qbittorrent.Cancel(hash, remove_completed=true)
```

## Full Flow: VA Compilation (Dominator 2015)

```
1. Phase 3 scenario: prowlarr.SearchAlbum() returns compilation release
   AlbumRelease{artist:"Various Artists", albumType:"Compilation", tracks:[
     {n:1, artist:"Angerfist", title:"Raise Your Fist"},
     {n:2, artist:"Miss K8", title:"Battlefield"}, ...
   ]}

2. QueueAlbum → 1 AlbumRecord{AlbumType:"Compilation"}

3. qBittorrent downloads folder

4. AlbumImportHandler.Handle():
   IsCompilation() → true → compilation_template
   For each file:
     Tag: artist="Angerfist", album="Dominator 2015"
     Rename: "Various Artists/Dominator 2015 (2015)/01. Angerfist - Raise Your Fist.flac"
     Library: track with artist="Angerfist"
   All 40 tracks → one album folder ✓
```

## What Gets Built (Phase 1)

| # | Component | Package |
|---|-----------|---------|
| 1 | `AlbumProvider` interface + `ExpectedTrack` | `internal/download/album.go` |
| 2 | `DownloadClient` interface | `internal/download/client.go` |
| 3 | `DownloadClientRegistry` | `internal/download/client_registry.go` |
| 4 | `AlbumRelease` domain type | `internal/domain/album.go` |
| 5 | `Record` album fields | `internal/download/types.go` |
| 6 | `AlbumImportHandler` | `internal/download/handler_album_import.go` |
| 7 | `QueueAlbum()` on download.Service | `internal/download/service.go` |
| 8 | `compilation_template` config + `PathResolver` support | `internal/library/` |
| 9 | Compilation-aware `Renamer` | `internal/library/renamer.go` |
| 10 | Orchestrator: album-first search routing | `internal/download/orchestrator.go` |
| 11 | MonitoringService: album-aware dispatch | `internal/download/monitor_dispatch.go` |
| 12 | Config migration | `internal/config/config.go` |

## What Gets Deleted (Phase 2)

| Package | Reason |
|---------|--------|
| ... | (nothing left after revert — already clean) |

## Backward Compatibility

- `download.Plugin` unchanged — Soulseek/Deezer untouched
- `download.MonitoredProvider` unchanged — existing plugins keep working
- `download_order` config replaced by `album_sources` + `track_sources`
- Default config matches current behavior (album_sources=[], track_sources=["deezer","soulseek"])
- No breaking changes until Phase 3 installs actual album providers
