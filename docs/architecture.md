# Groovearr — Architecture

> Go rewrite of SoulSync. Self-hosted music download manager.
> Go 1.26, stdlib `net/http`, `modernc.org/sqlite` (CGo-free), vanilla JS SPA embedded via `go:embed`.

## High-Level Component Diagram

```
cmd/groovearr/main.go  ─── entry point, wires all components via dependency injection

  config.Persistence        download.Registry          events.InMemoryEventBus
  │ JSON config              │ plugin lookup             │ pub/sub decoupler
  │ hot-reload               │                           │
  │                          ├─ soulseek.Client          ├─ CompletedDownloadService
  │                          └─ deezer.DownloadClient    ├─ SSENotifier
  │                          │                           │
  │                          │ download.MonitoringService│
  │                          │ poll loop (1s ticker)     │
  │                          │ state machine driver      │
  │                          │ orphan recovery           │
  │                          │ Provider: MonitoredProv…  │
  │                                                     │
  ├─ library.sqlite.Store    download.DownloadService    api.Server
  │  SQLite (artists,        │ queue/cancel/retry        │ HTTP :8008
  │  albums, tracks,         │                           │ embedded SPA
  │  playlists)              │                           │ 26 endpoints
  │                          │                           │ SSE stream
  ├─ library.Scanner         │                           │
  │  filesystem → SQLite     │                           │
  │                                                     │
  ├─ library.Renamer         download.Orchestrator       playlist.Service
  │  folder template         │ route search/download     │ import/sync
  │  {artist}/{album}...     │ cross-source fallback     │ playlist.Registry
  │                                                     │ deezer.PlaylistSource
└─ matching.Engine                                    │
   track matching                                     │
   version awareness

   metadata.Registry          metadata.MetadataResolver
   │ provider lookup          │ queue-time enrichment
   │ configurable order       │ primaryArtist fallback
   ├─ musicbrainz.Client      │ album + cover lookup
   ├─ deezer.DownloadClient   │
   └─ coverartarchive.Client
```

## Package Map

| Package | Purpose | Key Types |
|---------|---------|-----------|
| `cmd/groovearr` | Entry point, wiring, graceful shutdown | `main()` |
| `internal/api` | HTTP server + 26 REST handlers + SSE endpoint | `Server`, handlers |
| `internal/config` | JSON config load/validate/persist (thread-safe) | `Config`, `Persistence` |
| `internal/domain` | Core domain types (no behavior, plain structs) | `Track`, `Album`, `Artist`, `Playlist`, `PlaylistTrack`, `DownloadRecord`, `DownloadState`, `SearchResult`, `TrackResult`, `AlbumResult` |
| `internal/download` | Plugin contract, registry, monitoring poll loop, import pipeline | `Plugin`, `MonitoredProvider`, `MonitoringService`, `Registry`, `DownloadService`, `Orchestrator`, import handlers |
| `internal/metadata` | Metadata provider interface + resolver + registry | `Provider`, `MetadataResolver`, `Registry`, `CoverResult`, `TrackMetadata` |
| `internal/providers/musicbrainz` | MusicBrainz metadata provider (recording search, release lookup) | `Client` (implements `metadata.Provider`) |
| `internal/providers/coverartarchive` | Cover Art Archive (MBID-based cover lookup) | `Client` (implements `metadata.Provider`) |
| `internal/providers/deezer` | Deezer plugin — download (ARL) + metadata (public API) | `DownloadClient` (implements `download.Plugin` + `metadata.Provider`), `Client` (API) |
| `internal/download/sqlite` | SQLite download record store | `Store` (implements `DownloadStore`) |
| `internal/events` | In-memory pub/sub event bus | `InMemoryEventBus`, 8 topic constants |
| `internal/library` | Library store interface, scanner, renamer, path resolver, filename parser | `Store` (interface), `Scanner`, `Renamer`, `ParseArtistTitle`, `ParseAlbumDir` |
| `internal/library/sqlite` | SQLite library store (artists, albums, tracks, playlists) | `Store` (implements `library.Store`) |
| `internal/matching` | Cross-source track matching with version awareness | `Engine` |
| `internal/playlist` | Playlist service, registry, source interface | `Service`, `Registry`, `Source` (interface) |
| `internal/playlist/deezer` | Deezer playlist source plugin | `PlaylistSource` |
| `internal/sanitize` | Filename/path sanitization | `PathSegment`, `TrimLeadingDots` |
| `internal/sse` | Server-Sent Events hub + notifier | `SSEHub`, `SSENotifier` |
| `internal/tagging` | Audio metadata tag writing (ID3, FLAC) | `TagWriter` |

