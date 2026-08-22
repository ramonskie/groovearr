# Queue-Time Metadata Resolution

> Source of truth: `internal/metadata/resolver.go` (`MetadataResolver.EnrichMetadata`).

`MetadataResolver.EnrichMetadata` completes **partial** track metadata at queue
time — before a download is queued — by querying configured metadata providers
in priority order (`metadata_order` config). It resolves a missing album name,
then finds cover art.

It is **non-fatal and best-effort**: provider errors are logged at warn level
and enrichment failures leave fields empty rather than blocking the queue
pipeline. Unlike the post-import [Metadata Enrichment](metadata-enrichment.md)
handler, it is **not** gated by `ProviderCooldown`.

## Flowchart

```mermaid
flowchart TD
    E["EnrichMetadata(artist, title, album, year)"] --> EMPTY{"artist == '' || title == ''?"}
    EMPTY -- yes --> RETURN["return as-is"]
    EMPTY -- no --> PROV["OrderedProviders()<br/>(metadata_order config)"]

    PROV --> P1{"album empty?"}

    subgraph ALBUM["Phase 1 — resolve album name"]
        A1["for each provider in order"]
        A2["SearchAlbum(full artist, title)"]
        A3{"found?"}
        A3 -- yes --> A4["result.Album = found → break"]
        A3 -- no --> A5["primary != artist?"]
        A5 -- yes --> A6["SearchAlbum(primaryArtist(artist), title)"]
        A6 --> A7{"found?"}
        A7 -- yes --> A4
        A7 -- no --> A1
        A5 -- no --> A1
        A1 --> A2
    end

    ALBUM --> P2{"result.Album == ''?<br/>(still missing)"}
    P2 -- yes --> RETURN

    subgraph COVER["Phase 2 — find cover art"]
        C1["for each provider in order"]
        C2["SearchCover(artist, result.Album)"]
        C2 --> C3{"error?"}
        C3 -- yes --> C4["primary != artist?"]
        C4 -- yes --> C5["SearchCover(primary, album)"]
        C5 --> C6{"found with ImageURL?"}
        C6 -- yes --> C7["result.CoverURL = url → return"]
        C6 -- no --> C1
        C4 -- no --> C1
        C3 -- no --> C8{"cover != nil && ImageURL != ''?"}
        C8 -- yes --> C7
        C8 -- no --> C9["primary != artist?"]
        C9 -- yes --> C10["SearchCover(primary, album)"]
        C10 --> C11{"found with ImageURL?"}
        C11 -- yes --> C7
        C11 -- no --> C1
        C9 -- no --> C1
    end

    COVER --> RETURN2["return TrackMetadata{Artist, Title, Album, Year, CoverURL}"]

    RETURN --> DONE
    RETURN2 --> DONE["done — caller proceeds to queue"]
```

## Behavior Summary (code-verified)

| Aspect | Behavior |
|---|---|
| Failure mode | Non-fatal. Errors logged `warn`, empty fields, never blocks queue |
| Phase 1 (album) | Skips album lookup if `album` already non-empty |
| Phase 1 fallback | Full artist first, then `primaryArtist(artist)` (before first comma / `&` / `feat.` / `vs.` / `x`) |
| Phase 2 (cover) | Runs only if album resolved; returns on first `ImageURL` hit |
| Phase 2 fallback | Full artist first, then primary artist — per provider |
| Cooldown gating | **None** — every provider is tried on every queue |
| `primaryArtist()` | Strips collaboration suffixes; normalizes `\u00a0` (non-breaking space) in metadata |

## Relationship to Post-Import Enrichment

Queue-time resolution is the **cheap pre-queue pass** (`MetadataResolver`). The
post-import [Metadata Enrichment](metadata-enrichment.md) handler (import chain
step 5) is the **deep pass** that fills ISRC, genres, release date, label,
external IDs, artist images, and MBID sync after the file is in the library.
Both share `OrderedProviders()` ordering and the `primaryArtist` fallback, but
only the post-import handler is cooldown-gated.
