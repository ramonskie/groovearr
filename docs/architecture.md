# Groovearr — Architecture

> Go rewrite of SoulSync. Self-hosted music download manager.
> Go 1.26, stdlib `net/http`, `modernc.org/sqlite` (CGo-free), vanilla JS SPA embedded via `go:embed`.

## Flow Charts

Code-verified mermaid flow charts for the core components. These are the
source of truth for component behavior (see `docs/flows/README.md` for known
drift fixed during verification):

- [Download State Machine](flows/download-state-machine.md)
- [Monitoring Service](flows/monitoring-service.md)
- [Import Handler Chain](flows/import-handler-chain.md)
- [Album Import Handler](flows/album-import-handler.md)
- [Metadata Enrichment](flows/metadata-enrichment.md)
- [Queue-Time Resolution](flows/queue-time-resolution.md)

## High-Level Component Diagram

```
cmd/groovearr/main.go  ─── entry point, wires all components via dependency injection

  config.Persistence        download.Registry          events.InMemoryEventBus
  │ JSON config              │ plugin lookup             │ pub/sub decoupler
  │ hot-reload               │                           │
  │                          ├─ soulseek.Client          ├─ CompletedDownloadService
  │                          ├─ deezer.DownloadClient    ├─ SSENotifier
  │                          ├─ prowlarr.Plugin          │
  │                          └─ qbittorrent.Plugin       │
  │                          │ (DownloadClientRegistry)  │
  │                          │                           │
  │                          │ download.MonitoringService│
  │                          │ poll loop (1s ticker)     │
  │                          │ state machine driver      │
  │                          │ orphan recovery           │
  │                          │ Provider: MonitoredProv…  │
  │                          │ Album: DownloadClient     │
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

   metadata.Registry          metadata.MetadataResolver    metadata.ProviderCooldown
   │ provider lookup          │ queue-time enrichment      │ shared app-wide, keyed
   │ configurable order       │ primaryArtist fallback     │ by provider name
   ├─ musicbrainz.Client      │ album + cover lookup       │ 429/503 → ErrRateLimited
   ├─ deezer.DownloadClient   │                            │ MarkAfter honors Retry-After
   └─ coverartarchive.Client  │                            │ (2–10 min window)
```

## Package Map

| Package | Purpose | Key Types |
|---------|---------|-----------|
| `cmd/groovearr` | Entry point, wiring, graceful shutdown | `main()` |
| `internal/api` | HTTP server + 26 REST handlers + SSE endpoint | `Server`, handlers |
| `internal/config` | JSON config load/validate/persist (thread-safe) | `Config`, `Persistence` |
| `internal/domain` | Core domain types (no behavior, plain structs) | `Track`, `Album`, `Artist`, `Playlist`, `PlaylistTrack`, `DownloadRecord`, `DownloadState`, `SearchResult`, `TrackResult`, `AlbumResult` |
| `internal/download` | Download lifecycle, album provider contract, import pipeline, monitoring | `Record`, `MonitoredProvider`, `DownloadClient`, `AlbumProvider`, `AlbumImportHandler`, import handler chain, `CompletedDownloadService`, `MonitoringService` |
| `internal/metadata` | Metadata provider interface + resolver + registry + app-wide rate-limit cooldown | `Provider`, `MetadataResolver`, `Registry`, `ProviderCooldown`, `RateLimitError`, `CoverResult`, `TrackMetadata` |
| `internal/providers/musicbrainz` | MusicBrainz metadata provider (recording search, release lookup, release-group batch search) | `Client` (implements `metadata.Provider`) |
| `internal/providers/coverartarchive` | Cover Art Archive (MBID-based cover lookup) | `Client` (implements `metadata.Provider`) |
| `internal/providers/deezer` | Deezer plugin — download (ARL) + metadata (public API) | `DownloadClient` (implements `download.Plugin` + `metadata.Provider`), `Client` (API) |
| `internal/providers/prowlarr` | Prowlarr album provider — searches RuTracker via Torznab, resolves tracks via MusicBrainz | `Plugin` (implements `download.AlbumProvider`) |
| `internal/providers/qbittorrent` | qBittorrent download client — torrent downloads via WebUI API v2 | `Plugin` (implements `download.DownloadClient`) |
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

Lifecycle states (see [Download State Machine](flows/download-state-machine.md)):

