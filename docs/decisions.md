# Decisions

> **Canonical decision record.** Architectural decisions live here, in committed
> docs. Note: `.tmp/sessions/**` and `.opencode/context/**` are **gitignored**
> (`.gitignore:18,25`) — anything recorded only there is local to one machine,
> not reviewed, and can be lost. Record decisions here.
>
> Status legend: **Approved** | **Proposed** | **Superseded** | **Rejected**

Related: `AGENTS.md` §§1 (provider isolation), 2 (flow discipline), 8 (shared
cooldown); `docs/architecture.md`; `docs/flows/*`; `docs/album-architecture.md`.

---

## D-001 — Unified provider model (Option A)

**Date**: 2026-10-13
**Status**: Approved — Phases 1–3 implemented (2026-10-13); Phase 4 open
**Owner**: product owner

### Context

An audit of `docs/` + all sessions + the code found the provider layer has **no
single flow**:

- **Two registries.** `metadata.Provider` (enrichment) and `discovery.Provider`
  (browse + catalog + tracking) overlap in membership but are separate.
- **Three order sources.** `metadata_order` (enrichment, queue-time resolve);
  registration order for `metadata.Registry.Available()` (artist de-dup);
  registration order for `discovery.Registry` (catalog, discover, tracking).
- **Two MusicBrainz clients.** `internal/providers/musicbrainz/factory.go`
  (`NewClient`) and `internal/providers/prowlarr/factory.go:36`
  (`musicbrainz.NewAPIClient`) — each with its **own** 1 req/s pacer
  (`musicbrainz/api.go:84`), violating AGENTS §8 (one shared bucket).
- **Cross-provider concrete import.** `prowlarr` (a provider) imports the
  `musicbrainz` provider package and constructs its own client — a provider→
  provider dependency (AGENTS §1).
