# Metadata Enrichment

> Source of truth: `internal/download/handler_enrichment.go`,
> `internal/metadata/provider.go`, `internal/metadata/cooldown.go`,
> `cmd/groovearr/app.go` (provider cooldown + order wiring).

`MetadataEnrichmentHandler` (import chain step 5, and the bulk library job) runs
registered metadata providers against a library track. It resolves album titles,
downloads cover art and artist images, and enriches tracks with ISRC, genres,
release date, label, and external IDs (MusicBrainz MBIDs, etc.).

## Provider Loop

```mermaid
flowchart TD
    ENTER["enrichTrack(record, bulk)"] --> LOAD["load track / artist / album from libStore"]

    LOAD --> BULK{"bulk mode?"}
    BULK -- yes --> BK1{"metadata complete<br/>ISRC + ExternalIDs + genres<br/>+ releaseDate + cover?"}
    BK1 -- yes --> BK2{"artist image missing &<br/>not attempted this run?"}
    BK2 -- yes --> BK3["enrichArtistImage()"]
    BK2 -- no --> SKIP["return nil (skip track)"]
    BK3 --> SKIP
    BK1 -- no --> P
    BULK -- no --> P

    P["sync AlbumMBID from record<br/>→ album.ExternalIDs[musicbrainz_release]"] --> ORD["orderedProviders()<br/>(metadata_order config)"]

    subgraph PROV["for each provider (in order)"]
        PO{"bulk && providerCoolingDown(name)?"}
        PO -- yes --> PSKIP["skip this provider"]
        PO -- no --> P1["enrichFromProvider(provider)"]
        P1 --> P1A["album title resolution<br/>(primaryArtist fallback)"]
        P1A --> P1B["cover art: SearchCover<br/>then CoverArtArchive by MBID"]
        P1B --> P1C["EnrichTrack → ISRC / genres /<br/>release date / label / externalIDs"]
        P1C --> P1D{"bulk && track fully complete?"}
        P1D -- yes --> PBREAK["break provider loop"]
        P1D -- no --> P1E["continue to next provider"]
    end

    PROV --> IMG["enrichArtistImage() (unless skipped)"]
    IMG --> THUMB["thumb_url sync (cover.jpg)"]
    THUMB --> PERSIST["UpsertTrack / UpsertAlbum"]
    PERSIST --> RETAG{"track or album modified?"}
    RETAG -- yes --> TAGS["tagger.WriteTags(file, ...)"]
    RETAG -- no --> DONE
    TAGS --> DONE["done"]
```

## Rate Limiting & Provider Cooldown

```mermaid
flowchart LR
    E["provider returns ErrRateLimited<br/>(429, or 503 for MusicBrainz/Discogs/CAA)"] --> S{"bulk?"}
    S -- yes --> M["providerCooldown.MarkAfter(name, retryAfter)<br/>cooldown window: max(retryAfter, 2min) capped at 10min"]
    M --> SK["subsequent tracks in job skip this provider<br/>(providerCoolingDown)"]
    M --> RB["ResetBulk() clears cooldowns at job start"]
    S -- no --> R["per-download path NOT gated —<br/>tries every provider in order"]
```

## Provider Order

- Configurable via `metadata_order` in config.json
  (`{"metadata_order": ["deezer", "musicbrainz", "coverartarchive"]}`).
- Deezer first (fast, 50 req/5s, better album resolution). Falls through to
  MusicBrainz (1 req/s, deep catalog). Cover Art Archive last (MBID-based only).
- Unlisted providers sort to the end.

## Bulk vs Per-Download (code-verified)

| Behavior | Bulk library job (`bulk=true`) | Per-download (import chain) |
|---|---|---|
| Skip already-complete tracks | ✅ (metadata + cover present) | ❌ always runs |
| Cooldown-gated providers | ✅ skip cooling-down providers | ❌ runs every provider |
| Stop early once complete | ✅ break after first full provider | ❌ runs all providers (keeps MBIDs, label) |
| Artist image | once per artist per run (`bulkArtists` dedup) | always refreshes |
| Cooldown reset | `ResetBulk()` at job start | — |

## Key Facts

- **Shared cooldown** — one `metadata.ProviderCooldown` instance is created in
  `app.go` and wired into both the API server and the enrichment handler, so a
  rate limit observed by any consumer cools the provider app-wide briefly.
- **Spotify fail-fast** — `handleRateLimit` retries short `Retry-After` values
  (≤5s) with context-aware sleep; a long Retry-After fails fast with
  `ErrRateLimited` rather than blocking a worker (the enrichment convoy stall
  fix).
- **`primaryArtist`** — strips featured/collaboration artists from comma-
  separated Spotify co-artist strings for fallback searches.
- **Re-tag** — files are re-tagged only when track or album metadata changed.