```
DownloadRecord
  id                    Terminal states: imported, failed, ignored
  source_name           Retryable: failed, failedPending
  filename
  state                 Album-specific fields:
  progress (0-100)       album_tracks (ExpectedTrack[])
  size/transferred       album_type ("Album" | "Compilation" | ...)
  speed                  download_client (e.g. "qbittorrent")
  file_path              imported_track_ids ([]int64)
  error                  folder_path (raw download directory)
  metadata              magnet_uri (torrent download URL — legacy field name)
  playlist_id           provider_id (plugin-managed download ID, e.g. qBittorrent hash)
                         album_mbid (MusicBrainz release MBID, synced to library)

                        DownloadEvent
                          id, download_id
                          type, payload, timestamp
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

The full lifecycle is documented as mermaid flow charts — see the
[Download State Machine](flows/download-state-machine.md) (8 states, all
transitions) and the [Monitoring Service](flows/monitoring-service.md)
(1s poll loop, dispatch, retry, pending-source resolution, orphan sync).

Two pipes feed the state machine:

- **Track pipe**: individual tracks queued via `DownloadService.Queue()`. Any plugin
  implementing `MonitoredProvider` (Deezer, Soulseek, future Tidal) handles the
  download lifecycle via `StartDownload()`. One Record per file enters the import
  chain directly.
- **Album pipe**: when `album_sources` is configured, `Orchestrator.SearchAlbums()`
  queries Prowlarr's Torznab API (backed by RuTracker indexers), picks the best
  release by seeders, and queues via `QueueAlbum()`. The monitor dispatches to the
  configured `DownloadClient` (qBittorrent) via `AddDownload()`. When the torrent
  completes, the [Album Import Handler](flows/album-import-handler.md) scans the
  folder, resolves tracks **post-download** (using the actual file count), matches
  files to tracks, and feeds synthetic per-track Records through the
  [shared import chain](flows/import-handler-chain.md).
- **Retry**: `MonitoringService.scanRetry()` re-queues failed + `failedPending`
  records with exponential backoff `2→4→8→16→32→60 min` (±20% jitter), up to
  `MaxRetries=5`. Before re-queueing it searches all providers for an alternative
  source (`Orchestrator.FindBestMatch`).

### Event Flow

See [Monitoring Service](flows/monitoring-service.md) for the download lifecycle
events (queue → state change → progress → completed/failed) and
[Import Handler Chain](flows/import-handler-chain.md) for the import events
(started → completed/failed).

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

**Current plugins:** Soulseek (slskd REST API), Deezer (ARL + Blowfish decrypt) — both implement `Plugin` + `MonitoredProvider`. Prowlarr implements `download.AlbumProvider` for full-album torrent search and MusicBrainz track resolution. qBittorrent implements `download.DownloadClient` for torrent execution.

### Album Provider Interface

```go
// AlbumProvider: full-album search (Prowlarr via Torznab)
type AlbumProvider interface {
    plugin.BasePlugin

    SearchAlbum(ctx context.Context, query string) ([]domain.AlbumRelease, error)
    ResolveTracksForCount(ctx context.Context, release domain.AlbumRelease, fileCount int, torrentTitle string) ([]domain.ExpectedTrack, string, error)
}
```

Track resolution moved from queue-time to post-download: after the torrent completes,
`AlbumImportHandler` counts files on disk, then calls `ResolveTracksForCount` with the
file count and torrent title. This avoids a separate MusicBrainz API call at queue-time
and allows accurate matching based on the actual file count.

Inside `ResolveTracksForCount`:
1. `MusicBrainz.SearchReleasesByGroup(rgid)` — one API call returns ALL releases in the
   release group with `track_count` via the search endpoint (`/release?query=rgid:{mbid}`)
2. `pickBestMatchingRelease(allReleases, fileCount, torrentTitle)` — picks the release
   whose track count is closest to the file count. Word-overlap with the torrent title
   serves as a tiebreaker (e.g. "Ten Redux" vs "Ten" vs "Ten (Deluxe Edition)").

### TrackResolver

Wired in `main.go` as a function type, bridging `AlbumImportHandler` to Prowlarr's
`ResolveTracksForCount`:

```go
type TrackResolver func(ctx context.Context, sourceName, artist, album string, fileCount int, torrentTitle string) (tracks []domain.ExpectedTrack, mbid string, err error)
```

### Download Client Interface

```go
// DownloadClient: executes torrent downloads (qBittorrent via .torrent URL)
type DownloadClient interface {
    plugin.BasePlugin

    AddDownload(ctx context.Context, uri, category, savepath string) (providerID string, err error)
    GetStatus(ctx context.Context, providerID string) (*Record, error)
    GetProgress(ctx context.Context, providerID string) (*Progress, error)
    Cancel(ctx context.Context, providerID string, remove bool) error
    MaxConcurrent() int
    DownloadTimeout() time.Duration
}
```

Album downloads flow: `Orchestrator.SearchAlbums()` → user picks release → `DownloadService.QueueAlbum()` → `MonitoringService` dispatches to `DownloadClient.AddDownload(uri, "music", savePath)` → client downloads torrent → files land in staging → `AlbumImportHandler` scans folder, matches files → feeds per-track Records through standard import chain.

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
providers in priority order (`metadata_order` config). It resolves a missing album
name, then finds cover art — see the
[Queue-Time Resolution](flows/queue-time-resolution.md) flow chart. It is
**best-effort**: errors are logged, empty fields are left empty, and the queue
pipeline is never blocked.

### Post-Import Enrichment

After the library album/track exists, `MetadataEnrichmentHandler` (step 5) runs identically
for both single-track and album downloads. Album tracks go through the same handler chain —
`AlbumImportHandler` creates synthetic per-track Records and feeds them into the chain,
so all enrichment applies uniformly. The provider loop (album title → cover art → track
enrichment → artist image → re-tag) is documented in the
[Metadata Enrichment](flows/metadata-enrichment.md) flow chart.

Rate limiting during enrichment (bulk jobs): when a provider returns
`metadata.ErrRateLimited` (429, or 503 for MusicBrainz/Discogs/CAA), the handler marks it
cooling down via the shared `ProviderCooldown` and moves to the next provider — subsequent
tracks in the job skip the throttled provider until the cooldown expires. The per-download
path (single track, `bulk=false`) is intentionally NOT gated: it tries every provider in
order, so a cooldown observed during a bulk job never blocks an individual download. A
bulk job start clears cooldowns (`ResetBulk`) so every provider is re-attempted fresh.

### Album MBID Sync

Album torrents resolve the MusicBrainz release MBID during post-download track resolution.
This MBID is stored on `DownloadRecord.AlbumMBID`. During enrichment (step 5), the handler
syncs it to the library album's `ExternalIDs["musicbrainz_release"]`, enabling accurate
cover art lookups via CoverArtArchive and MusicBrainz release metadata.

### Provider Order

Configurable via `metadata_order` in config.json:
```json
{"metadata_order": ["deezer", "musicbrainz", "coverartarchive"]}
```

Deezer first (fast, 50 req/5s, better album resolution). Falls through to MusicBrainz (1 req/s, deep catalog). CoverArtArchive last (MBID-based only). Unlisted providers go to end.

### Rate Limiting & Provider Cooldown

Every provider client paces itself under its known limit (`minInterval` / token bucket),
but a burst or rolling window can still trip the upstream. When it does, providers surface a
shared sentinel instead of blocking:

- **`metadata.ErrRateLimited`** — sentinel all providers return on 429 (or 503 where that
  is the throttle signal: MusicBrainz, Discogs, Cover Art Archive). Callers match with
  `errors.Is`.
- **`metadata.RateLimitError`** — structured form wrapping the sentinel, carrying the
  server's `Retry-After` when the provider read it. `errors.Is(err, ErrRateLimited)` still
  matches. Providers that send the header (Spotify always, Discogs/Tidal sometimes) get it
  honored; header-less providers (MusicBrainz, Last.fm, Deezer) fall back to the default.

**Shared cooldown.** One `metadata.ProviderCooldown` instance is created in `app.go` and
wired into both the API server and the enrichment handler, so a rate limit observed by any
consumer cools the provider app-wide for a short window instead of hammering it repeatedly:

```
MarkAfter(name, retryAfter):
  d = max(retryAfter, ProviderRateLimitCooldown=2min)   // floor: never re-hit immediately
  d = min(d, maxRateLimitCooldown=10min)                // ceiling: never park a provider
