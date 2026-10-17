# Metadata-First Search & Download — Architecture Plan

> **Status**: Implemented. The discovery layer is live and is the catalog
> engine's backbone (`discovery.Registry.Configured()`); Deezer, Spotify, Tidal,
> Last.fm, Discogs and MusicBrainz all implement `discovery.Provider`. The
> `ReleaseResolver` capability (`internal/discovery/resolver.go`) is implemented
> by MusicBrainz and consumed by prowlarr.
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
│              User clicks "Add album to library"                       │
└──────────────────────────────────────────────────────────────────────┘
                              ↓
┌──────────────────────────────────────────────────────────────────────┐
│                    Discovery Providers                                │
│  Spotify (dev mode)    Deezer (public API)    MusicBrainz            │
│  - SearchArtists()     - SearchArtists()      - SearchArtists()        │
│  - GetArtistAlbums()   - GetArtistAlbums()    - GetArtistAlbums()      │
│  - GetAlbumTracks()    - GetAlbumTracks()     - GetAlbumTracks()       │
│  - SearchAlbums()      - SearchAlbums()       - SearchAlbums()         │
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
│  Searches: Soulseek (slskd/slskr), Deezer (ARL)                      │
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

    // SearchArtists searches for artists by name.
    SearchArtists(ctx context.Context, query string, limit int) ([]ArtistSummary, error)

    // GetArtistAlbums returns an artist's albums.
    GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]AlbumResult, error)

    // GetAlbumTracks returns all tracks on an album.
    GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]TrackInfo, error)

    // SearchAlbums searches for albums. Callers supply a structured
    // AlbumQuery — Artist+Title (the catalog path) or Text (the Discover
    // free-text path) — and render the phrase with AlbumQuery.Query() rather
    // than guessing an artist/title boundary themselves.
    SearchAlbums(ctx context.Context, q AlbumQuery, limit int) ([]AlbumResult, error)
}

// AlbumQuery is a structured album search. Callers set Artist+Title (the
// catalog carries the boundary it already has) or Text (the Discover free-text
// path). Query() renders the phrase with a defined precedence so no provider has
// to re-derive it.
type AlbumQuery struct {
    Artist string
    Title  string
    Text   string
}

