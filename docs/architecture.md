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
  │                                                     │
  ├─ library.sqlite.Store    download.DownloadService    api.Server
  │  SQLite (artists,        │ queue/cancel/retry        │ HTTP :8008
  │  albums, tracks,         │                           │ embedded SPA
  │  playlists)              download.WorkerPool         │ 26 endpoints
  │                          │ bounded goroutines        │ SSE stream
  ├─ library.Scanner         │ per-job contexts          │
  │  filesystem → SQLite     │ state machine             │
  │                                                     │
  ├─ library.Renamer         download.Orchestrator       playlist.Service
  │  folder template         │ route search/download     │ import/sync
  │  {artist}/{album}...     │ cross-source fallback     │ playlist.Registry
  │                                                     │ deezer.PlaylistSource
  └─ matching.Engine                                    │
     track matching                                     │
     version awareness
```

## Package Map

| Package | Purpose | Key Types |
|---------|---------|-----------|
| `cmd/groovearr` | Entry point, wiring, graceful shutdown | `main()` |
| `internal/api` | HTTP server + 26 REST handlers + SSE endpoint | `Server`, handlers |
| `internal/config` | JSON config load/validate/persist (thread-safe) | `Config`, `Persistence` |
| `internal/domain` | Core domain types (no behavior, plain structs) | `Track`, `Album`, `Artist`, `Playlist`, `PlaylistTrack`, `DownloadRecord`, `DownloadState`, `SearchResult`, `TrackResult`, `AlbumResult` |
| `internal/download` | Plugin contract, registry, worker pool, import pipeline | `Plugin`, `Registry`, `WorkerPool`, `DownloadService`, `Orchestrator`, import handlers |
| `internal/download/deezer` | Deezer ARL download client | `DownloadClient` |
| `internal/download/soulseek` | slskd REST API client | `Client` |
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
                         │ WorkerPool.Submit()
                    ┌────▼──────┐
               ┌────│downloading│  Plugin.Download() + poll progress
               │    └────┬──────┘
               │         │ Plugin.GetDownloadStatus() → imported
               │    ┌────▼──────────┐
               │    │ importPending │  Worker fires TopicDownloadCompleted
               │    └────┬──────────┘
               │         │ CompletedDownloadService picks up
               │    ┌────▼────┐
               │    │importing│  Atomic CAS (TransitionState)
               │    └────┬────┘
               │         │ Import handler chain (sequential):
               │         │  1. FileRenamer → moves to library path
               │         │  2. CoverArt → downloads cover.jpg
               │         │  3. TagWriter → ID3/FLAC tags
               │         │  4. LibraryImporter → artist→album→track in SQLite
               │         │  5. PlaylistLinker → updates playlist_tracks
               │         │  6. SSENotifier → broadcasts completion
               │    ┌────▼────┐         ┌────────┐
               └───►│ failed  │         │imported│
                    └────┬────┘         └────────┘
                         │ Retry
                         └──→ queued
```

### Event Flow

```
DownloadService.Queue()
  └─ Publish(TopicDownloadQueued, record)

WorkerPool.processJob()
  ├─ Publish(TopicDownloadStateChanged, record)  // queued→downloading
  ├─ Publish(TopicDownloadProgress, record)       // per polling tick
  └─ Publish(TopicDownloadCompleted, record)      // download done

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
| `TopicDownloadStateChanged` | `download:stateChanged` | `*DownloadRecord` | Worker state transitions |
| `TopicDownloadProgress` | `download:progress` | `*DownloadRecord` | Worker progress poll |
| `TopicDownloadCompleted` | `download:completed` | `*DownloadRecord` | Worker on download done |
| `TopicDownloadFailed` | `download:failed` | `*DownloadRecord` | Worker on download failure |
| `TopicImportStarted` | `import:started` | `*DownloadRecord` | CompletedDownloadService start |
| `TopicImportCompleted` | `import:completed` | `*DownloadRecord` | Import handler chain success |
| `TopicImportFailed` | `import:failed` | `*DownloadRecord` | Import handler chain failure |

## Plugin System

### Download Plugin Interface

```go
type Plugin interface {
    Name() string
    DisplayName() string
    IsConfigured() bool
    CheckConnection(ctx context.Context) error
    Search(ctx context.Context, query string) ([]TrackResult, []AlbumResult, error)
    Download(ctx context.Context, username, filename string, fileSize int64) (string, error)
    GetDownloads(ctx context.Context) ([]DownloadRecord, error)
    GetDownloadStatus(ctx context.Context, downloadID string) (*DownloadRecord, error)
    CancelDownload(ctx context.Context, downloadID string, remove bool) error
    ClearCompleted(ctx context.Context) error
    Connected() bool
}

// Optional: live-progress search
type SearchPlugin interface {
    Plugin
    SearchWithProgress(ctx, query, callback) (tracks, albums, error)
}

// Optional: byte-level download progress
type DownloadProgressor interface {
    Plugin
    GetProgress(ctx, downloadID) (*Progress, error)
}
```

**Current plugins:** Soulseek (slskd REST API), Deezer (ARL + Blowfish decrypt).

### Adding a Plugin

1. Implement `download.Plugin` in a new package under `internal/download/<source>/`
2. Register in `cmd/groovearr/main.go`: `registry.Register(myPlugin)`
3. That's it — search, download, cancel all work through the registry

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

## Dependency Injection

All components are wired explicitly in `main()`. No framework, no global state. Each component
receives its dependencies through constructor functions:

```go
// Pattern: explicit DI
store := sqlite.New(dbPath)
scanner := library.NewScanner(store)
eventBus := events.NewInMemoryEventBus()
downloadSvc := download.NewDownloadService(dlStore, eventBus)
workerPool := download.NewWorkerPool(0, registry, dlStore, eventBus)
downloadSvc.SetWorkerPool(workerPool)
srv := api.NewServer(addr, cfg, registry, downloadSvc, store, scanner, playlistSvc, eventBus, sseHub)
```

## Concurrency Model

- **Worker pool**: bounded goroutines (default 3). Each job gets a cancellable `context`.
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
