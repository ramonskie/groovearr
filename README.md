# Groovearr

Self-hosted music download manager. Search, download, and organize music from multiple sources
(Soulseek via slskd, Deezer) through a single web UI.

Single Go binary with embedded frontend — no runtime dependencies except a slskd daemon for Soulseek.

## Features

- **Multi-source search** — Soulseek (slskd) + Deezer, with cross-source matching engine
- **Download pipeline** — queued → downloading → import → library with real-time progress via SSE
- **Plugin architecture** — add new sources by implementing one interface
- **Music library** — SQLite-backed artist/album/track database with filesystem scanner
- **Playlist import** — import Deezer playlists, sync changes, download missing tracks
- **Auto-organization** — configurable folder template (`{artist}/{album} ({year})/{track:02d} - {title}`)
- **Tag writing** — ID3/FLAC metadata written on import
- **Cover art** — fetched and cached per-album during import
- **Single binary** — Go backend + vanilla JS SPA embedded via `go:embed`

## Quick Start

### Prerequisites

- Go 1.26+
- Node.js 18+ (for building the UI)
- [slskd](https://github.com/slskd/slskd) daemon (for Soulseek — optional)

### Build

```bash
# Full build (UI + Go binary)
make build
# Binary at ./build/groovearr
```

### Run

```bash
# From project root
./build/groovearr

# Or with custom config path
GROOVEARR_CONFIG=/path/to/config.json ./build/groovearr
```

Creates `config.json`, `library.db`, `./downloads/`, and `./music/` automatically on first run.
Open `http://localhost:8008` in your browser.

### Development

```bash
# Frontend dev server + Go backend (auto-reload)
make dev

# Run tests
make test

# Lint
make lint
```

## Configuration

See `config.json.example` for all options. Key paths:

| Setting | Default | Purpose |
|---------|---------|---------|
| `library.download_path` | `./downloads` | Staging — raw files from all sources |
| `library.library_path` | `./music` | Library — organized final files |
| `library.folder_template` | `{artist}/{album} ({year})/{track:02d} - {title}` | Directory structure |

## Docker

The repo ships a [docker-compose.yml](docker-compose.yml) that runs the full
stack: Groovearr, slskd, Prowlarr, FlareSolverr, and qBittorrent on shared
volumes for downloads, music, and playlists.

### First run

```bash
cp config.json.example config.docker.json   # container paths (/downloads, /music)
cp .env.example .env                        # Soulseek account credentials
make docker-setup                           # generate slskd.yml (account + Groovearr API key)
docker compose up -d
```

| Service     | URL                   |
|-------------|-----------------------|
| Groovearr   | http://localhost:8008 |
| slskd       | http://localhost:5030 |
| Prowlarr    | http://localhost:9696 |
| qBittorrent | http://localhost:8080 |
| FlareSolverr| http://localhost:8191 |

Open **http://localhost:8008** — on first run Groovearr walks you through a
setup wizard to connect your first download source (Soulseek and/or
Prowlarr + qBittorrent). Full walkthrough in the
[Setup Guide](docs/setup.md).

## Project Structure

```
cmd/groovearr/          Entry point, dependency injection, graceful shutdown
internal/
  api/                  HTTP server + handlers (stdlib net/http)
    downloads/          Download/music-library route handlers
    music/              Library browsing handlers
  config/               Thread-safe JSON config with hot-reload
  domain/               Core types: Track, Album, Artist, Playlist
  download/             Download engine, import handler chain, monitoring
    sqlite/             SQLite store for download records
  discovery/            Search registry + provider discovery
  events/               In-memory pub/sub event bus
  library/              Library store interface, Scanner, Renamer
    sqlite/             SQLite library store implementation
  logger/               Structured logging setup
  matching/             Cross-source track matching engine
  metadata/             Metadata provider order, resolution, enrichment
  plugin/               Plugin/config schema, factory, health
  playlist/             Playlist service + registry
  providers/            Individual source plugins
    soulseek/           Soulseek (slskd REST API)
    deezer/             Deezer (ARL auth)
    tidal/              Tidal
    prowlarr/           Prowlarr indexer search
    qbittorrent/        qBittorrent download client
    spotify/ discogs/ musicbrainz/ coverartarchive/ lastfm/
                        Metadata sources
  quality/              Quality ranking
  ratelimit/            Per-endpoint rate limiting
  sanitize/             Filename sanitization
  sse/                  Server-Sent Events hub + notifier
  tagging/              Audio metadata tag writing
docs/                   Architecture, API, setup, roadmap
ui/                     React SPA (Vite)
```

## Documentation

- [Setup Guide](docs/setup.md) — docker-compose stack, Soulseek + Prowlarr
- [Architecture](docs/architecture.md) — component diagram, data flows, event system, domain model
- [API Reference](docs/api.md) — REST endpoints with request/response schemas
- [Development Guide](docs/development.md) — build system, code patterns, adding plugins
- [Roadmap](docs/roadmap.md) — feature tiers and planning

## License

MIT
