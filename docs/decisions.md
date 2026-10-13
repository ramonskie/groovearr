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
3. **One order source.** `metadata_order` is promoted to the single provider
   order for **every** path (enrichment, queue-time resolve, artist de-dup,
   discover, catalog, tracking). Registration-order selection is retired.
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
- **One order source.** `provider_order` is canonical with a fallback to the
  legacy `metadata_order` (`Config.EffectiveProviderOrder`,
  `internal/config/config.go`). One `metadata.ProviderOrder`
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
| F2 | Three order sources | `metadata_order` (`config.go:25`) vs registration order in `Registry.Available()` (`registry.go:76`) and `discovery.Registry` |
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
