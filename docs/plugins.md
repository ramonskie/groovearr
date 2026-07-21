# Groovearr — Plugin Developer Guide

> How sources integrate with Groovearr via the plugin registry.
> Covers `plugin.BasePlugin`, `download.Plugin`, `plugin.PluginFactory`, optional interfaces,
> capability-based routing, config conventions, and a complete walkthrough for adding a new source.

## Overview

The plugin system lets any music source (Soulseek/slskd, Deezer, Tidal, Qobuz, YouTube,
etc.) integrate with Groovearr's unified search, download, and playlist pipelines.

A plugin is a Go package under `internal/providers/<source>/` that implements
`download.Plugin` and exposes a `plugin.PluginFactory` for self-registration.

**Architecture layers:**

```
internal/plugin/           ← Shared framework: BasePlugin, PluginFactory, Registry
       │
       ├── internal/download/  ← Download domain: download.Plugin extends BasePlugin
       │
       └── internal/providers/ ← All source implementations
               ├── deezer/     (capabilities: download, playlist)
               └── soulseek/   (capabilities: download)
```

**What the registry provides:**
- Name-based lookup + capability-based routing (`WithCapability()`)
- Automatic initialization from `config.json` `"sources"` map via `InitAll()`
- Hot-reload via `Registry.Rebuild()` when config changes
- Plugin queries (all configured sources searched in parallel)

**What a plugin gets for free:**
- Worker pool (bounded goroutines, cancellable contexts, state machine)
- Import pipeline (rename, cover art, tags, library DB, SSE broadcast)
- Event bus integration (progress, completion, failure)
- Config hot-reload without restart
- Sensitive field auto-masking in config dumps

**Example: the full wiring in `main.go`:**

```go
registry := download.NewRegistry()
registry.RegisterFactory(soulseek.Factory)
registry.RegisterFactory(deezer.Factory)
resources := plugin.PluginResources{DownloadPath: cfg.Library.DownloadPath}
err := registry.InitAll(cfg.Sources, resources)
```

That's it. Two lines per source. The framework handles construction, capability routing, and playlist auto-registration.

---

## Plugin Interfaces

### BasePlugin — all plugins

```go
// internal/plugin/plugin.go
type BasePlugin interface {
    Name() string
    DisplayName() string
    IsConfigured() bool
    CheckConnection(ctx context.Context) error
    Connected() bool
}
```

Every plugin — download, metadata, notify — implements this. It covers identity,
readiness, and connectivity. Domain-specific interfaces embed it.

### download.Plugin — download sources

Extends `BasePlugin` with download-specific methods. All current sources implement this.

```go
// internal/download/plugin.go
type Plugin interface {
    plugin.BasePlugin

    Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error)
    Download(ctx context.Context, username, filename string, fileSize int64) (string, error)
    GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error)
    GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error)
    CancelDownload(ctx context.Context, downloadID string, remove bool) error
    ClearCompleted(ctx context.Context) error
}
```

| Method | Purpose |
|--------|---------|
| `Name()` | Canonical source name (e.g. `"soulseek"`, `"deezer"`). Must match the config key. |
| `DisplayName()` | Human-readable label (e.g. `"Soulseek"`, `"Deezer"`). Shown in UI. |
| `IsConfigured()` | Returns `true` if credentials/settings are present and sufficient. |
| `CheckConnection()` | Probes the source API for reachability / authentication. |
| `Search()` | Queries the source for tracks and albums. |
| `Download()` | Enqueues a download. Returns a plugin-specific download ID. |
| `GetDownloads()` | Returns all tracked downloads (active + terminal) for this source. |
| `GetDownloadStatus()` | Returns a single download by its plugin-specific ID. |
| `CancelDownload()` | Cancels an active download. If `remove=true`, drops the record. |
| `ClearCompleted()` | Removes all terminal-state downloads from tracking. |
| `Connected()` | Returns `true` if source has been verified (auth tested, API reachable). |

**Method lifecycle — typical call sequence:**

```
Search() → Download() → GetDownloadStatus() [polled] → CancelDownload() or ClearCompleted()
                ↑
         GetProgress() [optional, polled]
```

**`Download()` parameter conventions:**

