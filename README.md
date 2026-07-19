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

```yaml
services:
  slskd:
    image: slskd/slskd:latest
    volumes:
      - ./downloads:/downloads

  groovearr:
    build: .
    volumes:
      - ./downloads:/downloads
      - ./music:/music
      - ./config.json:/config.json
    environment:
      - GROOVEARR_CONFIG=/config.json
    ports:
      - "8008:8008"
```

Both containers must share the same download path at the same mount point.

## Project Structure

```
cmd/groovearr/          Entry point, dependency injection, graceful shutdown
internal/
  api/                  HTTP server + handlers (stdlib net/http)
  config/               Thread-safe JSON config with hot-reload
  domain/               Core types: Track, Album, Artist, Playlist, DownloadRecord
  download/             Plugin interface, Registry, WorkerPool, import handler chain
    deezer/             Deezer download plugin (ARL auth)
    soulseek/           Soulseek plugin (slskd REST API)
    sqlite/             SQLite store for download records
  events/               In-memory pub/sub event bus
  library/              Library store interface, Scanner, Renamer
    sqlite/             SQLite library store implementation
  matching/             Cross-source track matching engine
  playlist/             Playlist service + registry
    deezer/             Deezer playlist source
  sanitize/             Filename sanitization
  sse/                  Server-Sent Events hub + notifier
  tagging/              Audio metadata tag writing
docs/                   Architecture, API, roadmap
ui/                     Vanilla JS SPA (Vite)
```

## Documentation

- [Architecture](docs/architecture.md) — component diagram, data flows, event system, domain model
- [API Reference](docs/api.md) — 26 REST endpoints with request/response schemas
- [Development Guide](docs/development.md) — build system, code patterns, adding plugins
- [Roadmap](docs/roadmap.md) — feature tiers and planning

## License

MIT
