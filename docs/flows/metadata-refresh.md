# Metadata Refresh (Sweep-Free Catalog)

> Source of truth: `internal/catalog/types.go`, `internal/catalog/store.go`,
> `internal/catalog/queue.go`, `internal/catalog/merge.go`,
> `internal/catalog/sqlite/store.go`, `internal/catalog/sqlite/queue.go`,
> `internal/catalog/stats.go`, `internal/catalog/sqlite/stats.go`,
> `internal/catalog/migrate/backfill.go`, `internal/refresh/service.go`,
> `internal/refresh/enqueue.go`, `internal/refresh/cold.go`,
> `internal/refresh/bulk.go`, `internal/matching/match.go`,
> `internal/discovery/provider.go`,
> `internal/jobs/warm_library.go`, `internal/jobs/recheck_unmatched.go`,
> `internal/identity/resolver.go`, `internal/identity/score.go`,
> `internal/identity/review.go`, `internal/discovery/identity.go`,
> `internal/providers/deezer/download.go`, `internal/providers/spotify/identity.go`,
> `internal/providers/musicbrainz/provider.go`,
> `internal/providers/musicbrainz/api.go`,
> `internal/api/handlers_library.go`, `internal/api/handlers_discover.go`,
> `internal/api/handlers_jobs.go`, `internal/api/handlers_catalog.go`,
> `internal/config/config.go`,
> `cmd/groovearr/app.go`.

Album metadata (a provider tracklist, the provider link, refresh bookkeeping)
is **materialized** into durable SQLite tables keyed by local library ids — not
kept as a TTL JSON blob. Warm reads are pure SQLite and never call a provider.
Freshness is maintained by a paced, cooldown-aware, service-owned refresh engine
that drains a persisted work queue. There is **no periodic full-library sweep**:
load is proportional to user activity and new content, not to library size.

Provider I/O happens only through `discovery.Provider`, and every provider call
goes through the single app-wide `metadata.ProviderCooldown` bucket (AGENTS §1,
§8). The engine never references a provider name.

