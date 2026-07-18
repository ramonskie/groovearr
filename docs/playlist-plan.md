# Playlist Support — Implementation Plan

> Phase 1: Deezer. Architecture designed for Spotify/Tidal later.

---

## Architecture

```
playlist.Source (interface)
  ├── deezer.PlaylistClient  (implements Source)
  ├── spotify.PlaylistClient  (future — Phase 2)
  └── tidal.PlaylistClient    (future — Phase 3)

playlist.Registry             (source registry, like download.Registry)

playlist.Service              (import + sync orchestration)
  ├── Source.GetPlaylists()   → list user's playlists
  ├── Source.GetTracks()      → get tracks for a playlist
  ├── library.Store           → persist playlist + tracks
  └── matching.Engine         → match playlist tracks to library

API endpoints:
  GET  /api/playlists/sources     → list configured playlist sources
  GET  /api/playlists             → list imported playlists
  GET  /api/playlists/{id}        → get playlist with tracks
  POST /api/playlists/import      → import playlist from source
  POST /api/playlists/{id}/sync   → refresh + discover + download missing
  DELETE /api/playlists/{id}      → remove imported playlist

UI: New "Playlists" tab in SPA
```

---

## Database — Migration v2

```sql
CREATE TABLE playlists (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  source             TEXT NOT NULL,       -- "deezer", "spotify", "tidal"
  source_playlist_id TEXT NOT NULL,        -- source-specific ID
  name               TEXT NOT NULL,
  description        TEXT,
  track_count        INTEGER,
  cover_url          TEXT,
  owner_name         TEXT,
  is_public          INTEGER DEFAULT 1,
  synced_at          TEXT,
  auto_sync          INTEGER DEFAULT 0,   -- future: background sync
  created_at         TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at         TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(source, source_playlist_id)
);

CREATE TABLE playlist_tracks (
  playlist_id       INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  position          INTEGER NOT NULL,
  track_id          INTEGER REFERENCES tracks(id),   -- NULL if not in library
  source_track_id   TEXT NOT NULL,                    -- source-specific track ID
  title             TEXT NOT NULL,
  artist            TEXT NOT NULL,
  album             TEXT,
  duration_ms       INTEGER,
  isrc              TEXT,
  added_at          TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (playlist_id, position)
);
```

---

## Domain Models

```go
// domain/playlist.go

type Playlist struct {
  ID               int64     `json:"id"`
  Source           string    `json:"source"`
  SourcePlaylistID string    `json:"source_playlist_id"`
  Name             string    `json:"name"`
  Description      string    `json:"description,omitempty"`
  TrackCount       int       `json:"track_count"`
  CoverURL         string    `json:"cover_url,omitempty"`
  OwnerName        string    `json:"owner_name,omitempty"`
  IsPublic         bool      `json:"is_public"`
  SyncedAt         string    `json:"synced_at,omitempty"`
  AutoSync         bool      `json:"auto_sync"`
  CreatedAt        string    `json:"created_at"`
  UpdatedAt        string    `json:"updated_at"`
}

type PlaylistTrack struct {
  PlaylistID     int64  `json:"playlist_id"`
  Position       int    `json:"position"`
  TrackID        *int64 `json:"track_id,omitempty"`    // NULL if not in library
  SourceTrackID  string `json:"source_track_id"`
  Title          string `json:"title"`
  Artist         string `json:"artist"`
  Album          string `json:"album,omitempty"`
  DurationMs     int64  `json:"duration_ms,omitempty"`
  ISRC           string `json:"isrc,omitempty"`
}
```

---

## Playlist Source Interface

```go
// internal/playlist/source.go

type Source interface {
  Name() string
  DisplayName() string
  IsConfigured() bool
  
  // List user's playlists.
  GetPlaylists(ctx context.Context) ([]PlaylistInfo, error)
  
  // Get tracks for a specific playlist.
  GetPlaylistTracks(ctx context.Context, sourcePlaylistID string) ([]PlaylistTrackInfo, error)
}

type PlaylistInfo struct {
  SourceID    string
  Name        string
  Description string
  TrackCount  int
  CoverURL    string
  OwnerName   string
  IsPublic    bool
}

type PlaylistTrackInfo struct {
  SourceTrackID string
  Title         string
  Artist        string
  Album         string
  DurationMs    int64
  ISRC          string
}
```

---

## Phase 1: Deezer Implementation

### Files to create

| File | Purpose |
|------|---------|
| `internal/domain/playlist.go` | Playlist + PlaylistTrack structs |
| `internal/playlist/source.go` | Source interface + info types |
| `internal/playlist/registry.go` | Source registry (Register/Get/Configured) |
| `internal/playlist/service.go` | Import + sync orchestration |
| `internal/playlist/deezer/client.go` | Deezer playlist Source (implements Source interface) |
| `internal/playlist/deezer/client_test.go` | Tests |
| `internal/playlist/service_test.go` | Tests |

### Files to modify

| File | Change |
|------|--------|
| `internal/library/sqlite/store.go` | Migration v2 (playlists + playlist_tracks) + playlist CRUD |
| `internal/library/store.go` | Add playlist methods to Store interface |
| `internal/api/handlers.go` | Add 6 playlist endpoints |
| `internal/api/static/index.html` | Add Playlists tab + UI |
| `cmd/groovearr/main.go` | Wire playlist registry + service |
| `docs/roadmap.md` | Mark #41-#44 progress |
| `internal/config/config.go` | Add `PlaylistPath`, `PlaylistTemplate` to `LibraryConfig` |

### Deezer API methods to add

Deezer uses **ARL cookie auth** (not OAuth). The private gateway API (`gw-light.php`) provides playlist access. Add to `internal/download/deezer/api.go`:

