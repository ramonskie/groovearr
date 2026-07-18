# Groovearr — Current Architecture & State

> Go rewrite of SoulSync. Self-hosted music download manager.

## Overview

Groovearr orchestrates music downloads across multiple sources (Soulseek via slskd, Deezer), provides
a single-page web UI for search/download/library management, and stores results in SQLite.

**Stack**: Go 1.26, `net/http` (stdlib router), `modernc.org/sqlite` (CGo-free), vanilla JS frontend (embedded).

**Binary**: single `groovearr` binary with embedded static UI. No external runtime dependencies except slskd daemon (for Soulseek).

---

## Architecture

```
cmd/groovearr/main.go          — entry point, wires all components
    │
    ├── config.Persistence      — JSON config hot-reload (thread-safe)
    │
    ├── download.Registry       — plugin registry (name + alias lookup)
    │   ├── soulseek.Client     — slskd REST API (/api/v0/)
    │   └── deezer.DownloadClient — ARL auth + Blowfish decrypt
    │
    ├── download.Orchestrator   — routes search/download across plugins
    │   └── matching.Engine     — cross-source track matching
    │
    ├── library.Scanner         — filesystem → SQLite importer
    │   └── library.PathResolver — template-based folder naming
    │
    ├── library.sqlite.Store    — SQLite CRUD (artists, albums, tracks)
    │
    └── api.Server              — HTTP server (:8008) + embedded SPA
```

## Package Map

| Package | Files | Purpose |
|---------|-------|---------|
| `cmd/groovearr` | `main.go` | Entry point, wiring, graceful shutdown |
| `internal/api` | `handlers.go`, `static/index.html` | 14 REST endpoints + embedded SPA |
| `internal/config` | `config.go`, `persist.go`, `*_test.go` | Config struct, thread-safe JSON persistence |
| `internal/domain` | `artist.go`, `album.go`, `track.go`, `download.go`, `search.go` | Domain models |
| `internal/download` | `plugin.go`, `registry.go`, `engine.go`, `orchestrator.go` | Plugin contract, registry, central engine, orchestrator |
| `internal/download/deezer` | `api.go`, `download.go` | Deezer metadata API + ARL download client |
| `internal/download/soulseek` | `client.go` | slskd REST client |
| `internal/library` | `store.go`, `scanner.go`, `path_resolver.go`, `*_test.go` | Library interface, scanner, path templating |
| `internal/library/sqlite` | `store.go`, `store_test.go` | SQLite implementation |
| `internal/matching` | `engine.go`, `engine_test.go` | Track matching with version awareness |

## API Endpoints (14 total)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/` | Embedded SPA (no-cache) |
| `GET` | `/api/health` | Health check |
| `GET` | `/api/config` | Get config (API key masked) |
| `PUT` | `/api/config` | Update config, persist, reload plugins |
| `GET` | `/api/config/sources` | List plugins with status badges |
| `POST` | `/api/config/test/{source}` | Test source connection |
| `POST` | `/api/search` | Search tracks + albums |
| `POST` | `/api/download` | Start download |
| `POST` | `/api/download/match` | Cross-source matching download |
| `GET` | `/api/downloads` | List all downloads |
| `DELETE` | `/api/downloads/{id}` | Cancel + remove download |
| `GET` | `/api/library/tracks` | List tracks (limit 200) |
| `GET` | `/api/library/artists` | List artists (limit 200) |
| `GET` | `/api/library/albums` | List albums (limit 200) |
| `POST` | `/api/library/scan` | Scan filesystem into library |

## Plugin System

All download sources implement the `download.Plugin` interface:

```go
type Plugin interface {
    Name() string
    DisplayName() string
    IsConfigured() bool
    CheckConnection(ctx context.Context) error
    Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error)
    Download(ctx context.Context, username, filename string, fileSize int64) (string, error)
    GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error)
    GetDownloadStatus(ctx context.Context, id string) (*domain.DownloadRecord, error)
    CancelDownload(ctx context.Context, id, username string, remove bool) error
    ClearCompleted(ctx context.Context) error
}
```

### Currently Implemented

| Plugin | Search | Download | Notes |
|--------|--------|----------|-------|
| **Soulseek** (slskd) | ✅ Polling with progress | ✅ Direct transfer | Requires slskd daemon running |
| **Deezer** | ✅ Public API + advanced search | ✅ ARL + Blowfish | Quality fallback: flac→mp3_320→mp3_128 |

### Orchestrator Modes

- **Single source**: route to specific plugin
- **Default**: first configured plugin
- **Hybrid**: merge results from all configured plugins
- **Best match**: cross-source fallback via matching engine (confidence ≥0.55)

## Config Structure

```json
{
  "soulseek":  { "slskd_url", "api_key", "download_path", "search_timeout", "min_upload_speed" },
  "deezer":    { "arl", "quality", "allow_fallback", "access_token" },
  "library":   { "library_path", "folder_template" },
  "quality":   { "preferred_format", "min_bitrate" }
}
```

Loaded from `$GROOVEARR_CONFIG` (default `./config.json`). Thread-safe updates auto-persist.

## Database Schema

3 tables: `artists`, `albums`, `tracks` — each with external ID columns (spotify, itunes, deezer, musicbrainz, tidal, qobuz).

WAL mode, foreign keys ON, idempotent `CREATE TABLE IF NOT EXISTS` (no migration versioning).

## Test Coverage

| Package | Tests | Coverage |
|---------|-------|----------|
| `internal/config` | 4 | Config load/persist cycle |
| `internal/download` | 8 | Registry + Orchestrator routing |
| `internal/library` | 12 | Path parsing + scanner integration |
| `internal/library` (resolver) | 10 | Template resolution + sanitization |
| `internal/library/sqlite` | 4 | CRUD operations |
| `internal/matching` | 11 | Normalization + scoring |
| **Total** | **~49** | — |

## Known Gaps / Unused Code

| Item | Status |
|------|--------|
| `download.Engine` (central record tracker) | Defined but **unused** — plugins manage their own records |
| `PathResolver` template-based renaming | Implemented but **not wired** — downloads stay flat in download path |
| `ArtistImage` type | **Defined, never used** |
| `QualityConfig` (min_bitrate, preferred_format) | **Not consulted** by any download logic |
| Deezer OAuth (`access_token` field) | **No OAuth flow** implemented |
| External ID columns (Spotify, iTunes, etc.) | Schema ready but **never populated** |
| Album-art images | ThumbURL fields exist but **no image fetching/display** |
| DB migration versioning | Idempotent CREATE only, **no version tracking** |