Provider iteration is **deterministic and configured**. `discovery.Registry`
returns providers from `plugin.Registry.WithCapability("discovery")`, which
iterates registration order (`r.names`), then sorts them into the one shared
`metadata.ProviderOrder` — the `provider_order` key
(`Config.EffectiveProviderOrder`). The same order instance is
injected into the discovery registry, the engine
(`refreshSvc.SetProviderOrder`), the metadata resolver/enrichment handler, and
the background jobs, so catalog materialization, discover search, enrichment and
the duplicates scan iterate providers in one order (`cmd/groovearr/app.go`).
MusicBrainz is itself a discovery provider (factory capabilities
`["metadata","discovery"]`; `*Client` implements `discovery.Provider`), so an
album only MusicBrainz carries (the album-359 class) can materialize here. Its
`SearchAlbums`/release-group/release lookups ride the same single shared,
rate-limited `*APIClient` as the metadata path (1100ms interval, ~0.91 req/s —
margin under MusicBrainz's 1 req/s per-IP limit) and the same cooldown bucket.
That one live `*APIClient` is shared by catalog refresh, enrichment, health check
and release-resolve through the one `plugin.Registry`, so every path serializes
through one token bucket and one cooldown (AGENTS §8); a config hot-reload
recreates it via `plugin.Registry.Rebuild`, resetting the pacer bucket while the
separate cooldown bucket persists.

## Sweep-Free Model

| Tier | Trigger | Mechanism | Status in code |
|---|---|---|---|
| 0 — lazy cold | First open of an album that is *pending* (no metadata row, or a cold fetch that has not resolved) | One **synchronous**, deduplicated, bounded fetch (`ColdFetcher` → `Service.RefreshAlbum`), then materialized forever | Implemented; gated by `catalog.enabled` |
| 1 — lazy stale | Warm album whose metadata is older than the soft TTL (default 30d) | Serve materialized rows, best-effort enqueue a low-priority refresh behind the response | Implemented (`Server.SetAlbumStaleAfter` ← `Catalog.AlbumStaleAfter()`; the read path enqueues `ReasonStale`) |
| 2 — event | Album import; explicit user refresh; a stale warm read | Enqueue a row on `metadata_refresh_queue` | Implemented (import = event, manual endpoint = manual, stale warm read = stale, pending download-missing = cold) |
| 3 — bulk | Opt-in "warm library" job | Bounded, resumable pass over albums that need warming | Implemented, default off |

**Why no sweep:** a periodic full-library pass would issue one search per album
on every cycle regardless of whether anyone reads it. On the live environment
(~8k albums / ~42k tracks) that is thousands of wasted provider calls per sweep,
and it would starve the single shared cooldown bucket that user-facing searches
also depend on. Warm-on-read plus event enqueues keep provider load tied to
demand.

## Chart 1 — Warm Album Read (zero provider calls)

```text
GET /api/library/albums/{albumID}/discovery        (rate-limit bucket: search)
    |
    v
handleLibraryAlbumDiscovery                         (api/handlers_library.go)
    | GetAlbum / GetArtist (library store); either missing -> 404
    v
buildAlbumDiscovery(ctx, albumID, artistID)
    |
    v
materialized = catalog.GetAlbumTracks(albumID)      [SQLite only]
meta         = catalog.GetAlbumMetadata(albumID)    [SQLite only]
    |
    +-- rows > 0  OR  isMaterializedStatus(meta) (fresh|stale) --- WARM -------+
    |       |  catalog.MergeTracksScoped(rows, albumLocal, artistPool)         |
    |       |     precedence: ISRC (artist-wide) -> (disc,track) -> title      |
    |       |                  (disc/track + title matched album-scoped only)  |
    |       |                                                                   |
    |       |  if isMaterializedStatus(meta) && metadata older than soft TTL:   |
    |       |     enqueueRefresh(ReasonStale, PriorityLow)   [behind response]  |
    |       +-> response metadata_pending = false; metadata_terminal = false    |
    |
    +-- fresh matched-empty (StatusFresh, 0 rows) -> WARM, empty tracks list
    |
    +-- terminal (unmatched|failed) AND !terminalRetryElapsed -> local pending
    |       |  no re-search, no re-enqueue (unmatched keeps its parked queue row;
    |       |  failed has no queue row and is re-armed only by cold-open gate/recheck)
    |       +-> local tracks, metadata_pending = true, metadata_terminal = true
    |
    +-- pending / no metadata row -> COLD path (chart 2)
```

`isMaterializedStatus(meta)` is `fresh || stale`: a **matched album with zero
rows is warm, not cold**, and is never re-fetched on open. `terminalRetryElapsed`
compares the terminal row's refresh/update time against `catalog.UnmatchedRetry`
(90d), so opening an `unmatched`/`failed` album inside the window does not
re-search the provider. That terminal-parked branch deliberately does **not**
enqueue: for `unmatched`, the parked `metadata_refresh_queue` row already carries
its 90-day `due_at`, and re-arming it here would let the engine re-search the
provider within one interval; `failed` has no queue row and is re-armed only by
the cold-open gate after 90d or by an explicit recheck/manual refresh (code:
`terminalRetryParked` / `buildAlbumDiscovery`).

`MergeTracksScoped` is a pure function (`internal/catalog/merge.go`): no IO, no
globals. Local tracks are indexed once by ISRC (matched against the **artist-wide**
pool, because an ISRC is globally unique), and by `(disc, track)` and normalized
title (matched against the **album-local** subset only, so a position/title from a
different album cannot mark this row downloaded). Each materialized row consumes
at most one local track, so the same local file is never handed to two rows.
`albumLocalTracks` returns the album's own tracks first, then the artist's other
tracks, deduplicated by track id (preserving cross-album ISRC matching).

## Chart 2 — Cold Album Read (Option A)

```text
buildAlbumDiscovery: no materialized rows, not isMaterializedStatus,
                     and not (terminal AND !terminalRetryElapsed)
    |
    v
context.WithTimeout(ctx, coldFetchBudget = 20s)          (api/handlers_library.go)
    |
    v
ColdFetcher.FetchMatched(albumID)                        (refresh/cold.go)
    |   if !svc.enabled() (catalog.enabled live closure) -> errColdDisabled
    |   in-package map+mutex dedupe (no singleflight dependency)
    |   first caller starts run(); concurrent opens join the SAME coldCall
    v
run(): context.WithTimeout(context.Background(), timeout)   <- detached from caller
    |   timeout = max(15s defaultColdTimeout, CallTimeout) = 30s in production
    v
Service.RefreshAlbum(ctx, albumID)   -> see chart 3 (provider-link reuse / search -> materialize)
    |
    +-- matched  -> re-read catalog.GetAlbumTracks -> MergeTracksScoped -> metadata_pending = false
    |
    +-- no match / provider error / timeout / no fetcher / disabled / caller ctx ended
             |
             v
        store.GetTracksByAlbum(albumID)   (local-library fallback)
        response: metadata_pending = true
```

Notes (code-verified):

- The caller's context cancelling returns `ctx.Err()` promptly so the HTTP
  request can fall back; it does **not** cancel the shared fetch, whose result is
  still published to any other waiter.
- Because the cold fetch delegates to `Service.RefreshAlbum`, it inherits
  provider-link reuse, the per-call timeout, the shared cooldown check/mark, and
  the per-album in-flight coalescer. A cold materialization also fires the
  `metadata_refresh` completion hook.
- A disabled catalog (`catalog.enabled=false`, read live) skips the provider fetch
  entirely and falls back to local tracks — the read path degrades without
  touching a provider.
- The pending cold open itself does **not** enqueue anything. (A *terminal* album
  opened before its retry is the different branch in chart 1: it serves local
  tracks with `metadata_pending=true` / `metadata_terminal=true` and, likewise,
  does **not** re-enqueue — the parked queue row keeps its 90-day `due_at`.) If
  the cold fetch does not match, the album stays cold until the manual endpoint
  (chart 4) or the bulk pass (chart 5) warms it.

## Chart 3 — Refresh Engine Loop

```text
Service.Start(ctx)  ->  loop goroutine                 (refresh/service.go; own lifecycle, AGENTS §2)
    timer = options().Interval        (live optsFn re-read each tick; default 1m)
    loop:
        select ctx.Done -> return
        timer.C ->
          enabled()  (live catalog.enabled closure; disabled -> skip this tick)
          processBatch:
            store.ClaimDueRefresh(now, BatchSize=8)      ORDER BY priority DESC, due_at ASC
            for each item:
              entity_type != album -> deferUnsupported (RetryRefresh +90d)   [artist deferred]
              else processItem:
                outcome = RefreshAlbum(albumID)
                  matched                 -> FinishRefresh(fresh)            [row deleted]
                  no match                -> RetryRefresh(+UnmatchedRetry 90d)  [album_metadata=unmatched]
                  coolingRetryError       -> RetryRefresh(at CoolingUntil)   [no failure counted]
                  errTransientSearch      -> RetryRefresh(attempts+1, +BackoffFor(attempts))
                                              [pending; never StatusFailed]
                  ctx cancelled/deadline  -> leave row untouched (re-claimed next run)
                  ErrProviderBusy         -> RetryRefresh(+BusyRetry 30s)     [no failure counted]
                  ErrRateLimited / errAllCooling / ErrNoProviders
                                          -> RetryRefresh(+CooldownRetry 15m)  [no failure counted]
                  ErrAlbumNotFound        -> FinishRefresh(failed)          [terminal]
                  other error             -> retryOrFail:
                                               attempts++
                                               >= MaxAttempts(5) -> recordAlbumFailure(failed)
                                                                    + FinishRefresh(failed) [terminal]
                                               else -> RetryRefresh(+BackoffFor(attempts))
          timer.Reset(options().Interval)
```

`RefreshAlbum(ctx, albumID)` (steady-state refresh):

```text
GetAlbumMetadata(albumID)
providers()                         live closure; empty -> ErrNoProviders
  |
  | 1. PROVIDER-LINK REUSE (one provider call, no search)
  |    meta.ProviderAlbumID != "" AND provider live AND !coolingDown(name)
  |       fetchTracks(GetAlbumTracks, CallTimeout) -> materialize -> Matched
  |       tracklist error (not rate-limited) -> clearProviderLink(album)
  |         (next attempt falls back to a fresh search instead of staying stuck)
  |       rate-limited / ErrProviderBusy / DeadlineExceeded / Canceled
  |         -> return the error (link kept; not the link's fault)
  |
  | 2. else: resolve local artist name + album title
  | 3. q = discovery.AlbumQuery{Artist: artistName, Title: album.Title}
  |    (structured: the artist/title boundary is carried, never re-guessed)
  |    for each provider in shared order (skip cooling):
  |       searchAlbums(q, SearchLimit) -> matching.BestMatch
  |          (one request per provider, no split heuristic; the matcher lives in
  |           internal/matching (NormalizeTitle, ArtistCompatible, BestMatch) and
  |           the refresh package delegates to it — no local duplicate.
  |           BestMatch returns the FIRST acceptable match in that provider's
  |           RANKED results — there is no cross-provider ranking, the caller
  |           owns provider order. NormalizeTitle drops a DELIMITED trailing
  |           same-content edition/format suffix — markers {deluxe, edition,
  |           expanded, anniversary, remaster, remastered, bonus, explicit,
  |           clean} plus bare years, and the separator-only format words
  |           "- Single" / "- EP" — then normalizes case, quotes, brackets and
  |           punctuation, then drops a `feat.` credit, so "Échame la culpa -
  |           Single" matches "Échame La Culpa". A suffix is dropped only when
  |           delimited (a trailing `(...)`/`[...]` group, or after a dash/colon),
  |           so "New Edition", "First Edition" and "The Single" are preserved.
  |           ArtistCompatible accepts an exact credit (accents folded), a
  |           core alphanumeric match, ANY act inside a collaboration, an
  |           article-only containment, or TWO shared distinctive tokens — so
  |           collab credits ("Roscoe" vs "Nelly, Roscoe", "Masta Ace" vs
  |           "Eminem, J-Black, Masta Ace") match while a genuinely different
  |           artist is rejected. Member splitting uses comma, " & ", " / ",
  |           and feat/ft/featuring; "with"/"x" are not boundaries, so single
  |           acts ("Dancing With Wolves", "Malcolm X Trio") are not split. A
  |           title-only match is accepted when the library artist is
  |           unknown/empty or the candidate omits the artist.)
  |       fetchTracks -> materialize -> Matched
  |
  + 4. classify no-match:
        parent ctx.Err()   -> return ctx.Err() (row untouched)
        rate-limited seen  -> return ErrRateLimited
        tracklist error    -> return (retry with backoff)
        tried == 0         -> transient search error or cooling-retry
        searchErr != nil   -> transient provider search (BackoffFor; no StatusFailed)
        skipped cooling    -> cooling-retry until the cooldown expiry (no count)
        otherwise          -> markUnmatched(album_metadata) ; Matched=false, nil
```

`RefreshAlbum` is the single entry point for the engine loop, the cold fetcher,
and the warm job, and it **coalesces concurrent refreshes of the same album** into
one in-flight call (a per-album `map+mutex`), so those callers share exactly one
provider fetch and one metadata write instead of racing duplicate calls.

`materialize` normalizes provider `(disc, track)` keys before writing (zero/absent
disc → 1, zero track → position, collisions disambiguated forward) so a provider
tracklist with repeated keys cannot violate `UNIQUE(album_id, disc_number,
track_number)` and abort the album. It writes `album_tracks` (`ReplaceAlbumTracks`,
`origin='provider'`) and upserts `album_metadata` (`refresh_status='fresh'`,
`partial` from the caller) in short per-album transactions — never across a
provider call — preserving the previous `last_accessed_at`. It then fires
`notifyMaterialized`, which the server wires to an SSE broadcast (chart 4).
A partial row is written by the identity resolver for a 1-track local release
resolved to a track on a provider release known to have more than one track;
`refreshAlbum` returns matched immediately for it, so the engine never expands
the single into the full album. An unknown/zero provider track count is not
marked partial, so the album remains refreshable.

Timing constants (`internal/catalog/queue.go`, `DefaultOptions`):
`BackoffBase` 1m, exponential `2^(n-1)` capped at `BackoffCap` 24h;
`UnmatchedRetry` 90d; `CooldownRetry` 15m; `CallTimeout` 30s.

## Chart 4 — Event Enqueue Sources

```text
source                                   call                              reason / priority
--------------------------------------   -------------------------------   ------------------------
album import (post-download)             AlbumImportHandler.enqueueAlbumRefresh -> EnqueueAlbum   event  / high
  internal/download/handler_album_import.go   (best-effort; 5s timeout)
manual endpoint                          handleLibraryAlbumRefresh        manual / high
  POST /api/library/albums/{albumID}/refresh    -> EnqueueAlbum
warm read (stale)                        buildAlbumDiscovery -> enqueueStaleRefresh   stale / low
  internal/api/handlers_library.go
download-missing on a pending album      handleLibraryAlbumDownloadMissing -> enqueueRefresh   cold / low
  POST /api/library/albums/{albumID}/download-missing
one-time legacy backfill remainder       backfill.enqueueRemainder        bulk   / low
  internal/catalog/migrate/backfill.go

materialization completion hook          Service.SetOnMaterialized        -> SSE "metadata_refresh"
  cmd/groovearr/app.go                       sseHub.Broadcast(type=metadata_refresh, data={"album_id": N})
```

Enqueue writes one row and returns immediately; it never blocks on a provider.
`EnqueueRefresh` upserts on the `UNIQUE(entity_type, entity_id)` key, keeping the
higher of the existing/new priority. Re-enqueuing the same album re-arms it
rather than duplicating.

**Not yet wired:** `EnqueueArtist` (no production caller). The refresh engine
defers `entity_type = artist` (`deferUnsupported`, +90d) — artist engine
materialization is a later phase. Tracked-artist refresh does **not** enqueue
album metadata refresh.

## Chart 5 — Opt-In Bulk Warm Job

```text
POST /api/jobs/warm-library        (admin-only; rate-limit bucket: scan)
    |
    v
startJob("warm-library", runners.WarmLibrary())
    |
    v
WarmCandidates(ctx, catalogStore, libStore, opts)        (refresh/bulk.go)
    |  opts = WarmOptions{Limit: Catalog.BatchSize}; 0 -> DefaultWarmOptions (Limit 50, ScanLimit 10000)
    |  ListAlbumsDueForRefresh(now, ScanLimit)      -> every album (cutoff = now)
    |  classifyWarm per album:
    |       providerCount = rows with origin='provider'   (library rows do NOT count)
    |       Tier(): provider>0 && local>=provider  -> complete
    |               provider>0                     -> incomplete
    |               has album_metadata row         -> never-materialized
    |               otherwise                      -> new
    |  selectWarm: sort by tier, then album id; drop Excluded(now) (terminal
    |      unmatched/failed before UnmatchedRetry); drop complete unless
    |      IncludeComplete; cap Limit
    |
    v
for each candidate:   RefreshAlbum(ctx + 2m warmLibraryTimeout)   -> chart 3 materialize
    |  report done/total + pace-based ETA after every album; ctx checked between albums
    v
candidates recomputed on EVERY invocation -> resumable with no persisted cursor
(materialized albums are now complete and drop out of the next run)
```

The job registers no ticker and runs no sweep; re-triggering it resumes with the
remaining candidates. Complete albums are skipped because re-materializing them
would spend provider calls for no gain. `IncludeComplete` defaults off.

## Tables (additive schema, `internal/catalog/sqlite/store.go`)

| Table | Key columns | Notes |
|---|---|---|
| `album_tracks` | `album_id, disc_number, track_number, title, artist_name, duration_ms, isrc, provider_name, provider_track_id, origin, created_at, updated_at` | `UNIQUE(album_id, disc_number, track_number)`; `FK albums ON DELETE CASCADE`; `origin ∈ {library, provider}` |
| `album_metadata` | `album_id PK, provider_name, provider_album_id, refresh_status, last_refreshed_at, last_accessed_at, consecutive_failures, refresh_error, updated_at, partial` | Durable provider link + bookkeeping; `partial` marks a deliberately subsetted 1-track materialization |
| `artist_metadata` | `artist_id PK, provider_name, provider_artist_id, image_url, genres, summary, refresh_status, last_refreshed_at, consecutive_failures, refresh_error, updated_at` | Written by the discover artist read path |
| `artist_discography` | `artist_id, provider_name, provider_album_id, title, year, type, cover_url, track_count, last_seen_at` | Display-only read-model |
| `metadata_refresh_queue` | `entity_type, entity_id, reason, priority, due_at, attempts, last_error, created_at` | `UNIQUE(entity_type, entity_id)`; index `(priority, due_at)`; no FK (polymorphic id) |

`refresh_status ∈ {pending, fresh, stale, unmatched, failed}`. `unmatched` is
retried on a 90d interval (`UnmatchedRetry`) and `failed` (retries exhausted) is
terminal; both report `IsTerminal`, so the read, warm, and backfill paths skip a
parked album until the retry elapses. The SQLite implementation shares the
library's single `*sql.DB`.

**Row writers today:** `album_tracks` holds **provider-expected** rows and is
written by the refresh engine (`origin='provider'`) and by the one-time legacy
backfill (`origin='library'` or `'provider'`, decoded from the old caches).
`ReplaceAlbumTracks` / `DeleteAlbumTracks` / `CountAlbumTracks` and
`catalog.OriginLibrary` have no other production caller — neither scan nor album
import writes catalog rows directly; they only enqueue (see *Known gaps*).

## Legacy Cache Tables (deprecated, retained)

`album_discovery_cache` and `artist_overview_cache` (the old 24h JSON blobs in
`library/sqlite/store.go`) are **retained but no longer read or written** by any
live path. The additive-only rule (§5) forbids dropping them, so existing
databases keep upgrading. A guard test
(`internal/api/legacy_cache_guard_test.go`) fails the build if any production
file outside `library/sqlite/store.go` (schema) and
`catalog/migrate/backfill.go` (one-time read) mentions either table, or if any
production `INSERT`/`UPDATE`/`REPLACE` targets them.

One-time migration: `migrate.Start` (`internal/catalog/migrate/backfill.go`) runs
`Backfill` in a goroutine at app startup, decodes both legacy tables into the
durable catalog, then low-priority-enqueues every still-unmaterialized album.
Best-effort, idempotent, resumable, cancellable; a DB whose legacy tables are
empty pays no cost. Started in `cmd/groovearr/app.go`; `Stop()` runs on shutdown.

> **Scheduled removal.** `internal/catalog/migrate` (the blob decoder) is a
> one-time migration, not a permanent component. Once the first release carrying
> `internal/catalog` has shipped and existing installs have upgraded, delete the
> package and its `cmd/groovearr/app.go` wiring. The legacy tables stay (never
> dropped). Tracked in `.opencode/context/project-intelligence/living-notes.md`.


## Chart 6 — Recheck Unmatched Albums (Identity-First)

```text
POST /api/jobs/recheck-unmatched    (admin-only; rate-limit bucket: scan)
    |
    v
startJob("recheck-unmatched", runners.RecheckUnmatched())    (api/handlers_jobs.go)
    |
    +-- identity.UnmatchedResolver wired --------------------------------+
    |                                                                    |
    |  ResolveUnmatched(ctx)                          (identity/resolver.go)
    |      |  catalog.ListUnmatchedAlbums(limit)      (catalog/sqlite/stats.go)
    |      |  per album: GetAlbumMetadata; skip if no longer unmatched
    |      |  library.GetTracksByAlbum(albumID)
    |      |  probe duration ONLY for tracks with duration=0
    |      |      library.ProbeDuration -> UpdateTrackDuration
    |      |      ErrDurationUnavailable disables probing for the pass
    |      |
    |      |  per track: discovery.TrackIdentityProvider
    |      |      external provider id -> GetTrackIdentity
    |      |      ISRC -> LookupTrackByISRC (skipped once the id candidate
    |      |              already auto-applies)
    |      |      score -> matching.CandidateScore
    |      |      auto only when:
    |      |        score >= 75, title agrees,
    |      |        (artist agrees OR duration agrees),
    |      |        no known duration conflict
    |      |      exact-score tie: equivalent only when non-empty ISRCs
    |      |        match, or neither ISRC conflicts + title/artist agree
    |      |        + both durations known and <=2s
    |      |        -> earlier configured provider wins; else parked
    |      |
    |      +-- 1-track local release:
    |      |      MaterializeResolved(one provider row,
    |      |          partial = provider album has >1 track)
    |      |
    |      +-- multi-track local release:
    |      |      group candidates by provider release
    |      |      only complete groups are eligible
    |      |        (every local track, same provider album)
    |      |      equal-score groups accepted only when every local track
    |      |        is the same recording and known track counts agree;
    |      |        earlier provider wins
    |      |      -> GetAlbumTracks -> MaterializeResolved(full release)
    |      |
    |      +-- ambiguous / wrong-ISRC / conflicting -> stay parked
    |      |
    |      +-- each resolved album -> catalog.DeleteRefreshByEntity(album)
    |      |
    |  report resolved / parked / no-identity / skipped counts
    |
    +-- no resolver OR ErrNoTrackProviders (fallback) --------------------+
    |
    v
UnmatchedRearmer.ReArmTerminalAlbums(ctx, recheckUnmatchedCap)   (refresh/enqueue.go
    |                                                             -> jobs/recheck_unmatched.go)
    v
catalog.Store.ReArmTerminalAlbums(now, limit)        (catalog/sqlite/queue.go)
    |  parked unmatched row (due_at=+90d) -> due-now, priority raised
    |  failed album (terminal transition DELETED its row) -> fresh row
    v
engine next drain: ClaimDueRefresh(now) -> processBatch -> RefreshAlbum (chart 3)
```

Manual review is a separate admin path (Settings → Metadata Catalog **Review
unmatched**), not part of this job. `GET
/api/catalog/unmatched/{id}/review` returns the local tracks, the available
identity-capable providers, and (for a one-track release) the identity
candidates. `POST /api/catalog/unmatched/{id}/match` re-fetches the chosen
provider track/release and calls the same `MaterializeResolved`; a track match
needs identity support, while a release match only needs `GetAlbumTracks`, so
the discovery-search fallback works for any configured provider. A release
smaller than a multi-track local album is rejected rather than deleting rows.

The identity pass is the explicit, user-triggered override of the 90-day
`UnmatchedRetry` park. It does **not** re-run the album-string search for
ambiguous albums: identity evidence has already shown that the album string is
not enough, so those rows stay parked for the review UI. Provider access is
through the optional `discovery.TrackIdentityProvider` capability (Deezer by
ISRC, Spotify by track id / ISRC); core packages never reference a provider
name (§1). Every call consults and marks the one shared
`metadata.ProviderCooldown` bucket (§8).

`MaterializeResolved` uses the same per-album in-flight coalescer as
`RefreshAlbum`, so an identity result and a concurrent engine refresh cannot
clobber each other's rows. A partial materialization (`album_metadata.partial`)
is never expanded by the engine: `refreshAlbum` returns matched immediately for
it (chart 3), so a 1-track local release stays a single.

## Observability

The engine is a service-owned loop, not a `jobs.Manager` job, so it does **not**
appear in `GET /api/jobs`. Its status surface is **`GET /api/catalog/status`**
(admin-only), assembled from the read-only `catalog.StatsStore` (`QueueStats`,
`AlbumStatusCounts`, `CountArtistMetadata`) and the engine's
`refresh.Service.Status()` snapshot. Settings → Jobs renders it as the
"Metadata Catalog" card, whose **Warm library** button starts the opt-in
`POST /api/jobs/warm-library` job and whose **Recheck unmatched** button starts
`POST /api/jobs/recheck-unmatched` (chart 6). The count alone cannot identify
the parked albums, so **`GET /api/catalog/unmatched`** (admin-only) exposes the
same `StatsStore`'s `ListUnmatchedAlbums` rows (album id, artist, title,
persisted error, updated_at) as a read-only diagnostic. An idle engine with an
empty queue is normal: the queue fills only on demand — a cold album open, a
stale warm read, an album import, a manual refresh, or the warm job.