## Domain Model

### Library (SQLite: `artists`, `albums`, `tracks`)

```
Artist ──1:N──> Album ──1:N──> Track
  id              id              id
  name            artist_id       album_id
  genres[]        title           artist_id
  thumb_url       year            title
  external_ids    genres[]        track_number
                  album_type      disc_number
                  thumb_url       duration (ms)
                  external_ids    file_path
                                  bitrate
                                  file_size
                                  external_ids (8 services)
```

### Downloads (SQLite: `downloads`, `download_events`)

```
DownloadRecord          DownloadState (lifecycle)
  id                    queued → downloading → importPending
  source_name                     → importing → imported
  filename                        └─ failed / ignored
  state
  progress (0-100)     DownloadEvent
  size/transferred       id, download_id
  speed                  type, payload, timestamp
  file_path
  error                 Terminal states:
  metadata              imported, failed, ignored
  playlist_id
```

### Playlists (SQLite: `playlists`, `playlist_tracks`)

```
Playlist               PlaylistTrack
  id                    playlist_id
  source                position
  source_playlist_id    source_track_id
  name                  title, artist, album
  cover_url             duration_ms, isrc
  owner_name            track_id (→ tracks.id, nullable)
  auto_sync
```

### Search

```
SearchResult          TrackResult           AlbumResult
  username              (embeds SearchResult)  username
  filename              artist                 album_path
  size                  title                  album_title
  bitrate               album                  artist
  duration              track_number           track_count
  quality               cover_url              total_size
  slots/speed/queue                          tracks[] (TrackResult)
                                             dominant_quality
```

## Download Pipeline

### State Machine

```
                    ┌──────────┐
                    │  queued  │  DownloadService.Queue()
                    └────┬─────┘
                         │ MonitoringService detects (DB scan)
                    ┌────▼──────┐
               ┌────│downloading│  MonitoredProvider.StartDownload() + MonitorService polls
               │    └────┬──────┘
               │         │ MonitoredProvider.GetStatus() → imported
               │    ┌────▼──────────┐
               │    │ importPending │  MonitoringService fires TopicDownloadCompleted
               │    └────┬──────────┘
               │         │ CompletedDownloadService picks up
               │    ┌────▼────┐
               │    │importing│  Atomic CAS (TransitionState)
               │    └────┬────┘
                │         │ Import handler chain (sequential):
                │         │  1. TagValidator → verifies file tags match expected metadata
                │         │  2. FileRenamer → moves to library path
                │         │  3. CoverArt → downloads cover.jpg from CoverURL
                │         │  4. TagWriter → ID3/FLAC tags
                │         │  5. LibraryImporter → artist→album→track in SQLite
                │         │  6. MetadataEnrichment → album resolution, ISRC, genres, covers, MBIDs, thumb_url sync
                │         │  7. PlaylistLinker → updates playlist_tracks
                │         │  8. SSENotifier → broadcasts completion
               │    ┌────▼────┐         ┌────────┐
               └───►│ failed  │         │imported│
                    └────┬────┘         └────────┘
                         │ MonitoringService retry scan
                         └──→ queued
```

### Event Flow

```
DownloadService.Queue()
  └─ Publish(TopicDownloadQueued, record)

MonitoringService.tick() (1s poll loop)
  ├─ startQueuedDownloads() → TransitionState(queued→downloading)
  │   └─ Publish(TopicDownloadStateChanged, record)
  ├─ pollActiveDownloads() → GetStatus / GetProgress per active download
  │   └─ Publish(TopicDownloadProgress, record)             // per tick
  ├─ handleProviderState() → on provider-reported completion
  │   └─ Publish(TopicDownloadCompleted, record)            // download done
  └─ failRecord() → on error
      └─ Publish(TopicDownloadFailed, record)

CompletedDownloadService (subscribes to TopicDownloadCompleted)
  ├─ Publish(TopicImportStarted, record)          // importing
  ├─ Handler chain executes
  ├─ Publish(TopicImportCompleted, record)        // success
  └─ Publish(TopicImportFailed, record)           // failure

SSENotifier (subscribes to state/progress/completed/failed)
  └─ SSEHub.Broadcast(event) → all connected SSE clients
```

### Topics

