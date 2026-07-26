# Groovearr — Development Guide

## Prerequisites

- Go 1.26+
- Node.js 18+ (UI build only)
- [staticcheck](https://staticcheck.io/) (optional, for linting)

## Build System

```
make              # vet → test → build (default)
make build        # build UI + Go binary → ./build/groovearr
make rebuild      # force full rebuild (picks up go:embed changes)
make test         # run all tests
make test-race    # run tests with race detector
make cover        # run tests with coverage report
make vet          # go vet
make lint         # vet + staticcheck
make run          # build and run
make dev          # Vite dev server + Go backend (hot reload)
make clean        # remove build artifacts
```

### UI Build

The frontend is a vanilla JS SPA built with Vite. The build output (`ui/dist/`) is
embedded into the Go binary via `go:embed`. The embed directive is in `embed.go` at
the project root:

```go
//go:embed ui/dist
var UIFiles embed.FS
```

If you skip the UI build, the binary won't compile — `api.Server` fatally exits if
the embedded filesystem is empty.

## Project Conventions

### Dependency Injection

All components receive their dependencies through constructor functions — explicit,
testable, no global state:

```go
// Pattern: constructor receives everything it needs
func NewDownloadService(store DownloadStore, bus events.IEventAggregator) *DownloadService {
    return &DownloadService{store: store, bus: bus}
}

// Wiring happens in one place (main.go)
downloadSvc := download.NewDownloadService(dlStore, eventBus)
```

### Optional dependencies set after construction:

```go
// Some dependencies are set later (circular dependency avoidance)
downloadSvc.SetRegistry(registry)
```

### Interfaces over concrete types

Every store and service has an interface defined in its package:

| Interface | Defined in | Implemented in |
|-----------|-----------|----------------|
| `library.Store` | `internal/library/store.go` | `internal/library/sqlite/store.go` |
| `download.DownloadStore` | `internal/download/store.go` | `internal/download/sqlite/store.go` |
| `download.Plugin` | `internal/download/plugin.go` | `internal/download/soulseek/`, `internal/download/deezer/` |
| `download.MonitoredProvider` | `internal/download/provider.go` | `internal/download/soulseek/`, `internal/download/deezer/` |
| `events.IEventAggregator` | `internal/events/bus.go` | `InMemoryEventBus` |
| `playlist.Source` | `internal/playlist/source.go` | `internal/playlist/deezer/` |

### Struct field alignment

No special alignment — standard Go conventions.

### Error handling

- Functions return `(result, error)` — never panic except in `main()` for fatal startup
- Errors are wrapped with `fmt.Errorf("context: %w", err)` for traceability
- Store operations that "not found" return `nil, nil` (not an error)
- SSE and event bus handlers recover panics and log them

### Context propagation

All I/O operations accept `context.Context` as the first parameter. The monitoring
service creates per-download contexts with per-provider timeouts (`DownloadTimeout()`)
so stalled downloads are detected without affecting healthy downloads.

### Concurrency

| Component | Pattern | Rationale |
|-----------|---------|-----------|
| `config.Persistence` | `sync.RWMutex` | Many readers, rare writers |
| `event.InMemoryEventBus` | `sync.RWMutex` + goroutine per handler | Non-blocking publish |
| `MonitoringService` | Ticker (1s) + provider-owned goroutines | Polls provider status, drives download state machine |
| `sse.SSEHub` | `sync.RWMutex` + buffered channels | Fan-out, overflow protection |
| `playlist.Service` | Per-playlist `sync.Mutex` | No concurrent syncs of same playlist |

### Logging

Uses stdlib `log` package. No structured logging framework. Log levels by convention:

```go
log.Printf("worker: download %s FAILED: %s", id, err)    // error
log.Printf("playlist: background sync %d failed: %v", id, err)  // warning
log.Printf("groovearr listening on %s", addr)             // info
```

## Testing

### Test file location

Tests live alongside the code in `*_test.go` files. Package-level tests use
the same package name (white-box). No separate `_test` package convention.

### Mock objects

Mocks are defined inline in test files, typically at the top. They implement
the interface being tested:

```go
// service_test.go
type mockStore struct {
    mu      sync.Mutex
    records map[string]*domain.DownloadRecord
}

var _ DownloadStore = (*mockStore)(nil)  // compile-time interface check
```

### Test helpers

Common test helpers:

```go
// waitFor polls a condition with timeout (used in worker tests)
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if fn() { return }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatal("timeout waiting for condition")
}
```

### Running tests

```bash
make test          # all tests, no cache
make test-race     # with race detector
make test-verbose  # verbose output
make cover         # with coverage report
```

## Adding a Download Source Plugin

1. Create a package under `internal/download/<source>/`

2. Implement the `download.Plugin` interface for **registration and search** only:

```go
type MyPlugin struct {
    // source-specific fields
}

var _ download.Plugin = (*MyPlugin)(nil)  // compile-time interface check

func (p *MyPlugin) Name() string                                   { return "mysource" }
func (p *MyPlugin) DisplayName() string                            { return "My Source" }
func (p *MyPlugin) IsConfigured() bool                             { return true }
func (p *MyPlugin) CheckConnection(ctx context.Context) error      { ... }
func (p *MyPlugin) Search(ctx context.Context, query string) ([]TrackResult, []AlbumResult, error) { ... }
func (p *MyPlugin) Connected() bool                                { return p.configured }
```

3. **Optional**: implement `download.SearchPlugin` for incremental search results:

```go
func (p *MyPlugin) SearchWithProgress(ctx context.Context, query string, cb func(...)) (...) { ... }
```

4. Implement `download.MonitoredProvider` for all download lifecycle methods:

```go
var _ download.MonitoredProvider = (*MyPlugin)(nil)  // compile-time check

// StartDownload initiates a non-blocking download and returns a provider-managed
// download ID immediately. Callers (MonitoringService) do not block on this call.
func (p *MyPlugin) StartDownload(ctx context.Context, meta download.DownloadMeta) (string, error) { ... }

// GetStatus returns the current state of a tracked download.
// Called by MonitoringService every 1s.
func (p *MyPlugin) GetStatus(ctx context.Context, providerID string) (*domain.DownloadRecord, error) { ... }

// GetProgress returns live byte-level transfer progress.
// Return nil, nil if the provider cannot report progress.
func (p *MyPlugin) GetProgress(ctx context.Context, providerID string) (*download.Progress, error) { ... }

// Cancel cancels an active download. If remove is true, also drop internal tracking.
func (p *MyPlugin) Cancel(ctx context.Context, providerID string, remove bool) error { ... }

// ActiveDownloads returns provider-managed IDs of all currently tracked downloads.
func (p *MyPlugin) ActiveDownloads() []string { ... }

// MaxConcurrent returns the maximum concurrent downloads for this provider.
// Return 0 for unlimited.
func (p *MyPlugin) MaxConcurrent() int { ... }

// DownloadTimeout returns the per-provider timeout. Downloads exceeding this
// duration are considered stalled by the monitoring service.
func (p *MyPlugin) DownloadTimeout() time.Duration { ... }
```

5. Register in `cmd/groovearr/main.go`:

```go
myPlugin := mysource.New(cfg.MySource, cfg.Library.DownloadPath)
if err := registry.Register(myPlugin); err != nil {
    log.Fatalf("register mysource: %v", err)
}
```

6. For config hot-reload support, add a `reload*` method to `api.Server`:

```go
func (s *Server) reloadMySource() {
    cfg := s.cfg.Get()
    p := mysource.New(cfg.MySource, cfg.Library.DownloadPath)
    if err := s.registry.Replace("mysource", p); err != nil {
        log.Printf("reload mysource: %v", err)
    }
}
```

## Adding a Playlist Source Plugin

1. Create a package under `internal/playlist/<source>/`

2. Implement `playlist.Source`:

```go
type Source interface {
    Name() string
    DisplayName() string
    IsConfigured() bool
    Browse(ctx context.Context, page, limit int) ([]SourcePlaylistItem, error)
    GetPlaylist(ctx context.Context, sourcePlaylistID string) (*domain.Playlist, []domain.PlaylistTrack, error)
}
```

3. Register in `main.go` via `playlistReg.Register(mySource)`.

## Database Schema

SQLite via `modernc.org/sqlite`. No migration framework — schema is created by
`CREATE TABLE IF NOT EXISTS` in `internal/library/sqlite/store.go`. When adding
tables or columns:

1. Add the `CREATE TABLE IF NOT EXISTS` statement to `initSchema()`
2. For new columns on existing tables, use `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`
3. Test against an existing database (schema changes are additive only)

Tables: `artists`, `albums`, `tracks`, `playlists`, `playlist_tracks`, `downloads`, `download_events`.

## File Organization

```
internal/
  domain/          Core types (no behavior). One file per concept.
  <package>/       Feature package. Contains:
    store.go       Interface definition
    service.go     Business logic
    *_test.go      Tests
    sqlite/        SQLite implementation (if it has a store)
      store.go
      store_test.go
    <source>/      Plugin implementation (if it's a plugin registry)
```

### File size convention

- Keep files under 500 lines
- Split large handlers files by domain (config handlers, download handlers, library handlers, playlist handlers)
- Extract helpers into separate files when they grow beyond 50 lines

## Known Architectural Patterns

### Atomic state transitions

Download states use `TransitionState(old, new)` — a SQL `UPDATE ... WHERE state=?`
that fails if another goroutine changed the state. Used for:
- `queued → downloading` (monitoring service picks up job)
- `importPending → importing` (import handler starts)
- `* → failed` in failRecord (prevents overwriting `ignored`)

### Import handler chain

Post-download processing runs through a sequential chain of `ImportHandler` implementations.
Each handler does one thing. The chain stops on first error (fail-fast).

### Event-driven decoupling

The download pipeline communicates entirely through the event bus. The monitoring
service doesn't know about SSE, the import service doesn't know about the monitoring
service. New subscribers can be added without modifying existing code.

### Monitoring polling pattern

The `MonitoringService` runs a single ticker-based goroutine (1s interval) that
orchestrates all download state transitions:

- Scans the store for queued records and calls `StartDownload` on providers
- Polls active downloads via `GetStatus` + `GetProgress` every tick
- Syncs with providers via `ActiveDownloads` to detect externally-started downloads
- Detects cancellations by re-reading store state each tick
- Runs periodic retry scanning for failed downloads with exponential backoff

Orphan recovery on startup: `downloading` records are marked failed, `importPending`
records re-trigger the import chain, `importing` records are marked failed.

### Plugin registry pattern

Both `download.Registry` and `playlist.Registry` follow the same pattern:
- Name-based lookup
- Alias support
- Insertion-order preservation
- Hot-swap via `Replace()` for config reload