| Parameter | Soulseek | Deezer | General |
|-----------|----------|--------|---------|
| `username` | slskd peer name | empty (worker falls back to source name) | Source-dependent identifier |
| `filename` | SMB share path | `"track_id\|\|display_name"` | Source-specific encoding |
| `fileSize` | Bytes from search result | Bytes from API | Pass `0` if unknown |

The download ID returned by `Download()` is opaque to the framework — it's only passed back
to `GetDownloadStatus()`, `GetProgress()`, and `CancelDownload()`.

---

## Optional Interfaces

Plugins can implement these interfaces for extra capabilities. None are required.

### SearchPlugin — incremental search results

Implement when the source can stream results as they arrive (e.g. slskd polls
peer responses over time).

```go
// internal/download/plugin.go
type SearchPlugin interface {
    Plugin

    SearchWithProgress(ctx context.Context, query string,
        cb func(tracks []domain.TrackResult, albums []domain.AlbumResult, responseCount int),
    ) ([]domain.TrackResult, []domain.AlbumResult, error)
}
```

**When to implement:** Source API delivers results incrementally (WebSocket,
polling, paginated streaming). Without this, the UI only sees final results.

**Example (soulseek):** `SearchWithProgress` polls slskd's `/searches/{id}/responses`
every second, invoking `cb` with accumulated results each tick.

### DownloadProgressor — byte-level progress

Implement when the source can report transferred bytes and speed during download.

```go
// internal/download/plugin.go
type DownloadProgressor interface {
    Plugin

    GetProgress(ctx context.Context, downloadID string) (*Progress, error)
}

type Progress struct {
    DownloadID  string
    Transferred int64 // bytes downloaded so far
    Total       int64 // total file size in bytes
    Speed       int64 // bytes per second
}
```

**When to implement:** Source provides per-transfer progress data. Gives smoother
UI progress bars and speed displays.

### PlaylistSourceProvider — playlist import

Implement when the source provides playlist browsing (e.g. Deezer user playlists).
Defined in `internal/playlist` to avoid import cycles.

```go
// internal/playlist/pluginsource_provider.go
type PlaylistSourceProvider interface {
    download.Plugin

    PlaylistSource() Source
}
```

The returned `playlist.Source` must implement:

```go
// internal/playlist/source.go
type Source interface {
    Name() string
    DisplayName() string
    IsConfigured() bool
    GetUserPlaylists(ctx context.Context) ([]PlaylistInfo, error)
    GetPlaylistTracks(ctx context.Context, sourcePlaylistID string) ([]TrackInfo, string, error)
}
```

**Auto-registration in main.go — already wired:**

```go
for _, p := range registry.All() {
    if psp, ok := p.(playlist.PlaylistSourceProvider); ok {
        if p.IsConfigured() {
            playlistReg.Register(psp.PlaylistSource())
        }
    }
}
```

---

## PluginFactory — Self-Registration

Each plugin package exports a `Factory` variable implementing `plugin.PluginFactory`.
The registry calls `InitAll()` or `Rebuild()` to construct plugins from raw config.

```go
// internal/plugin/factory.go
type PluginResources struct {
    DownloadPath string
}

type PluginFactory interface {
    Name() string
    DisplayName() string
    Capabilities() []string
    Create(rawCfg json.RawMessage, resources PluginResources) (BasePlugin, error)
    ValidateConfig(rawCfg json.RawMessage) error
    DefaultConfig() json.RawMessage
}
```

| Method | Purpose |
|--------|---------|
| `Name()` | Canonical name, must match `config.json` key. |
| `DisplayName()` | Human-readable label for UI. |
| `Capabilities()` | List of domain capabilities: `["download"]`, `["download","playlist"]`, `["metadata"]`. Used for capability-based routing. |
| `Create()` | Builds a `BasePlugin` from raw config + runtime resources. |
| `ValidateConfig()` | Checks structural validity of raw config. |
| `DefaultConfig()` | Returns default config as JSON. Used for config file generation. |

**Pattern — every factory follows this structure:**

```go
// internal/providers/soulseek/factory.go
package soulseek

import (
    "encoding/json"
    "github.com/ramonskie/groovearr/internal/plugin"
)

// Exported variable implements plugin.PluginFactory.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string              { return "soulseek" }
func (f *factory) DisplayName() string       { return "Soulseek" }
func (f *factory) Capabilities() []string    { return []string{"download"} }

func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
    var cfg SoulseekConfig
    if err := json.Unmarshal(rawCfg, &cfg); err != nil {
        return nil, err
    }
    return New(cfg, resources.DownloadPath)
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
    var cfg SoulseekConfig
    if err := json.Unmarshal(rawCfg, &cfg); err != nil {
        return err
    }
    return nil
}

func (f *factory) DefaultConfig() json.RawMessage {
    return json.RawMessage(`{"slskd_url":"","api_key":"","search_timeout":60,"min_upload_speed":0}`)
}
```