## Provider I/O & Rate Limiting

- All provider calls go through `discovery.Provider` (`SearchAlbums`,
  `GetAlbumTracks`); no provider name is referenced outside the provider package.
- Providers are tried in the one shared order: `WithCapability("discovery")`
  preserves registration order, then `metadata.Order` sorts by `provider_order`.
  `RefreshAlbum` has no cross-provider ranking —
  `matching.BestMatch` returns the first acceptable match within the current
  provider, and the first provider with a match wins.
- Album search is structured: the engine passes
  `discovery.AlbumQuery{Artist, Title}` (never a re-joined string), and the
  generic matcher is `internal/matching` — the refresh package delegates to it.
- MusicBrainz is a discovery provider (`["metadata","discovery"]`); its search
  and lookup calls share the single `*APIClient` (1100ms interval, ~0.91 req/s —
  margin under MusicBrainz's 1 req/s per-IP limit) with the metadata path, so
  MB-only albums can materialize without a second client or a second pacer.
  `SearchReleaseGroups` returns the ranked result list (not just the top hit);
  the structured path issues ONE release-group request with explicit
  `artist`/`releasegroup` terms, the free-text path issues one bare query, and
  partial results are kept when a later candidate errors.
- Before a call: `coolingDown(name)` skips a parked provider. On
  `metadata.ErrRateLimited`: `markProviderError` marks the shared cooldown
  (`MarkAfter`, honoring a server `Retry-After`), so a bulk pass cannot push a
  concurrent user search into the same throttle.
- The engine's interval/batch/stale-TTL come from the live config closure
  (`SetOptionsFunc`), re-read every tick; `Interval`/`BatchSize` map from
  `config.Catalog` (zero meaning the engine default), and `AlbumRefreshDays` maps
  to `Options.StaleAfter`, which the read path also consumes via
  `Service.AlbumStaleAfter`. `ArtistRefreshDays` and `Concurrency` are **not**
  consumed (reserved; see *Known gaps*).

## Known Gaps / Deferred (code-verified, not aspirational)

1. **Artist engine materialization is deferred.** The engine defers
   `entity_type = artist`; `EnqueueArtist` is unused. The discover artist
   overview is instead materialized synchronously in
   `handleDiscoverArtistOverview` (`materializeArtistOverview`), and its
   `artistOverviewResponse` carries the same `metadata_pending` hint.
2. **Jobs → refresh coupling is one-way.** Tracked-artist refresh does **not**
   enqueue an album metadata refresh; the `tracked-refresh` / `warm-library` jobs
   and the metadata refresh engine remain separate loops. Feeding tracked
   releases into the catalog queue is a later phase.
3. **Hot-album ordering is dormant.** `Store.TouchAlbumAccessed` has no
   production caller and `album_metadata.last_accessed_at` is never written, so
   `ListAlbumsDueForRefresh` orders the warm pass by a column that stays `NULL`
   (the pass still enumerates every album and filters by tier).
   `last_accessed_at` is preserved across materialization for when it is wired.
