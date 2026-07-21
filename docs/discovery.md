# Metadata-First Search & Download — Architecture Plan

> **Status**: Draft — not yet implemented.
> **Goal**: Never parse artist/title from filenames. Metadata comes from authoritative
> sources (Spotify, Deezer, MusicBrainz). Downloaders only provide the FILE.

---

## Problem

Filenames from P2P sources (Soulseek, future NZB/torrent) are unreliable:
`"50. Dune"`, `"319_ Dune"`, `"1-09"`, `"music\Haddaway"`.

Current code parses artist/title from these filenames via `ParseArtistTitle`. This
produces garbage metadata that ends up in library paths like:
`/music/50. Dune/Pussy Lounge Part 2/50 - Are You Ready To Fly.flac`

## Solution

Introduce a **Discovery Provider** layer. Users browse/search authoritative metadata
sources to find albums/tracks. Those carry clean metadata into the download queue.
Download providers (Soulseek, Deezer, future Prowlarr) only supply the FILE.

After download, a **tag validator** checks that the file's ID3/FLAC tags actually
match the expected metadata. If they don't, the download is rejected and retried
with the next candidate.

---

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                         UI: /discover                                 │
│  Search "Dune" → [Dune (artist)] → [Albums] → [Tracks]               │
│                          ↓                                            │
│              User clicks "Download Album"                             │
└──────────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────────┐
│                    Discovery Providers                                │
│  Spotify (dev mode)    Deezer (public API)    MusicBrainz (future)    │
│  - SearchArtists()     - SearchArtists()      - SearchRelease()       │
│  - GetArtistAlbums()   - GetArtistAlbums()    - GetRelease()          │
│  - GetAlbumTracks()    - GetAlbumTracks()                             │
│  Returns: structured {artist, album, title, year, cover, isrc}       │
└──────────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────────┐
│                    Download Queue (existing)                          │
│  DownloadMeta populated from DISCOVERY (not search result):           │
│    Artist="Dune", Album="Expedicion", Title="Are You Ready To Fly"   │
│    TrackNumber=7, DurationMs=213466, ISRC="ABC123"                   │
│    Username/Filename/Size from Soulseek search (download locator)     │
└──────────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────────┐
│                    Download Orchestrator (existing)                   │
│  For each track: query="Dune Are You Ready To Fly"                   │
│  Searches: Soulseek (slskd), Deezer (ARL)                            │
│  Matches against known duration ±5s                                  │
│  Downloads to /downloads/                                            │
└──────────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────────┐
│                    Import Pipeline (extended)                         │
│  1. TagValidator  ← NEW — artist/title/duration must match expected   │
│  2. FileRenamer   → /music/Dune/Expedicion (1996)/07 - Are You....   │
│  3. CoverArt      → downloads cover.jpg                              │
│  4. TagWriter     → writes ID3/FLAC tags                             │
│  5. LibraryImporter → SQLite                                         │
│  6. MetadataEnrichment → ISRC, genres, MBIDs                         │
│  7. PlaylistLinker → links playlist_track → library_track            │
└──────────────────────────────────────────────────────────────────────┘
```

---

## New Components

### 1. `DiscoveryProvider` interface (`internal/discovery/provider.go`)

```go
package discovery

type Provider interface {
    plugin.BasePlugin

    SearchArtists(ctx context.Context, query string, limit int) ([]ArtistSummary, error)
    GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]AlbumResult, error)
    GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]TrackInfo, error)
    SearchAlbums(ctx context.Context, query string, limit int) ([]AlbumResult, error)
}

type ArtistSummary struct {
    ProviderID string
    Name       string
    ImageURL   string
    Genres     []string
}

type AlbumResult struct {
    ProviderID   string // "spotify:album:xxx" or "deezer:12345"
    ProviderName string // "spotify", "deezer"
    ArtistName   string
    Title        string
    Year         int
    CoverURL     string
    TrackCount   int
    Type         string // "album", "single", "compilation", "ep"
}

type TrackInfo struct {
    ProviderID  string
    ArtistName  string
    AlbumTitle  string
    Title       string
    TrackNumber int
    DiscNumber  int
    DurationMs  int64
    ISRC        string
}
```

### 2. TagValidator handler (`internal/download/handler_tagvalidator.go`)

Runs as position 1 in the import chain, before the renamer.

```
Import chain:
  1. TagValidator      ← NEW
  2. FileRenamer
  3. CoverArt
  4. TagWriter
  5. LibraryImporter
  6. MetadataEnrichment
  7. PlaylistLinker
```

```go
type TagValidator struct{}