**Key constraints:**
- `Name()` return must match the key in `config.json` `"sources"` map.
- `Capabilities()` determines which domains this plugin participates in.
- `Create()` receives `PluginResources` — extends easily for future resources (e.g. database handle, logger).

**Registration in main.go:**

```go
registry.RegisterFactory(soulseek.Factory)  // package-level var
registry.RegisterFactory(deezer.Factory)
resources := plugin.PluginResources{DownloadPath: cfg.Library.DownloadPath}
registry.InitAll(cfg.Sources, resources)
```

### Capability-Based Routing

The underlying `plugin.Registry` supports querying plugins by capability:

```go
// Get all plugins capable of "download":
dlPlugins := registry.Inner().WithCapability("download")

// Get all plugins capable of "metadata" (future):
mdPlugins := registry.Inner().WithCapability("metadata")
```

Use `registry.Inner()` to access the generic `plugin.Registry` from the type-safe `download.Registry` wrapper.

---

## Config Format

### config.json structure

```json
{
  "sources": {
    "soulseek": {
      "slskd_url": "http://localhost:5030",
      "api_key": "abc123",
      "search_timeout": 60,
      "min_upload_speed": 0
    },
    "deezer": {
      "arl": "your-arl-token",
      "quality": "flac",
      "allow_fallback": true,
      "access_token": ""
    }
  },
  "library": {
    "download_path": "./downloads",
    "library_path": "./music",
    "folder_template": "{artist}/{album} ({year})/{track:02d} - {title}",
    "playlist_path": "./playlists",
    "playlist_template": "{position:02d} {artist} - {title}"
  },
  "quality": {
    "preferred_format": "flac",
    "min_bitrate": 0
  }
}
```

### Sources map — `map[string]json.RawMessage`

```go
// internal/config/config.go
type Config struct {
    Sources map[string]json.RawMessage `json:"sources"`
    // ...
}
```

- The key is the source name (e.g. `"soulseek"`, `"deezer"`).
- The value is opaque `json.RawMessage` — the framework never inspects it.
- Each plugin's factory receives its own `json.RawMessage` from `Sources[name]`.
- Unrecognized keys (no matching factory) are silently skipped by `InitAll()`.

### Plugin-local config structs

Each plugin defines its own config struct in its package:

```go
// Soulseek
type SoulseekConfig struct {
    SlskdURL       string `json:"slskd_url"`
    APIKey         string `json:"api_key"`
    SearchTimeout  int    `json:"search_timeout"`
    MinUploadSpeed int    `json:"min_upload_speed"`
}

// Deezer
type DeezerConfig struct {
    ARL           string `json:"arl"`
    Quality       string `json:"quality"`
    AllowFallback *bool  `json:"allow_fallback"`
    AccessToken   string `json:"access_token"`
}
```

### Sensitive field masking

Fields matching known sensitive keys are auto-masked in logs and config dumps.
The `Config.Mask()` method recursively scans source configs.

**Recognized sensitive keys:** `api_key`, `token`, `secret`, `arl`, `password`,
`key`, `api_secret`, `access_token`, `license_token`.

Masking keeps first 2 + last 2 chars: `"ar**********************Ux"`.

**For plugin authors:** Use these JSON field names for any sensitive values.
If your source uses a non-standard field name (e.g. `my_custom_auth`), it won't be masked.
Always prefer the recognized names.

---

## Shared Utilities

The `internal/library` package provides metadata extraction functions that any
plugin can use when search results lack structured artist/title fields (e.g.,
peer-to-peer sources that return raw filenames instead of API JSON).

### `library.ParseArtistTitle`

```go
// internal/library/pathparser.go
func ParseArtistTitle(filename string) (artist string, title string, trackNum int)
```