- **MusicBrainz never reaches the catalog engine.** It is the documented
  authority (`docs/discovery.md:4-5`, Key Decision #4) and was planned as a
  discovery provider (`docs/discovery.md` Implementation Order step 11) but was
  never built; the engine consumes only `discovery.Provider`
  (`cmd/groovearr/app.go:278`), so MB-only releases are permanently `unmatched`.

Consequence observed live: `library?album=359` shows "Fetching full tracklist…"
indefinitely for an `unmatched` album.

### Decision

Adopt **one provider flow**:

1. **One inner registry.** `metadata.Registry` and `discovery.Registry` become
   typed views over a **single** `plugin.Registry` (the `NewRegistryFrom(inner)`
   plumbing already exists).
2. **Capabilities are interfaces, not registries.** A provider declares which
   capabilities it implements; callers ask the registry for a capability.
3. **One order source.** A single provider-priority key (now `provider_order`;
   the legacy `metadata_order` is folded into it on load) orders **every** path
   (enrichment, queue-time resolve, artist de-dup, discover, catalog,
   tracking). Registration-order selection is retired.
4. **One cooldown bucket.** Every path checks `CoolingDown` / marks
   `MarkAfter` on the shared `metadata.ProviderCooldown` (already true for the
   catalog engine and artist de-dup; extend to all).
5. **Singleton provider instances.** One `musicbrainz.Client` instance shared by
   all capability paths; no second client anywhere.
6. **`ReleaseResolver` capability (new).** A named interface for release-group →
   expected-tracklist resolution (`SearchReleaseGroup`,
   `SearchReleasesByGroup`, `LookupRelease`). MusicBrainz implements it;
   `prowlarr` consumes it **via the registry**, removing the concrete import and
   its private client. See D-003.
7. **No provider→provider concrete imports** (AGENTS §1).

### Rationale

- Matches the documented intent ("Discovery metadata is the authority") and
  AGENTS §§1, 2, 8.
- Removes the root cause of MB-only albums never materializing.
- One order + one bucket removes the "one task breaks another" class described in
  AGENTS §8.

### Alternatives Considered

| Alternative | Pros | Cons | Why rejected |
|---|---|---|---|
| **B — keep two interfaces, align membership** | Smaller; MB implements `discovery.Provider`; share one MB client | Keeps two registries + two order sources; doesn't fix prowlarr import cleanly | Doesn't achieve "one flow"; ordering divergence persists |
| **C — docs-only + UI spinner fix** | Cheapest | Leaves 1910 albums permanently unmatched; keeps structural split | Doesn't fix the defect, only hides it |

### Impact

- **Positive**: single source of truth; MB reachable for materialization;
  AGENTS compliance; fewer moving parts.
- **Negative**: order unification is a **behavior change** (enrichment vs
  de-dup/discovery currently differ); new `ReleaseResolver` interface is a
  design commitment.
- **Risk**: `Registry.Available()` semantics (metadata-capable without primary
  credentials, e.g. Deezer sans ARL) must be preserved. Change with tests.

### Phases

| Phase | Work | Status |
|---|---|---|
| 0 | This decision record + audit | Done (2026-10-13) |
| 1 | One inner registry + one ordered accessor + singleton MB client | Implemented (2026-10-13) |
| 2 | `ReleaseResolver` capability; migrate `prowlarr` off the concrete import | Implemented (2026-10-13) |
| 3 | MusicBrainz first-class in the catalog engine; verify MB-only albums | Implemented (2026-10-13) |
| 4 | UI truthfulness (pending vs terminal) + doc-drift fixes | Open |

**Implementation note (2026-10-13, Phases 1–3).** What landed (code-verified):

- **Deterministic capability lookup.** `plugin.Registry.WithCapability` now
  iterates `r.names` (registration order) instead of the `r.factories` map, so
  the discovery/catalog provider order is stable across calls
  (`internal/plugin/registry.go`).
- **One order source.** `provider_order` is the single provider priority key
  (the legacy `metadata_order` is folded into it on load — one key, no
  shadowing). `Config.EffectiveProviderOrder` returns `ProviderOrder`
  (`internal/config/config.go`). One `metadata.ProviderOrder`
  (`internal/metadata/order.go`) is built in `cmd/groovearr/app.go` and injected
  into the metadata resolver + enrichment handler, the discovery registry, the
  refresh engine, and the background jobs (`SetProviderOrder`). Registration-order
  selection is retired on those paths.
- **`ReleaseResolver` capability.** Interface in
  `internal/discovery/resolver.go`; the MusicBrainz `*Client` implements it; the
  consumer contract is `discovery.ReleaseResolverConsumer`; injection happens in
  `cmd/groovearr/app.go` (`injectReleaseResolver`), with re-injection after a
  config-update rebuild in `internal/api/handlers.go`
  (`reinjectReleaseResolver`). `prowlarr` imports no sibling provider.
- **MusicBrainz as a discovery provider.** The `musicbrainz` factory declares
  `["metadata","discovery"]`; `*Client` implements `discovery.Provider` and
  `discovery.ReleaseResolver`, so the catalog refresh engine
  (`discovery.Registry.Configured()`) can materialize MusicBrainz-only albums.
- **Single MusicBrainz client.** The musicbrainz plugin owns the process's one
  shared, rate-limited `*APIClient` (1 req/s pacer); prowlarr's private client is
  gone, so the metadata path and album-track resolution ride one bucket
  (`internal/providers/musicbrainz/provider.go`).

Phase 4 remains open: UI truthfulness (pending vs terminal) plus the remaining
doc drift (this change closes the discovery/MB drift — audit D1, D2, and the
plugin-list half of D3; `docs/flows/README.md` D5 and the DB-table half of D3
stay open).

> **Deploy-blocked:** the album-359 live verification cannot run yet — the instance at `192.168.1.178:8008` serves the pre-change binary and needs a redeploy plus a manual refresh to confirm.

### Related

- Audit appendix below; `docs/discovery.md`; `docs/flows/metadata-refresh.md`;
  session `.tmp/sessions/2026-10-12-metadata-materialization` (approved decisions 1–12).

---

## D-002 — Materialized catalog is the album-metadata source of truth

**Date**: 2026-10-12 (promoted to committed record 2026-10-13) · **Status**: Approved

Album provider metadata is materialized into durable SQLite tables
(`internal/catalog`), replacing the 24h JSON-blob caches. Reads never block on a
provider; freshness is sweep-free (lazy cold + event + opt-in bulk). Full
table set and the `refresh_status` lifecycle (`pending|fresh|stale|unmatched|failed`)
are in `docs/album-architecture.md` §"Metadata Catalog & Sweep-Free Refresh".

Provider I/O is **only** via `discovery.Provider` (in D-001 this becomes the
unified registry). Terminal `unmatched` retries on a 90-day interval.

---

## D-003 — Release resolution is a first-class provider capability

**Date**: 2026-07-29 (implicit) · **Status**: Approved (formalised 2026-10-13) · **Implemented** (2026-10-13)

`prowlarr` must resolve an album's expected tracklist from a torrent's **file
count** and title, which needs MBID-level release-group lookup
(`SearchReleaseGroup` → `SearchReleasesByGroup` → `LookupRelease` +
`pickBestMatchingRelease`). No existing interface exposed this. Historically
`prowlarr` imported the concrete `musicbrainz.APIClient` (`factory.go:11,36`).

**Decision**: model this as a named **`ReleaseResolver`** capability implemented
by MusicBrainz and consumed by `prowlarr` **through the registry**. The concrete
import and the private client are removed (see D-001 §6).

**Implemented (2026-10-13).** The capability is defined in
`internal/discovery/resolver.go` (`ReleaseResolver`, with
`ReleaseResolverConsumer` as the injection contract); the MusicBrainz `*Client`
implements it (`internal/providers/musicbrainz/provider.go`,
`var _ discovery.ReleaseResolver`); the composition root resolves one resolver
from the configured plugins and injects it (`injectReleaseResolver`,
`cmd/groovearr/app.go`); and `internal/api/handlers.go`
(`reinjectReleaseResolver`) re-injects the same instance into any plugin rebuilt
by a config update. `prowlarr` holds only a `discovery.ReleaseResolver` and
imports no sibling provider.

---

## D-004 — Artist canonical name via `metadata.ArtistNameProvider`

**Date**: 2026-07-13 (implicit) · **Status**: Approved (formalised 2026-10-13)

Case-only duplicate artists are merged by resolving a canonical spelling via
`metadata.ArtistNameProvider.CanonicalArtistName` — currently implemented **only
by MusicBrainz** (`musicbrainz/provider.go:110`). The de-dup scan
(`internal/jobs/duplicates.go`) is already cooldown-compliant and now orders
providers via `metadata.Order(ProviderOrder, Available())` — the single provider
order (D-001 §3) — falling back to registration order only when no order is set.

**Decision**: this path must (a) use the single provider order (D-001 §3) and
(b) keep consulting/marking the shared cooldown. Artist canonicalisation is a
MusicBrainz capability, not a separate flow.

---

## D-005 — `artist_discography` vs `tracked_albums` — kept separate

**Date**: 2026-10-12 · **Status**: Approved (supersedes the session master-plan
preference) · **Owner**: product owner

The catalog's `artist_discography` (display-only read-model) and tracking's
`tracked_albums` (monitored/wanted state machine) are **kept separate**.
Rationale: different concerns, lifetimes, and invalidation rules; unifying would
entangle the tracking state machine with the catalog read-model (full rationale
in `docs/album-architecture.md` §"Decision").

> **Supersedes**: session `2026-10-12-metadata-materialization` master-plan
> decision #12, which recorded a *preference* to "unify a single
> provider-release store". That preference is **not** adopted; shared value
> types may still be factored out later. A true unification would need its own
> design + migration.

---

## D-006 — Decision-record location

**Date**: 2026-10-13 · **Status**: Approved

`.tmp/sessions/**` and `.opencode/context/**` are **gitignored** — not committed,
not shared, not reviewable. Decisions recorded only there are not durable.
**Decision**: architectural decisions live in `docs/decisions.md` (this file).
`.tmp/sessions/**` is scratch only; the `project-intelligence` context files are
local aids, not the record.

---

## Audit appendix — provider & flow consistency (2026-10-13)

Scope: `docs/` (22 files), `AGENTS.md`, `.opencode/context/project-intelligence/*`,
`.tmp/sessions/*` (40), and the code behind each provider interface. No files
outside this appendix's own record were changed.

### Findings

| # | Finding | Evidence |
|---|---|---|
| F1 | Two provider registries with overlapping membership | `internal/metadata/registry.go`, `internal/discovery/registry.go`; capabilities in `deezer/factory.go:28`, `spotify/factory.go:51`, `tidal/factory.go:36`, `lastfm/factory.go:24`, `musicbrainz/factory.go:26`, `discogs/factory.go:27` |
| F2 | Three order sources | `metadata_order` (`config.go:25`, pre-rename snapshot) vs registration order in `Registry.Available()` (`registry.go:76`) and `discovery.Registry` |
| F3 | Two MusicBrainz clients; one is a provider→provider concrete import | `musicbrainz/factory.go` and `prowlarr/factory.go:11,36` |
| F4 | One handler mixes both registries | `handler_enrichment.go:209` (metadata order) vs `:315,:594` (`discoveryReg`) |
| F5 | `bestMatch` normalization is punctuation-blind | `refresh/service.go:920` + `normalize` (`:947`) = `ToLower(TrimSpace)` only |
| F6 | Albums list ignores `offset` | `handlers_library.go:123` `_ = offset`; `parsePagination` default limit 200 |
| F7 | Doc drift D1–D5 (below) | see table |
| F8 | Decisions not committed | `.gitignore:18` (`.opencode/`), `:25` (`.tmp/`) |

### Doc drift

| # | Doc | Drift |
|---|---|---|
| D1 | `docs/discovery.md:3` | Says "Draft — not yet implemented"; it **is** implemented (and is the catalog engine's backbone) — **Resolved 2026-10-13** (`provider-unification`) |
| D2 | `docs/discovery.md` | "MusicBrainz (future)" + Provider API Status (MB ❌) + Implementation Order step 11 never built — **Resolved 2026-10-13** (`provider-unification`) |
| D3 | `docs/architecture.md` | "Current plugins" lists only Soulseek+Deezer; DB table list omits catalog tables — **Partially resolved 2026-10-13** (`provider-unification`): Plugin System / Provider Order updated; DB-table list still open |
| D4 | `docs/flows/metadata-refresh.md` | **Resolved 2026-10-13** — the chart previously said the terminal branch enqueued `ReasonStale`; it now (correctly) states the branch does **not** re-enqueue (the parked queue row keeps its own `due_at`) |
| D5 | `docs/flows/README.md` | Self-documents 7 further drifts |

### Live verification (2026-10-13, instance `192.168.1.178:8008`)

Catalog status:

```
albums: fresh=4310  unmatched=1910  pending=0  failed=0
queue:  queued=1906  due_now=0       scheduled=1906
engine: running · 60s · batch 8
```

Random sample (seeded, 12 albums) — note: drawn from the first 200 rows because
`GET /api/library/albums` caps at 200 and **ignores `offset`** (F6). 9 matched /
3 unmatched. Two distinct failure modes:

| Album | Provider has it? | Cause |
|---|---|---|
| `#4327` R.E.M. — "2001 Fanclub Single" | MusicBrainz: **yes** (score 100); Deezer: no | **MB-only release** → confirms D-001 root cause |
| `#1226` DJ Antoine — `"WOW"` | Deezer: **yes** (`WOW`) | **Matcher false-negative** — library title is `"WOW"`; `normalize` keeps the quotes, so `"wow" != "wow"` (F5) |
| `#1229` Remady — `"WOW"` | ambiguous (MB loose match; Deezer no exact) | quote-title edge case (F5) |

**Conclusion**: the 1910 `unmatched` is a **mixture** of (a) genuine discovery-
provider coverage gaps (MB-only releases → D-001) and (b) matcher
false-negatives on punctuated/quoted titles (F5). Both must be fixed; D-001 alone
does not clear the set.

### Follow-ups

Status 2026-10-13 — the `library-metadata-fixes` change resolves F5, F6 and the
in-scope doc drift (D4); the `provider-unification` change (D-001 Phases 1–3)
resolves D1 and D2 and the plugin-list half of D3. D5 and the DB-table half of D3
remain open.

- **F5 — resolved.** Punctuation/quote/edition-insensitive matching in the shared
  matcher (`bestMatch` / `normalizeTitle`, `internal/refresh/service.go`).
- **F6 — resolved.** `handleLibraryAlbums` now honours `offset`
  (`internal/api/handlers_library.go`); `GET /api/library/albums` documented in
  `docs/api.md`.
- **F7/D4 — resolved.** `docs/flows/metadata-refresh.md` reconciled with the
  terminal branch (no re-enqueue).
- **D1/D2 — resolved (2026-10-13, `provider-unification`).** `docs/discovery.md`
  no longer says "Draft — not yet implemented"; MusicBrainz is documented as a
  first-class discovery provider and the Provider API Status table matches code.
- **D3 — partially resolved (2026-10-13, `provider-unification`).** The
  Plugin System / Provider Order sections in `docs/architecture.md` document the
  unified registry, `provider_order`, and the `ReleaseResolver` injection. The
  DB-table list still omits the catalog tables and stays open.
- **D5 — open.** `docs/flows/README.md` self-documents further drift.
- Re-run the sample against a **uniform** library slice now that F6 is fixed — the
  2026-10-13 live sample was drawn from the first 200 rows because the endpoint
  capped the page and ignored `offset`.

---

## D-007 — Structured discovery search contract + matcher consolidation (matcher-plugin deferred)

**Date**: 2026-10-14 · **Status**: Approved

### Context

The discovery contract `discovery.Provider.SearchAlbums(ctx, query string, limit)`
takes a pre-joined `"artist title"` string, discarding the artist/title boundary.
Callers hand it joined strings (`internal/refresh/service.go`,
`internal/api/handlers_discover.go`). MusicBrainz must
re-guess the split (`albumSearchCandidates`, `maxAlbumSearchArtistWords = 3`,
`internal/providers/musicbrainz/provider.go`), which mis-splits 4+ word artists
(`Red Hot Chili Peppers`) and titles containing `" - "` (`Échame la culpa - Single`).
Combined with single-release-group recall and abort-on-transient-error, MB-only
releases stay `unmatched` even when MusicBrainz has them (live evidence, build
`cdd932d`). Album-result matching is also duplicated — `bestMatch`/`normalizeTitle`
(`internal/refresh/service.go`) and `internal/catalog/merge.go` each re-implement
it — while `internal/matching` already exists as the shared home (AGENTS §3).

### Decision

1. **Structured query.** Replace the joined-string search with an `AlbumQuery`
   (explicit artist/title fields plus a free-text fallback for the Discover UI).
   Provider-specific query building stays inside the provider package (AGENTS §1);
   the generic matcher is **not** provider-specific.
2. **Consolidate matching.** Fold album-result matching (`NormalizeTitle`,
   `ArtistCompatible`, best-candidate selection) into one shared
   `internal/matching` path and delete the duplicated matcher bodies in
   `internal/refresh/service.go`. (`internal/catalog/merge.go`'s `normalize` is a
   *different* key — punctuation/feat/edition-preserving — and is intentionally
   kept, not folded.)

### Rationale

- Deterministic lookup: the artist/title boundary is carried, not re-guessed.
- One matching policy instead of three divergent copies (AGENTS §3).
- Provider isolation preserved: the contract is generic; only query building is
  per-provider.

### Alternatives Considered

| Alternative | Pros | Cons | Why rejected |
|---|---|---|---|
| **A — status quo (joined string)** | No interface change | Keeps the mis-split + duplication defects | Does not fix the observed `unmatched` cause |
| **B — structured `AlbumQuery` + shared matcher (chosen)** | Fixes boundary + duplication; small surface | Interface change across all discovery providers | Adopted |
| **C — matcher capability/plugin** | Pluggable acceptance policies | Premature; single implementation; matching is generic policy | Deferred — see below |

### Explicitly deferred — "matcher plugin" capability (Option C)

Do **not** build a matcher capability/plugin now. No observed failure mode is a
matcher-acceptance failure: the correct candidate is either never produced
(contract/recall) or the provider is unavailable. A capability/registry with a
single implementation is premature; matching is generic policy, not provider-owned
behavior, and `internal/matching` is already the shared home. Option C only makes
sense once ≥2 configured providers need genuinely different acceptance policies or
a cross-provider candidate-ranking need exists.

### Revisit triggers

- ≥2 configured providers requiring different acceptance policies.
- A cross-provider candidate-ranking requirement.
- Per-provider tolerance strategies.

### Impact

- **Positive**: deterministic lookup preserved; fewer MusicBrainz requests; one
  matching policy; AGENTS-compliant (§1, §3).
- **Negative / risk**: interface change across all discovery providers
  (mechanical); behavior change in edge titles — mitigate with regression tests.

### Phases

| Phase | Work | Status |
|---|---|---|
| 0 | This decision record | Done (2026-10-14) |
| 1 | Introduce `AlbumQuery`; migrate all discovery providers | Done (2026-10-14, task 08) |
| 2 | Consolidate album-result matching into `internal/matching`; delete duplicates | Done (2026-10-14, task 08) |
| 3 | Regression tests for mis-split artists/titles | Done (`internal/matching/match_test.go`, `internal/providers/musicbrainz/provider_discovery_test.go`) |

### Resolved (2026-10-14)

The duplicated MusicBrainz split heuristic named in **Context**
(`albumSearchCandidates`, `maxAlbumSearchArtistWords = 3`) was removed in task 08:
the structured path now carries the artist/title boundary and issues one
release-group request, and the free-text path is a single bare query
(`internal/providers/musicbrainz/provider.go`, `api.go`). Album-result matching
was consolidated into `internal/matching` (`NormalizeTitle`, `ArtistCompatible`,
`BestMatch`); `internal/refresh` delegates to it: the duplicated matcher bodies
were removed and `refresh.bestMatch` is now a thin adapter over
`matching.BestMatch`. Phase 1
(`AlbumQuery`) and Phase 2 (matcher consolidation) are done; Phase 3 regression
tests are in place. This note records the resolution without rewriting the
Context/Decision history above.

### Deferred (with reason)

Review nits deliberately not actioned in task 08:

- **Release group's `ArtistName` preferred over the search artist** —
  `albumResultFromReleaseGroup` keeps the release group's own artist credit and
  falls back to the searched artist only when the credit is empty
  (`internal/providers/musicbrainz/provider.go`). Kept intentionally: a tribute or
  split release group should surface its real credit, not the query artist.
- **`time.After` reuse in the MusicBrainz pacer** — the pacer waits on
  `time.After(wait)` (`internal/providers/musicbrainz/api.go:625`); a reusable
  `time.Timer` would avoid a per-wait allocation. Perf nit only, no correctness
  impact.
- **`internal/matching/engine.go` provider-name comment** — the doc comment names
  `(Spotify, iTunes, etc.)` (`engine.go:15`). Pre-existing and unrelated to this
  change (iTunes is not a provider).

### Audit note — one MusicBrainz client (pacing audit, D2)

One `musicbrainz.APIClient` is live at a time and shared by catalog refresh,
enrichment, health check and release-resolve through the one `plugin.Registry`
(`internal/providers/musicbrainz/factory.go` builds one `*Client`, which owns one
`*APIClient`; `cmd/groovearr/app.go` wires that one registry). Every path
therefore serializes through one MusicBrainz token bucket (1 req/s; paced at
1100ms for margin) and one shared cooldown (AGENTS §8). A config hot-reload
recreates the plugin (`plugin.Registry.Rebuild` → `factory.Create`), replacing
the `*APIClient` and resetting its pacer bucket; the cooldown bucket is separate
and survives the rebuild. No code change; recorded to close the pacing audit's
D2 finding.

## D-008 — Identity-first unmatched resolution (Phase A)

### Context

Album-title search leaves a stable unmatched set: 223 albums / 495 tracks on the
live library, almost all with a Spotify track id and/or ISRC. The album string
is often a local grouping artifact (1-track pseudo-albums, quoted titles,
compilation artists), so re-running `BestMatch` on it cannot recover them.
`matching.CandidateScore` already defined the confidence bands (auto ≥75,
review 50–74, local-only <50) and the known-duration-conflict cap, but nothing
consumed it.

### Decision

Resolve parked unmatched albums from **track identity**, not more album-string
matching:

- An optional `discovery.TrackIdentityProvider` capability resolves one track by
  provider id (`GetTrackIdentity`) or ISRC (`LookupTrackByISRC`). Deezer and
  Spotify implement it; core packages reference only the interface.
- `internal/identity` probes duration only for the parked albums' tracks, scores
  each candidate with `matching.CandidateScore`, and auto-applies only when the
  title agrees and either artist or duration confirms the recording; a known
  duration conflict always parks.
- A 1-track local release materializes exactly one provider row. It is marked
  `album_metadata.partial = 1` only when the provider release is known to have
  more than one track (a true single, or an unknown count, stays refreshable).
  A multi-track album materializes the full provider release only when every
  local track maps to the same provider album and every candidate is
  high-confidence. Ambiguous/wrong-ISRC/conflicting candidates stay parked for
  the review UI.
- The user-triggered `POST /api/jobs/recheck-unmatched` runs the resolver first;
  the old re-arm path remains as a fallback when no resolver is wired or no
  configured provider exposes the identity capability.

### Consequences

- Positive: high-confidence identities are used directly; wrong ISRCs cannot
  auto-apply on ID alone; singles/EPs are valid releases; duration is probed
  only for the backlog (495 tracks), not the 33k library.
- Positive: one shared provider cooldown still governs every lookup; partial
  materializations are never expanded by the refresh engine.
- Deferred: the review UI for 50–74 confident candidates (Phase B) and a
  targeted "play local track / choose candidate" flow.