func (v *TagValidator) Handle(record *DownloadRecord) error {
    tags := readFileTags(record.FilePath)

    if !artistMatches(record.Artist, tags.Artist) {
        return fmt.Errorf("tag mismatch: expected artist %q, got %q", ...)
    }
    if !titleMatches(record.Title, tags.Title) {
        return fmt.Errorf("tag mismatch: expected title %q, got %q", ...)
    }
    // Duration ±5s
    if absDiff(record.Duration, tags.Duration) > 5000 {
        return fmt.Errorf("tag mismatch: expected duration %dms, got %dms", ...)
    }

    return nil
}
```

**Matching rules:**

| Field | Method | Rationale |
|-------|--------|-----------|
| Artist | Normalized containment or similarity ≥ 0.85 | "Dune" vs "Dune (German)" → match |
| Title | Normalized containment, version-tolerant | "Are You Ready To Fly" vs "Radio Mix" suffix → match |
| Duration | ±5 seconds | Slightly different fade-out is OK |
| Album | Not checked (warn only) | Album tags often wrong on P2P files |

**On rejection**: Download marked as "failed" with reason. Orchestrator retries
with next candidate (exclude_source). If all exhausted, stays "wanted" for retry.

---

## What Gets Deleted

| File | What goes | Why |
|------|-----------|-----|
| `library/pathparser.go` | `ParseArtistTitle`, `ParseAlbumDir`, `splitArtistParts`, `stripArtistPrefix` | No more filename parsing |
| `soulseek/client.go` | Call to `library.ParseArtistTitle` in `processResponses` | TrackResult.Artist/Title set to empty |
| `library/renamer.go` | `ParseFlatFilename` fallback | Renamer only uses file tags + DownloadMeta |

## What Stays

| Component | Why |
|-----------|-----|
| `matching.Engine` (title + duration scoring) | Still needed to match download results against expected metadata |
| Soulseek search | Still finds the FILE. Artist/title on TrackResult become empty. |
| Download queue + workers | Same pipeline, fed with discovery/playlist metadata |
| Renamer | Still reads file tags as primary source, DownloadMeta as fallback |

---

## API Endpoints (new)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/discover/providers` | List configured discovery providers |
| `GET` | `/api/discover/search?q=dune&type=artist,album` | Search across all providers |
| `GET` | `/api/discover/artists/{id}/albums` | Get artist's albums |
| `GET` | `/api/discover/albums/{id}/tracks` | Get album's tracklist |
| `POST` | `/api/discover/albums/{id}/download` | Queue all album tracks for download |
| `POST` | `/api/discover/tracks/{id}/download` | Queue single track for download |

---

## Provider API Status

| Method | Spotify | Deezer | MusicBrainz |
|--------|---------|--------|-------------|
| `SearchAlbums(query)` | ✅ `SearchAlbums` | ✅ `SearchAlbums` | ❌ |
| `GetAlbumTracks(id)` | ✅ `GetAlbum` includes tracks | ✅ `GetAlbumTracks` | ❌ |
| `SearchArtists(query)` | ❌ needs adding | ✅ `SearchArtists` | ❌ |
| `GetArtist(id)` | ✅ `GetArtist` | ✅ `GetArtist` | ❌ |
| `GetArtistAlbums(id)` | ❌ needs adding | ✅ `GetArtistAlbums` | ❌ |

---

## Implementation Order

| Step | What | Effort | Depends on |
|------|------|--------|------------|
| 1 | Spotify: add `SearchArtists`, `GetArtistAlbums` to API client | S | — |
| 2 | `DiscoveryProvider` interface + type-safe registry | S | — |
| 3 | Spotify discovery plugin (wraps existing `API`) | S | 1, 2 |
| 4 | `GET/POST /api/discover/...` endpoints | M | 2, 3 |
| 5 | `POST /api/discover/albums/{id}/download` — bridge to orchestrator | M | 4 |
| 6 | UI: Discover page (artist → albums → tracks → download) | L | 4, 5 |
| 7 | `TagValidatorHandler` — validate file tags match expected metadata | S | 5 |
| 8 | Delete filename parsing code | S | 5, 7 |
| 9 | Wire playlist to use playlist metadata in DownloadMeta | S | 5 |
| 10 | Deezer discovery plugin | M | 2 |
| 11 | MusicBrainz discovery (free tier) | L | 2 |

**MVP**: Steps 1-9. One discovery provider (Spotify), one UI page, tag validation,
no more filename parsing.

---

## Key Design Decisions

1. **No separate Wantlist queue** — reuse existing download queue. DownloadMeta
   already stores Artist/Album/Title. Just populate from discovery instead of
   search results.

2. **Discovery providers use existing plugin system** — register with capability
   `"discovery"` via the same `plugin.Registry` already wired in `main.go`.

3. **Deezer dual role** — Deezer is both a discovery provider (public API) AND a
   download provider (ARL). When user discovers via Deezer and has ARL, use the
   Deezer track ID for direct download — bypass filename matching entirely.

4. **File tags are for VALIDATION, not identification** — the downloaded file
   must prove it matches what we asked for. Discovery metadata is the authority
   for naming and organization.

5. **Existing `/search` page stays** — for power users who want raw download
   provider results. New `/discover` page is the primary flow.