Extracts artist, title, and track number from a filename. Handles:
- `"Artist - Title.mp3"` → artist, title
- `"08 - Artist - Title.flac"` → artist, title, trackNum=8
- `"01 - Title.mp3"` → trackNum=1, title (no artist)
- `"artist_-_title_(remix).mp3"` → artist, title (underscore-hyphen delimiter)
- Windows `\` path separators (auto-normalized to `/`)

**When to use:** Your source receives files with names like `"Daft Punk - Get Lucky.flac"`
or `"01 - Artist - Title.flac"` instead of structured API data.

**When NOT to use:** Your source has structured API metadata (Deezer, Spotify, Tidal).
Map API fields directly to `TrackResult.Artist`/`TrackResult.Title`.

**Example — Soulseek plugin:**
```go
import "github.com/ramonskie/groovearr/internal/library"

// In Search():
for _, file := range searchResponse.Files {
    artist, title, trackNum := library.ParseArtistTitle(file.Filename)
    results = append(results, domain.TrackResult{
        Artist:      artist,
        Title:       title,
        TrackNumber: trackNum,
        // ... other fields
    })
}
```

### `library.ParseAlbumDir`

```go
// internal/library/pathparser.go
func ParseAlbumDir(dirPath string) (artist string, album string, year string)
```

Extracts artist, album title, and year from a directory path segment.
- `"Artist - Album (2024)"` → artist, album, year=2024
- `"Artist/Album"` → artist, album
- `"Album (2024)"` → album, year=2024

**When to use:** Grouping tracks into albums based on directory structure
(e.g., Soulseek peer shares, scene-release folder names).

### Other library utilities

| Function | Package | Purpose |
|---|---|---|
| `ParseFlatFilename(filename)` | `library` | `"Artist - Title.ext"` → `(artist, album, title)`. No track number awareness. |
| `ParseFileMetadata(path)` | `library` | Full directory-hierarchy parser: `Artist/Album/01 - Title.ext` |
| `TrackNumRE` | `library` | Regex: `^(\d{1,3})[\.\s\-]+(.+)$` — extract track numbers |

---

## Domain Model — What Plugin Authors Set

### Search Results

Plugins return `[]domain.TrackResult` and `[]domain.AlbumResult` from `Search()`.

```go
// internal/domain/search.go
type TrackResult struct {
    SearchResult                      // embedded
    Artist      string `json:"artist,omitempty"`
    Title       string `json:"title,omitempty"`
    Album       string `json:"album,omitempty"`
    TrackNumber int    `json:"track_number,omitempty"`
    CoverURL    string `json:"cover_url,omitempty"`
}

type SearchResult struct {
    Username        string `json:"username"`           // source name or slskd peer
    Filename        string `json:"filename"`           // source-specific encoding
    Size            int64  `json:"size"`               // bytes
    Bitrate         int    `json:"bitrate,omitempty"`  // kbps
    Duration        int64  `json:"duration,omitempty"` // milliseconds
    Quality         string `json:"quality"`            // flac, mp3, ogg, aac, wma
    FreeUploadSlots int    `json:"free_upload_slots"`
    UploadSpeed     int64  `json:"upload_speed"`       // bytes/sec
    QueueLength     int    `json:"queue_length"`
}