| Topic Constant | String | Payload | Trigger |
|----------------|--------|---------|---------|
| `TopicDownloadQueued` | `download:queued` | `*DownloadRecord` | `DownloadService.Queue()` |
| `TopicDownloadStateChanged` | `download:stateChanged` | `*DownloadRecord` | MonitoringService state transitions |
| `TopicDownloadProgress` | `download:progress` | `*DownloadRecord` | MonitoringService progress poll |
| `TopicDownloadCompleted` | `download:completed` | `*DownloadRecord` | MonitoringService on download done |
| `TopicDownloadFailed` | `download:failed` | `*DownloadRecord` | MonitoringService on download failure |
| `TopicImportStarted` | `import:started` | `*DownloadRecord` | CompletedDownloadService start |
| `TopicImportCompleted` | `import:completed` | `*DownloadRecord` | Import handler chain success |
| `TopicImportFailed` | `import:failed` | `*DownloadRecord` | Import handler chain failure |

## Plugin System

### Download Plugin Interfaces

Plugins implement two interfaces: `Plugin` for search/discovery and `MonitoredProvider` for the download lifecycle. The `Plugin` interface extends `plugin.BasePlugin` (name, connection check, configuration). `MonitoredProvider` replaces the download-specific methods that were previously on `Plugin`, giving each provider ownership of its own download goroutines.

```go
// Plugin: search and discovery (extends plugin.BasePlugin)
type Plugin interface {
    plugin.BasePlugin

    // Search queries the source and returns matching tracks and albums.
    Search(ctx context.Context, query string) ([]TrackResult, []AlbumResult, error)
}

// MonitoredProvider: download lifecycle (each provider owns its downloads)
type MonitoredProvider interface {
    // StartDownload initiates a non-blocking download. Returns a
    // provider-managed download ID for subsequent status queries.
    StartDownload(ctx context.Context, meta DownloadMeta) (string, error)

    // GetStatus returns the current state of a tracked download.
    GetStatus(ctx context.Context, providerID string) (*DownloadRecord, error)

    // GetProgress returns live byte-level progress for a download.
    // Providers that do not support progress reporting return nil, nil.
    GetProgress(ctx context.Context, providerID string) (*Progress, error)

    // Cancel cancels an active download. If remove is true, the provider
    // drops internal tracking.
    Cancel(ctx context.Context, providerID string, remove bool) error

    // ActiveDownloads returns all provider-managed active download IDs.
    ActiveDownloads() []string

    // MaxConcurrent returns the per-provider concurrency limit.
    // Return 0 for unlimited.
    MaxConcurrent() int

    // DownloadTimeout returns the per-provider timeout duration.
    DownloadTimeout() time.Duration
}

// Optional: live-progress search
type SearchPlugin interface {
    Plugin
    SearchWithProgress(ctx, query, callback) (tracks, albums, error)
}
```

**Current plugins:** Soulseek (slskd REST API), Deezer (ARL + Blowfish decrypt) — both implement `Plugin` + `MonitoredProvider`. Soulseek returns `MonitoredProvider` via `Connected()`. Deezer additionally implements `SearchPlugin` and `metadata.Provider`.

### Adding a Plugin

1. Implement both `download.Plugin` and `download.MonitoredProvider` in a new package under `internal/download/<source>/`
2. Register in `cmd/groovearr/main.go`: `registry.Register(myPlugin)`
3. That's it — search goes through `Plugin`, downloads go through `MonitoredProvider`

### Playlist Source Interface

```go
type Source interface {
    Name() string
    IsConfigured() bool
    Browse(ctx, page, limit) ([]Playlist, error)
    GetPlaylist(ctx, sourcePlaylistID) (*Playlist, []PlaylistTrack, error)
}
```

**Current source:** Deezer (ARL).

|## Metadata Enrichment Pipeline

### Queue-Time Resolution

Before a download is queued, `MetadataResolver.EnrichMetadata()` queries configured
providers in priority order (`metadata_order` config):

```
EnrichMetadata(artist, title, album, year)
  │
  ├─ Phase 1: Album lookup (if album empty)
  │   └─ For each provider in order:
  │       ├─ SearchAlbum(full artist, title) → found? Done.
  │       └─ SearchAlbum(primary artist, title) → comma fallback
  │
  ├─ Phase 2: Cover art lookup (if album found)
  │   └─ For each provider in order:
  │       ├─ SearchCover(full artist, album) → found? Done.
  │       └─ SearchCover(primary artist, album) → comma fallback
  │
  └─ Returns TrackMetadata{Album, CoverURL}
```