// Query renders the search phrase. Structured Artist+Title wins; otherwise the
// bare Title; otherwise the free-text Text. Artist and Title are trimmed and
// whitespace-collapsed when joined. Returns "" only when every field is blank.
func (q AlbumQuery) Query() string {
    if artist := strings.TrimSpace(q.Artist); artist != "" {
        return strings.Join(strings.Fields(artist+" "+strings.TrimSpace(q.Title)), " ")
    }
    if title := strings.TrimSpace(q.Title); title != "" {
        return title
    }
    return strings.TrimSpace(q.Text)
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

### Search contract — `AlbumQuery` + the shared album-result matcher

`SearchAlbums` takes a structured `AlbumQuery` (`internal/discovery/provider.go`),
not a pre-joined `"artist title"` string, so the artist/title boundary is carried
instead of re-guessed. Callers set exactly one form:

| Caller | Sets | Path |
|--------|------|------|
| Catalog refresh engine (`internal/refresh/service.go`) | `{Artist, Title}` | provider resolves the structured pair |
| Discover UI (`internal/api/handlers_discover.go`) | `{Text}` | provider runs one bare free-text query |

Precedence is uniform and defined once by `AlbumQuery.Query()`
(`internal/discovery/provider.go`): structured `Artist`+`Title` when `Artist` is
non-empty, else the bare `Title`, else the free-text `Text`. The title-only
fallback covers an album with no/unknown artist whose catalog record carries a
title but no artist. Provider-specific query building stays inside the provider
package (AGENTS §1); a free-text caller never guesses a split.

**MusicBrainz query term.** The structured path sends `artist:"…"` +
`releasegroup:"…"` in one release-group query, with the title passed through
`matching.TrimEditionSuffix` — a delimited `" - Single"`/`" - EP"` suffix is
dropped, but case and punctuation (`Don't Stop`, `AC/DC`) are kept for
MusicBrainz to tokenize (it does **not** run the full `NormalizeTitle`). The
`artist:` term is omitted when `Artist` is empty, so a title-only query still
sends `releasegroup:"…"` alone. The requested limit is clamped to
`[1, maxReleaseGroupLimit]` before any slice is pre-allocated. A rate-limit error
(`metadata.ErrRateLimited`) always propagates out of the provider — even with
partial results — so the shared provider cooldown (AGENTS §8) can park
MusicBrainz app-wide.

> **Behaviour change — MusicBrainz free-text.** The Discover free-text path for
> MusicBrainz is now a **single bare release-group query** built from the raw text,
> replacing the old multi-split search. Results are broader/looser, and the request
> count per query drops (one request, no artist/title split heuristic).

**Album-result matching is shared.** Deciding which provider result refers to the
wanted release is a pure, provider-agnostic policy in `internal/matching`
(`match.go`): `NormalizeTitle`, `ArtistCompatible`, `BestMatch`. `internal/refresh`
maps `discovery.AlbumResult` onto `matching.AlbumCandidate` and delegates to
`matching.BestMatch`; the duplicated matcher bodies were removed and
`refresh.bestMatch` is now a thin adapter over `matching.BestMatch`. `BestMatch`
returns the FIRST acceptable candidate within one
provider's ranked results — there is no cross-provider ranking, the caller owns
provider order.

Tolerances (code-verified):

- **Title** — `NormalizeTitle` drops a DELIMITED trailing same-content
  edition/format suffix, then normalizes case, quotes, brackets and punctuation,
  then drops a `feat.` credit. The suffix is stripped only when delimited: a
  trailing `(...)`/`[...]` group whose tokens are edition markers
  ({deluxe, edition, expanded, anniversary, remaster, remastered, bonus,
  explicit, clean}) or bare years, or a suffix after a dash/colon separator —
  including the format words `- Single` / `- EP`. This is what lets
  `Échame la culpa - Single` match the provider release group `Échame La Culpa`.
  A bare trailing word (`New Edition`, `The Single`) is preserved.
- **Artist** — `ArtistCompatible` accepts an exact normalized credit with accents
  folded (`Jason Derulo` vs `Jason Derülo`); a core alphanumeric match
  (`LL Cool J` vs `L.L. Cool J`); **any act inside a collaboration**, not only
  the first (`Roscoe` in `Nelly, Roscoe`; `Masta Ace` in
  `Eminem, J-Black, Masta Ace`); an article-only containment
  (`The Cure` ⊃ `Cure`); or at least TWO shared distinctive tokens
  (`Simon & Garfunkel` vs `Garfunkel & Simon`). Member splitting uses comma,
  ` & `, ` / `, and `feat`/`ft`/`featuring`; `with` and `x` are not member
  boundaries, so single acts such as `Dancing With Wolves` and `Malcolm X Trio`
  are not split. The broad prefix tolerance still accepts the act before any
  joiner, including `with`/`x` (`Malcolm` vs `Malcolm X`), but those are not
  member splits. A compact symbol inside one act (`AC/DC`) is not a joiner, and
  a single shared generic token is rejected outside a collaboration
  (`King` vs `King Crimson`).

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

The `discovery.Provider` interface (above) has four methods. All listed providers
implement the full set (`internal/discovery/provider.go`; each provider's
compile-time assertion or method set in its own package).

| Method | Spotify | Deezer | MusicBrainz |
|--------|---------|--------|-------------|
| `SearchAlbums(AlbumQuery)` | ✅ `Plugin.SearchAlbums` | ✅ `DownloadClient.SearchAlbums` | ✅ release-group search → `AlbumResult{ProviderID: rgMBID}` |
| `GetAlbumTracks(id)` | ✅ `Plugin.GetAlbumTracks` | ✅ `DownloadClient.GetAlbumTracks` | ✅ `LookupReleaseGroup` → representative release → `LookupRelease` |
| `SearchArtists(query)` | ✅ `Plugin.SearchArtists` | ✅ `DownloadClient.SearchArtists` | ✅ `Client.SearchArtists` |
| `GetArtistAlbums(id)` | ✅ `Plugin.GetArtistAlbums` | ✅ `DownloadClient.GetArtistAlbums` | ✅ release-groups by artist |

Other implementers expose the same four methods: Tidal (`*Client`), Last.fm
(`*Plugin`), Discogs (`*Plugin`). `GetArtist(id)` is **not** part of
`discovery.Provider`; the provider artist id returned by `SearchArtists` is the
key callers pass back to `GetArtistAlbums`.

### `ReleaseResolver` capability

`internal/discovery/resolver.go` defines **`ReleaseResolver`** beside
`discovery.Provider`:

```go
type ReleaseResolver interface {
    plugin.BasePlugin
    ResolveTracksForCount(ctx context.Context, artist, album string, fileCount int, torrentTitle string) ([]TrackInfo, string, error)
}
```

It returns neutral `discovery.TrackInfo` values so no provider-specific type
crosses the boundary. MusicBrainz's `*Client` implements it
(`SearchReleaseGroup` → `(SearchReleasesByGroup | LookupReleaseGroup)` →
`LookupRelease` → `TrackInfo`), picking the release closest to `fileCount` with
`torrentTitle` word-overlap as tiebreaker.

Consumption is by interface, never by concrete provider (AGENTS §1): a caller
implements `discovery.ReleaseResolverConsumer` (`SetReleaseResolver`) and
`cmd/groovearr/app.go` resolves one resolver from the configured providers and
injects it after construction. `prowlarr` is the current consumer, so it imports
no sibling provider and owns no second MusicBrainz client. After a config-update
rebuild, `internal/api/handlers.go` re-injects the same resolver
(`reinjectReleaseResolver`).

---

## Implementation Order

| Step | What | Effort | Depends on |
|------|------|--------|------------|
| 1 | Spotify: add `SearchArtists`, `GetArtistAlbums` to API client | S | — |
| 2 | `DiscoveryProvider` interface + type-safe registry | S | — |
| 3 | Spotify discovery plugin (wraps existing `API`) | S | 1, 2 |
| 4 | `GET/POST /api/discover/...` endpoints | M | 2, 3 |
| 5 | `POST /api/discover/albums/{id}/download` — delegates to the shared canonical policy `download.Service.QueueAlbumWithFallback` (album-first → per-track fallback), also used by tracking | M | 4 |
| 6 | UI: Discover page (artist → albums → tracks → download) | L | 4, 5 |
| 7 | `TagValidatorHandler` — validate file tags match expected metadata | S | 5 |
| 8 | Delete filename parsing code | S | 5, 7 |
| 9 | Wire playlist to use playlist metadata in DownloadMeta | S | 5 |
| 10 | Deezer discovery plugin | M | 2 |
| 11 | MusicBrainz discovery (free tier) — **implemented** (D-001 Phase 3) | — | 2 |

**MVP**: Steps 1-9. One discovery provider (Spotify), one UI page, tag validation,
no more filename parsing.

**Implemented beyond MVP:** MusicBrainz is now a discovery provider
(capabilities `["metadata","discovery"]`), so the catalog refresh engine can
materialize MusicBrainz-only albums; it also provides the `ReleaseResolver`
capability consumed by prowlarr (see above). Additional discovery providers
(Deezer, Tidal, Last.fm, Discogs) are live.

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