type AlbumResult struct {
    Username        string        `json:"username"`
    AlbumPath       string        `json:"album_path"`
    AlbumTitle      string        `json:"album_title"`
    Artist          string        `json:"artist,omitempty"`
    TrackCount      int           `json:"track_count"`
    TotalSize       int64         `json:"total_size"`
    Tracks          []TrackResult `json:"tracks"`
    DominantQuality string        `json:"dominant_quality"`
    Year            string        `json:"year,omitempty"`
    FreeUploadSlots int           `json:"free_upload_slots"`
    UploadSpeed     int64         `json:"upload_speed"`
    QueueLength     int           `json:"queue_length"`
}
```

**Critical field: `Filename`** — This is opaque to the framework but must be passed
back verbatim to `Download()`. Encode whatever the source needs as a string.
Deezer uses `"track_id||display_name"`, Soulseek uses the SMB share path.

**Album grouping:** Albums are detected at the plugin level — return grouped tracks
as `AlbumResult` structs. Framework does not group tracks into albums.

### Download Records

Plugins populate `domain.DownloadRecord` in `GetDownloads()`, `GetDownloadStatus()`,
and internally as state changes.

```go
// internal/domain/download.go
type DownloadRecord struct {
    ID          string        `json:"id"`                     // plugin-specific download ID
    SourceName  string        `json:"source_name"`            // canonical name (e.g. "soulseek")
    Filename    string        `json:"filename"`               // from search result
    DisplayName string        `json:"display_name"`           // human-readable
    State       DownloadState `json:"state"`                  // queued→downloading→...→imported
    Progress    float64       `json:"progress"`               // 0.0 — 100.0
    Size        int64         `json:"size"`                   // bytes
    Transferred int64         `json:"transferred"`            // bytes
    Speed       int64         `json:"speed"`                  // bytes/sec
    FilePath    string        `json:"file_path,omitempty"`    // local disk path after download
    Error       string        `json:"error,omitempty"`        // failure reason
    Username    string        `json:"username,omitempty"`     // source-specific username
    TrackID     string        `json:"track_id,omitempty"`     // source-specific track ID
    CoverURL    string        `json:"cover_url,omitempty"`    // album cover URL
    PlaylistID  string        `json:"playlist_id,omitempty"`  // playlist association

    // Metadata for post-download organization.
    Artist      string `json:"artist,omitempty"`
    Album       string `json:"album,omitempty"`
    Title       string `json:"title,omitempty"`
    TrackNumber int    `json:"track_number,omitempty"`
    DiscNumber  int    `json:"disc_number,omitempty"`
    Year        int    `json:"year,omitempty"`
}
```

**State machine (handled by framework, plugin sets `State`):**

```
queued → downloading → importPending → importing → imported
               ↓              ↓            ↓
             failed         failed       failed
               ↓
            queued (retry)
```

Terminal states: `imported`, `failed`, `ignored`.

**After download completion:** Set `State = DownloadImported` and populate `FilePath`
with the absolute path. The import pipeline takes over from there.

### External IDs

Library tracks carry cross-referenced external IDs for matching:

```go
// internal/domain/track.go
type Track struct {
    // ...
    ExternalIDs map[string]string `json:"external_ids,omitempty"` // keyed by source name
    AcoustID    string            `json:"acoustid,omitempty"`
    ISRC        string            `json:"isrc,omitempty"`
}
```

| Key | Meaning | Set by |
|-----|---------|--------|
| `"deezer"` | Deezer track ID | Deezer plugin |
| `"spotify"` | Spotify track URI | Matching engine |
| `"musicbrainz"` | MusicBrainz ID | Tag reader |

**For plugin authors:** The import pipeline handles `ExternalIDs` population —
plugins do not write to the library store directly. Set metadata in
`DownloadRecord` and the `TagWriter` + `LibraryImporter` handlers populate tracks.

---

## Walkthrough: Adding a New Source (Tidal)

Step-by-step for a hypothetical Tidal plugin.

### File layout

```
internal/providers/tidal/
  factory.go      — plugin.PluginFactory + config struct
  client.go       — download.Plugin implementation (HTTP client, state)
  api.go          — API response types (optional)
```

### Step 1: Create the config struct

```go
// internal/providers/tidal/factory.go
package tidal

import (
    "encoding/json"
    "github.com/ramonskie/groovearr/internal/plugin"
)

const pluginName = "tidal"
const displayName = "Tidal"

type TidalConfig struct {
    AccessToken string `json:"access_token"` // auto-masked (contains "token")
    CountryCode string `json:"country_code"`
    Quality     string `json:"quality"`      // "lossless", "high", "normal"
}
```

### Step 2: Implement download.Plugin

```go
// internal/providers/tidal/client.go
package tidal

import (
    "context"
    "fmt"
    "sync"

    "github.com/ramonskie/groovearr/internal/domain"
    "github.com/ramonskie/groovearr/internal/download"
)

type Client struct {
    cfg    TidalConfig
    dlPath string

    mu        sync.RWMutex
    downloads map[string]*domain.DownloadRecord
}

func New(cfg TidalConfig, downloadPath string) *Client {
    return &Client{
        cfg:       cfg,
        dlPath:    downloadPath,
        downloads: make(map[string]*domain.DownloadRecord),
    }
}

func (c *Client) Name() string             { return pluginName }
func (c *Client) DisplayName() string      { return displayName }
func (c *Client) IsConfigured() bool       { return c.cfg.AccessToken != "" }
func (c *Client) Connected() bool          { return c.IsConfigured() }