```

A provider is never permanently abandoned — only deactivation (config) removes it from the
rotation; every cooldown expires and re-attempts.

**Consumer behavior:**

| Consumer | On ErrRateLimited |
|---|---|
| Bulk enrichment | mark cooling down → skip provider for subsequent tracks in the job → next provider |
| Per-download enrichment | NOT gated — tries every provider (isolation) |
| Discover search | pre-filters cooling-down providers; all-cooled → HTTP 503 |
| Album discovery | skips cooling-down providers, marks on error, moves on |
| Duplicate scan (MB only) | pause honoring Retry-After (5–10s) → retry group once → empty canonical fallback |
| Download monitor | separate system — DB queue + jittered exponential backoff (2→60 min) |

**Spotify fail-fast.** `handleRateLimit` retries short `Retry-After` values (≤5s) with a
context-aware sleep (job cancellation / per-track deadline aborts the wait immediately). A
long Retry-After fails fast with `ErrRateLimited` rather than blocking a worker for minutes —
this is the fix for the enrichment convoy stall.

### MusicBrainz Release-Group Batch Lookup

`SearchReleasesByGroup(rgMBID)` queries the MusicBrainz search endpoint
(`/release?query=rgid:{mbid}`) and returns ALL releases in a release group with their
`track_count`. Unlike the release-group endpoint (which doesn't include track counts),
the search endpoint provides this field, enabling a single API call to resolve all
release variants:

```go
type ReleaseGroupRelease struct {
    ID         string `json:"id"`
    Title      string `json:"title"`
    TrackCount int    `json:"track_count"`
    Disambiguation string `json:"disambiguation"`
    // ...
}
```

Used by `AlbumImportHandler` post-download to match the torrent's file count against
available MusicBrainz releases, avoiding a separate API call at queue-time.

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

Tables: `artists`, `albums`, `tracks`, `playlists`, `playlist_tracks`, `downloads`, `download_events`, `album_discovery_cache`.

## Album Discovery Cache

The `album_discovery_cache` table stores pre-loaded album data for the UI album-discovery
endpoints. Each row maps an album to a provider's track listing.

```
album_discovery_cache
  album_id          INTEGER  (FK → albums.id)
  provider_name     TEXT     ("library" | "deezer" | future providers)
  provider_album_id INTEGER  (provider-specific album identifier)
  tracks_json       TEXT     (JSON array of {title, track_number, duration_ms})
  cached_at         TEXT     (ISO 8601 timestamp)