```go
// GetUserPlaylists returns the authenticated user's playlists via ARL auth.
// Uses deezer.pageProfile → USER.PLAYLISTS data.
func (c *Client) GetUserPlaylists(ctx context.Context) ([]PlaylistSummary, error)

// GetPlaylistTracks returns all tracks for a playlist via ARL auth.
// Uses deezer.pagePlaylist → SONG data.
func (c *Client) GetPlaylistTracks(ctx context.Context, playlistID int64) ([]PlaylistTrackInfo, error)
```

Gateway methods:
- `deezer.pageProfile` — returns USER.PLAYLISTS (owned + favorite playlists)
- `deezer.pagePlaylist` — returns SONG list for a playlist ID

### Deezer playlist client

The `deezer.PlaylistClient` wraps the existing `deezer.Client` and implements `playlist.Source`:

```go
type PlaylistClient struct {
  apiClient *deezer.Client
}

func (p *PlaylistClient) Name() string        { return "deezer" }
func (p *PlaylistClient) DisplayName() string  { return "Deezer" }
func (p *PlaylistClient) IsConfigured() bool   { return p.apiClient.IsConfigured() }
```

---

## Config: Playlist Path

Add to `LibraryConfig`:

```go
PlaylistPath     string `json:"playlist_path"`     // separate folder for playlist downloads
PlaylistTemplate string `json:"playlist_template"`  // e.g. "{position:02d} {artist} - {title}"
```

Defaults:
```json
"playlist_path":     "./playlists",
"playlist_template": "{position:02d} {artist} - {title}"
```

Playlist downloads land in `./playlists/{playlist_name}/` organized by template.

---

## Playlist Import + Sync Flow

Single operation: import triggers sync immediately.

```
User clicks "Import from Deezer" → selects playlist
  → POST /api/playlists/import {source:"deezer", playlist_id:"123"}
  → playlist.Service.Import(ctx, sourceName, playlistID)
      1. Source.GetPlaylistTracks(playlistID) → []PlaylistTrackInfo
      2. Create Playlist record in DB
      3. For each track (in order):
         a. Save PlaylistTrack record (all tracks, not just unmatched)
         b. Search library: already have this track? → link track_id, SKIP download
         c. Not in library: Search cross-source via orchestrator
         d. Download best match via orchestrator.DownloadBest()
         e. Downloaded files → post-process (renamer to playlist folder, cover, tags)
         f. After download: trigger library scan to link new track
      4. Update playlist synced_at timestamp
  → Return Playlist with tracks + download status
```

**Key behaviors**:
- All playlist tracks are recorded in DB (matched or not)
- Tracks already in library are skipped (no re-download)
- Downloads use the same pipeline as single song/album (orchestrator → renamer → cover → tags)
- Downloaded files go to `playlist_path/{playlist_name}/` organized by template
- Playlist folder name is sanitized from playlist name

## Playlist Sync Flow (manual + future auto)

```
POST /api/playlists/{id}/sync
  → Refresh tracks from source
  → For any new tracks (not in playlist_tracks): download
  → For deleted tracks: mark as removed
  → Update synced_at
```

Auto-sync: add `auto_sync` boolean to Playlist. Background polling loop checks playlists with auto_sync=true and syncs them periodically (future Phase 2).

---

## Execution Order (Phase 1)

| Step | What | Deps | Files |
|------|------|------|-------|
| 01 | Add playlist_path/template to config | — | `config/config.go`, `config.json.example` |
| 02 | Domain models | — | `internal/domain/playlist.go` (new) |
| 03 | Playlist source interface | 02 | `internal/playlist/source.go` (new) |
| 04 | Add Deezer ARL playlist API methods | — | `deezer/api.go` |
| 05 | DB migration v2 + playlist CRUD | 02 | `sqlite/store.go`, `library/store.go` |
| 06 | Playlist registry | 03 | `internal/playlist/registry.go` (new) |
| 07 | Deezer playlist source | 03, 04 | `internal/playlist/deezer/client.go` (new) |
| 08 | Playlist service (import + sync) | 03, 05, 06 | `internal/playlist/service.go` (new) |
| 09 | Playlist renamer (playlist folder + template) | 02 | `internal/library/playlist_renamer.go` (new) |
| 10 | API endpoints | 05, 08 | `internal/api/handlers.go` |
| 11 | UI: Playlists tab | 10 | `internal/api/static/index.html` |
| 12 | Wiring in main.go | 06, 08 | `cmd/groovearr/main.go` |
| 13 | Tests | 07, 08 | various `_test.go` |
| 14 | Docs update | — | `docs/roadmap.md` |

---

## Phase 2: Spotify (future)

- Spotify playlist source: `playlist/spotify/client.go`
- Implements `playlist.Source` interface
- OAuth flow embedded in UI or separate config
- Registers in `playlist.Registry`
- Same import/sync pipeline works unchanged

## Phase 3: Tidal (future)

- Tidal playlist source: `playlist/tidal/client.go`
- Same interface, same pipeline

---

## Resolved Decisions

1. **Deezer auth**: Use ARL cookie auth (private gateway `gw-light.php`), same as existing download client. No OAuth flow needed yet.

2. **Phase 1 scope**: Full import + sync pipeline. Import triggers download of all tracks. Tracks already in library are skipped. Downloads use the same orchestrator pipeline as single songs.

3. **Playlist folder**: Separate `playlist_path` directory, organized by `playlist_template`. Default: `./playlists/{playlist_name}/{position:02d} {artist} - {title}`.

4. **UI**: Full playlist management page — import from sources, view tracks, manual sync button, auto-sync toggle (future).

5. **Architecture**: `playlist.Source` interface separate from `download.Plugin`. Each source (Deezer, future Spotify/Tidal) implements playlist source independently. Playlist service orchestrates import + download.