func (c *Client) CheckConnection(ctx context.Context) error {
    if !c.IsConfigured() {
        return fmt.Errorf("tidal: access token not configured")
    }
    // Ping Tidal API.
    return nil
}

func (c *Client) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
    // Call Tidal search API, map responses to domain types.
    return nil, nil, nil
}

func (c *Client) Download(ctx context.Context, username, filename string, fileSize int64) (string, error) {
    id := "tidal-" + generateID(filename)
    c.mu.Lock()
    c.downloads[id] = &domain.DownloadRecord{
        ID:         id,
        SourceName: pluginName,
        Filename:   filename,
        State:      domain.DownloadQueued,
    }
    c.mu.Unlock()
    return id, nil
}

func (c *Client) GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    out := make([]domain.DownloadRecord, 0, len(c.downloads))
    for _, r := range c.downloads {
        out = append(out, *r)
    }
    return out, nil
}

func (c *Client) GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    r, ok := c.downloads[downloadID]
    if !ok {
        return nil, fmt.Errorf("tidal: download %s not found", downloadID)
    }
    return r, nil
}

func (c *Client) CancelDownload(ctx context.Context, downloadID string, remove bool) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    if remove {
        delete(c.downloads, downloadID)
    }
    return nil
}

func (c *Client) ClearCompleted(ctx context.Context) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    for id, r := range c.downloads {
        if r.State.Terminal() {
            delete(c.downloads, id)
        }
    }
    return nil
}
```

### Step 3: Create the factory

```go
// internal/providers/tidal/factory.go (continued)

var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string           { return pluginName }
func (f *factory) DisplayName() string    { return displayName }
func (f *factory) Capabilities() []string { return []string{"download"} }

func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
    var cfg TidalConfig
    if err := json.Unmarshal(rawCfg, &cfg); err != nil {
        return nil, fmt.Errorf("tidal: invalid config: %w", err)
    }
    return New(cfg, resources.DownloadPath), nil
}

func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
    var cfg TidalConfig
    if err := json.Unmarshal(rawCfg, &cfg); err != nil {
        return err
    }
    validQualities := map[string]bool{"lossless": true, "high": true, "normal": true}
    if !validQualities[cfg.Quality] {
        return fmt.Errorf("tidal.quality: must be lossless, high, or normal")
    }
    return nil
}

func (f *factory) DefaultConfig() json.RawMessage {
    return json.RawMessage(`{"access_token":"","country_code":"US","quality":"lossless"}`)
}
```

### Step 4: Register in main.go

```go
// cmd/groovearr/main.go

import (
    // ... existing imports ...
    "github.com/ramonskie/groovearr/internal/providers/tidal"
)