```

**Write path**: Two providers currently populate the cache:
- **Deezer** (queue-time): during `Orchestrator.SearchAlbums()`, Deezer album results
  with tracks are inserted under `provider_name = 'deezer'`.
- **Library** (post-import): after `AlbumImportHandler` finishes the import chain,
  `updateDiscoveryCache()` queries the actual library tracks and writes them under
  `provider_name = 'library'`. This overrides the Deezer cache so the UI reflects the
  real downloaded release (e.g. 17 tracks for "Ten Redux" instead of Deezer's 11-track
  standard edition).

The UI queries cache by `album_id`, preferring `'library'` rows over `'deezer'`.

## Cancel Flow

`DownloadService.Cancel(downloadID)` transitions the record to the `ignored`
state **before** cancelling the provider (prevents `checkCancellations` from
issuing a duplicate cancel on the next tick), then cancels at the provider
level for both album and track downloads:

```go
func (s *Service) Cancel(ctx context.Context, id string) error {
    // ...
    if record.State.Terminal() {
        return nil // already terminal — idempotent
    }
    record.State = StateIgnored          // update state FIRST
    if err := s.store.Update(ctx, record); err != nil {
        return err
    }
    s.cancelDownloadClient(ctx, record)  // album: DownloadClient.Cancel
    s.cancelTrackProvider(ctx, record)   // track: MonitoredProvider.Cancel
    s.bus.Publish(ctx, events.TopicDownloadStateChanged, record)
    return nil
}
```

`ProviderID` is the provider's own download identifier (e.g. qBittorrent info hash),
stored on `DownloadRecord` at queue-time and persisted to SQLite. This avoids
re-inventing provider ID resolution or keeping an in-memory mapping. Each `DownloadClient`
accepts its own IDs natively — no translation layer needed.

> **Cancel → `ignored` (not `failed`).** `ignored` is terminal and not retryable —
> a cancelled download is never re-queued by `scanRetry()`. See
> [Download State Machine](flows/download-state-machine.md).

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