### Post-Import Enrichment

After the library album/track exists, `MetadataEnrichmentHandler` (step 6) runs:

```
For each configured provider:
  ├─ Album title resolution (if album.Title empty)
  │   └─ SearchAlbum(artist, track title) → update album title
  ├─ Cover art download
  │   ├─ SearchCover(artist, album) → download cover.jpg
  │   └─ SearchCoverByMBID(mbid) → CAA lookup
  ├─ ThumbURL sync (if cover.jpg exists on disk)
  │   └─ Set album.thumb_url = "cover.jpg"
  ├─ Track enrichment
  │   └─ EnrichTrack → ISRC, genres, release date, label, external IDs
  └─ Re-tag file with updated metadata
```

### Provider Order

Configurable via `metadata_order` in config.json:
```json
{"metadata_order": ["deezer", "musicbrainz", "coverartarchive"]}
```

Deezer first (fast, 50 req/5s, better album resolution). Falls through to MusicBrainz (1 req/s, deep catalog). CoverArtArchive last (MBID-based only). Unlisted providers go to end.

## Dependency Injection

All components are wired explicitly in `main()`. No framework, no global state. Each component
receives its dependencies through constructor functions:

```go
// Pattern: explicit DI
store := sqlite.New(dbPath)
scanner := library.NewScanner(store)
eventBus := events.NewInMemoryEventBus()
downloadSvc := download.NewDownloadService(dlStore, eventBus, logger)
monitor := download.NewMonitoringService(dlStore, registry, eventBus, logger)
monitor.Start(ctx)
srv := api.NewServer(addr, cfg, registry, downloadSvc, store, scanner, playlistSvc, eventBus, sseHub)
```

## Concurrency Model

- **Monitoring ticker**: single goroutine poll loop at 1-second intervals. Scans DB for queued downloads, polls provider status, drives state transitions. Per-plugin concurrency gates via buffered channel semaphores.
- **Event bus**: handlers run in separate goroutines per event, with panic recovery.
- **Config**: `sync.RWMutex` for thread-safe reads/writes.
- **SSE Hub**: `sync.RWMutex`-protected client registry, non-blocking broadcast with overflow drop.
- **Playlist sync**: per-playlist `sync.Mutex` prevents concurrent syncs of the same playlist.

## Config & Path Model

Two directories, two purposes:

```
./downloads/                              ← staging (all plugins write here)
./downloads/Daft Punk - Get Lucky.flac    ← raw download
         ↓ renamer (post-download hook)
./music/Daft Punk/RAM (2013)/07 - Get Lucky.flac  ← final organized
         ↓ scanner
SQLite library record
```

| Config Key | Default | Purpose | Scanned? |
|------------|---------|---------|----------|
| `library.download_path` | `./downloads` | Staging for raw downloads | No |
| `library.library_path` | `./music` | Organized final library | Yes |
| `library.folder_template` | `{artist}/{album} ({year})/{track:02d} - {title}` | Rename pattern | — |
| `library.playlist_path` | `./playlists` | Separate playlist downloads | No |
| `library.playlist_template` | `{position:02d} {artist} - {title}` | Playlist filename pattern | — |
| `metadata_order` | `["deezer", "musicbrainz", "coverartarchive"]` | Metadata provider priority | — |

## Database

SQLite via `modernc.org/sqlite` (pure Go, no CGo). WAL mode, foreign keys ON.
Idempotent `CREATE TABLE IF NOT EXISTS` (no migration versioning).

Tables: `artists`, `albums`, `tracks`, `playlists`, `playlist_tracks`, `downloads`, `download_events`.

## Technology Stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Language | Go 1.26 | Single binary, no runtime |
| HTTP | stdlib `net/http` | Go 1.22+ pattern routing, zero deps |
| Database | `modernc.org/sqlite` | Embedded, CGo-free |
| Frontend | Vanilla JS + Vite | Embedded via `go:embed`, no framework |
| Audio tags | `dhowden/tag` (read), `bogem/id3v2` (write), `go-flac` | Standard libraries |
| Crypto | `x/crypto/blowfish` | Deezer stream decryption |
| SSE | Custom hub (`internal/sse`) | Fan-out with overflow protection |
| Events | Custom bus (`internal/events`) | In-memory pub/sub with panic recovery |