func main() {
    // ... existing setup ...

    registry.RegisterFactory(soulseek.Factory)
    registry.RegisterFactory(deezer.Factory)
    registry.RegisterFactory(tidal.Factory)          // <-- add this line

    resources := plugin.PluginResources{DownloadPath: cfg.Library.DownloadPath}
    err := registry.InitAll(cfg.Sources, resources)
    // ...
}
```

### Step 5: Add config to config.json

```json
{
  "sources": {
    "soulseek": { ... },
    "deezer": { ... },
    "tidal": {
      "access_token": "your-tidal-token",
      "country_code": "US",
      "quality": "lossless"
    }
  },
  "library": { ... }
}
```

The key `"tidal"` must match `factory.Name()`.

### Step 6: (Optional) Implement DownloadProgressor

```go
func (c *Client) GetProgress(ctx context.Context, downloadID string) (*download.Progress, error) {
    c.mu.RLock()
    r, ok := c.downloads[downloadID]
    c.mu.RUnlock()
    if !ok {
        return nil, fmt.Errorf("tidal: download %s not found", downloadID)
    }
    return &download.Progress{
        DownloadID:  downloadID,
        Transferred: r.Transferred,
        Total:       r.Size,
        Speed:       r.Speed,
    }, nil
}
```

### Step 7: (Optional) Implement PlaylistSourceProvider

```go
// internal/providers/tidal/client.go
func (c *Client) PlaylistSource() playlist.Source {
    return &tidalPlaylistSource{client: c}
}
```

Then define a `playlist.Source` implementation (separate file, `playlist.go` in the same package).

**Auto-registration in main.go is already wired** — any plugin implementing `PlaylistSourceProvider`
gets its playlist source registered automatically when configured.

### Step 8: (Optional) Declare additional capabilities

If Tidal also provides metadata enrichment (artist bios, album reviews), add it to capabilities:

```go
func (f *factory) Capabilities() []string { return []string{"download", "metadata"} }
```

The metadata domain can then discover it via:

```go
mdPlugins := registry.Inner().WithCapability("metadata")
```

---

## Adding Non-Download Plugins (Future)

The `plugin` framework is domain-agnostic. To add a new domain:

```go
// internal/metadata/provider.go
type MetadataProvider interface {
    plugin.BasePlugin

    SearchArtist(ctx context.Context, name string) ([]ArtistMatch, error)
    GetArtistDetails(ctx context.Context, sourceID string) (*ArtistDetail, error)
}
```

A provider like Spotify would implement both `download.Plugin` and `MetadataProvider`,
and declare `Capabilities() []string{"download", "metadata"}`. Each domain queries
`registry.Inner().WithCapability(cap)` to discover its plugins.

---

## Reference

### All Interfaces

| Interface | Package | Purpose | Required? |
|-----------|---------|---------|-----------|
| `BasePlugin` | `internal/plugin` | Identity, readiness, connectivity | **Yes** (via download.Plugin) |
| `PluginFactory` | `internal/plugin` | Self-registration with capabilities | **Yes** |
| `download.Plugin` | `internal/download` | Download source contract (extends BasePlugin) | **Yes** (for download sources) |
| `SearchPlugin` | `internal/download` | Incremental search with progress callback | No |
| `DownloadProgressor` | `internal/download` | Byte-level download progress polling | No |
| `PlaylistSourceProvider` | `internal/playlist` | Bridge to playlist import | No |
| `playlist.Source` | `internal/playlist` | Playlist browse + import | Only if providing playlists |

### Key Types — Domain

| Type | Package | Source file | Used in |
|------|---------|-------------|---------|
| `TrackResult` | `internal/domain` | `search.go` | `Plugin.Search()` |
| `AlbumResult` | `internal/domain` | `search.go` | `Plugin.Search()` |
| `SearchResult` | `internal/domain` | `search.go` | Embedded in `TrackResult` |
| `DownloadRecord` | `internal/domain` | `download.go` | All download state methods |
| `DownloadState` | `internal/domain` | `download.go` | `DownloadRecord.State` |
| `Track` | `internal/domain` | `track.go` | Library store (plugins don't write directly) |

### Key Types — Plugin System

| Type | Package | Source file | Purpose |
|------|---------|-------------|---------|
| `BasePlugin` | `internal/plugin` | `plugin.go` | Minimal plugin contract |
| `PluginFactory` | `internal/plugin` | `factory.go` | Factory with capabilities |
| `PluginResources` | `internal/plugin` | `factory.go` | Runtime resources for `Create()` |
| `plugin.Registry` | `internal/plugin` | `registry.go` | Generic plugin registry |
| `download.Registry` | `internal/download` | `registry.go` | Type-safe wrapper for download plugins |
| `Progress` | `internal/download` | `plugin.go` | Download progress snapshot |
| `Config` | `internal/config` | `config.go` | Top-level config with `Sources` map |

### Config Keys

| JSON Key | Go Type | Where Defined |
|----------|---------|---------------|
| `sources` | `map[string]json.RawMessage` | `config.Config.Sources` |
| `sources.<name>` | Plugin-specific struct | Plugin's local config struct |
| `library.download_path` | `string` | `config.LibraryConfig.DownloadPath` |

### Directory Structure Convention

```
internal/providers/<source>/
  factory.go       — plugin.PluginFactory, config struct, DefaultConfig()
  client.go        — download.Plugin implementation (or download.go)
  api.go           — API response types (optional)
  playlist.go      — playlist.Source implementation (optional, in same package)
```

### Existing Plugins (Reference Implementations)

| Source | Package | Lines | Capabilities |
|--------|---------|-------|-------------|
| Soulseek (slskd) | `internal/providers/soulseek` | ~680 | `["download"]` |
| Deezer | `internal/providers/deezer` | ~1000 | `["download", "playlist"]` |

Read `internal/providers/soulseek/client.go` for the simplest HTTP-based plugin.
Read `internal/providers/deezer/download.go` for a plugin with authentication and
playlist integration.
