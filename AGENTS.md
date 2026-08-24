# AGENTS.md

Groovearr — Go backend + React SPA music download manager with a plugin
architecture. These rules are the architectural contract for every change.
The docs referenced below are the source of truth for how the flows work;
code is authoritative over any diagram.

---

## 1. Provider Isolation

Providers live **only** in `internal/providers/<name>/` and talk to the rest
of the app exclusively through the interfaces in `internal/plugin/` and the
per-domain interfaces (`download`, `playlist`, `metadata`, `discovery`).

**Forbidden in core packages** (`config`, `domain`, `download`, `library`,
`api`, `jobs`, `matching`, `metadata`, `playlist`, `plugin`):

- Provider names in defaults or hardcoded lists — **one sanctioned
  exception**: the first-run bootstrap `metadata_order`/`download_order`
  seed in `config.DefaultConfig()`. It exists only so discovery, search,
  and downloads work out of the box on first setup; the user's config can
  override it at any time and runtime provider behavior never depends on
  it. Everything else provider-specific stays out of core config.
- Provider-specific string keys or behavior in generic handlers
  (e.g. slskd `@@user/` workarounds in `FileRenamerHandler`,
  `musicbrainz_release` key checks in the enrichment handler)
- Core packages implementing provider-defined interfaces
  (dependency inversion — `library.Renamer` must not implement a
  deezer-specific interface; define the interface in the domain it serves)
- Provider-specific matching/parsing logic outside the provider package

**Where provider behavior belongs:**

- Provider defaults → the provider's `factory.go`
- Provider config → `Sources map[string]json.RawMessage`, decoded by the
  provider's own config struct (see `docs/plugins.md`)
- Provider workarounds → inside the provider package; expose the corrected
  result through the interface, not the workaround

New provider = one directory in `internal/providers/` + a factory registered
in `cmd/groovearr/main.go`. Follow `docs/plugins.md` (walkthrough: Tidal).
No changes to core packages should be required to add a provider.

## 2. Flow Discipline

Never write code around an existing flow. Every operation has one canonical
path:

| Operation | Canonical flow | Where |
|---|---|---|
| Background jobs (scan, enrich, duplicates, organize, repair, sync) | `jobs.Manager.Start` with a `jobs.Runner` from `jobs.Runners` | `internal/jobs/`, `internal/api/handlers_jobs.go` |
| Post-download import | 7-handler import chain wired in `app.go` | `docs/flows/import-handler-chain.md` |
| Download lifecycle | State machine, monitor tick loop, `TransitionState` atomic transitions | `docs/flows/download-state-machine.md`, `docs/flows/monitoring-service.md` |
| Album import | `AlbumImportHandler` feeding per-track records through the same chain | `docs/flows/album-import-handler.md` |
| Metadata | Provider registry + resolver with order/cooldown | `docs/flows/metadata-enrichment.md` |

**Hard rules:**

- No ad-hoc `go func()` background work in HTTP handlers. All
  user-triggered background work goes through the job Manager
  (single-flight, SSE progress, cancel, persisted/restored on restart).
  Service-owned loops may run background work but own their lifecycle
  (e.g. `plugin.HealthChecker.RequestCheck` pokes the checker's own loop —
  never a raw goroutine in a handler).
- Download state changes go through store methods only — never direct SQL
  state writes. Atomic transitions use `TransitionState(old, new)`.
- Post-download steps are handlers in the chain, not code added to the
  monitor or service.
- When you change a flow, update the matching `docs/flows/*.md` chart in
  the same change (they are code-verified; don't leave drift).

## 3. Layering & Dependencies

- **Interfaces over concrete types.** Stores are defined as interfaces in the
  package root (`library.Store`, `download.Store`) with SQLite impls in a
  `sqlite/` subpackage. Wire dependencies in `cmd/groovearr/app.go`.
- **No type assertions to concrete impls from higher layers.** The
  `s.store.(sqlDBProvider)` pattern in the api package is forbidden — add a
  method to the interface instead.
- **Package layout** (see `docs/development.md`):
  - `domain/` — pure types, no behavior
  - `<feature>/` — `store.go` (interface), `service.go` (logic), tests
  - `<feature>/sqlite/` — SQLite impl
  - `<feature>/<source>/` — plugin impls
- **File size**: split files that grow beyond a single focused concern
  (~500 lines); handlers already split per responsibility (`handler_*.go`).

## 4. HTTP Handlers

Handlers are thin. `parse/validate → call service → writeJSON/writeError`.
No business logic, no goroutines, no direct store access that a service
should own.

- Use `writeJSON` / `writeError` helpers; never hand-roll JSON or status codes.
- New endpoints register through the router in `internal/api/handlers.go`.
- API surface documented in `docs/api.md` — update it when adding/renaming
  endpoints.

## 5. Data & State

- SQLite schema is **additive only**: `CREATE TABLE IF NOT EXISTS` and
  `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` in `initSchema()`
  (`internal/library/sqlite/store.go`). Never break existing databases.
- No migration framework by design — keep it that way unless explicitly
  agreed with the user.
- All state mutations go through store methods so monitoring, events, and
  retry logic stay consistent.

## 6. Communication

- Cross-component notifications go through `events.IEventAggregator`
  (`internal/events/`). Subscribe; don't call across packages directly.
- All UI progress/updates go through `sse.SSEHub` (`internal/sse/`).
  Job snapshots, download events, logs broadcast via SSE topics.

## 7. Concurrency & Context

- Every I/O operation takes `context.Context` as first parameter with a
  per-operation timeout. No unbounded goroutines.
- Follow existing patterns: `safeTick` watchdog (30s) in the monitoring
  service, per-provider download semaphores, one job at a time in the
  Manager.
- Long-running loops must be cancellable via context and must not block
  shutdown (`Shutdown()` in `app.go`).

## 8. Rate Limiting (Shared Provider Cooldown)

One shared per-provider rate-limit bucket for the whole app: a single
`metadata.ProviderCooldown` instance wired in `cmd/groovearr/app.go` and
injected into every path that calls a provider (enrichment handler, API
server, health checker, jobs). No caller keeps its own per-path rate-limit
state, and no code goes around the bucket.

- **Before calling a provider**, background/long-running paths check
  `CoolingDown(name)` and skip providers currently cooling down.
- **On `metadata.ErrRateLimited`**, mark the shared bucket with
  `MarkAfter(name, retryAfter)`. Server-requested `Retry-After` is honored
  (with post-ban grace, capped 4h); guesses escalate circuit-breaker
  style; marks persist to SQLite so a long backoff survives a restart.
- **Why one bucket**: it keeps one task from breaking another. The moment
  any caller observes a 429, the provider is parked app-wide — so a bulk
  enrich job hammering a provider can't push a concurrent user-facing
  search into the same throttle, and nobody re-probes a throttled API to
  re-arm a longer ban.
- Steady-state pacing belongs in the provider's own HTTP client via
  `ratelimit.NewRateLimitedTransport` (per-provider req/sec, see
  `docs/providers-rate-limits.md`); the cooldown is the shared *reactive*
  enforcement on top.
- The health checker consumes the same bucket — it skips cooling-down
  providers and re-marks when its own probe is rate-limited.
- Background jobs consult and mark the same bucket too: `jobs.Runners`
  receives the shared `RateLimit` via `RunnerDeps`/`SetRateLimiter`
  (wired from `Server.SetProviderCooldown`), so the duplicates canonical
  lookups skip cooling providers and park one on `ErrRateLimited` like
  every other path.

## 9. Config

- Runtime config is thread-safe JSON with hot-reload (`internal/config`,
  `Persistence` + `Update(func)`). Read live config via the `func()
  config.Config` closure — never cache config values in structs.
- Plugin config lives under `sources.<name>` as `json.RawMessage`; each
  plugin decodes its own struct. Core config must not know provider fields.
- Sensitive fields (ARL, API keys, passwords) are masked in config output
  and redacted from logs.
- Provider names in `DefaultConfig()` limited to the first-run order seed
  (rule 1).

## 10. Errors & Logging

- Return `(result, error)`; wrap with `fmt.Errorf("context: %w", err)`.
  Panic only in `main()` for fatal startup errors.
- Store "not found" returns `nil, nil`, not an error.
- Event bus and SSE handlers recover panics and log them.
- Structured logging via `log/slog` with `component` attribute.
  Use `internal/logger`.

## 11. Testing

- Table-driven tests, located next to the code (`*_test.go`), using
  interface mocks — never a real network or a real sqlite DB in unit tests
  unless it's an integration test (`*_integration_test.go`).
- Every new service/handler/job/plugin ships with tests.
- Run `make test` (or `go test ./...`) before handing off. Broken tests
  are blocking.

## 12. UI

- React SPA in `ui/`. Feature code in `ui/src/features/<name>/`, shared
  state in `ui/src/stores/`, hooks in `ui/src/hooks/`.
- **All HTTP goes through `ui/src/api/client.ts`** — no raw `fetch()` in
  components or contexts (auth requests included: `login`/`logout`/config
  all live in the client).
- Server events via `EventSource` hooks (`use-download-events.ts`,
  `use-logs.ts`) — don't poll where SSE exists.
- No provider-specific UI code: the backend's `ConfigSchemaProvider`
  manifest drives settings cards, OAuth buttons, and import patterns
  (see `docs/plugins.md`).

## 13. Documentation

- `docs/` holds architecture, flow charts, API reference, plugin guide.
  Update the relevant doc in the same change as the code.
- `docs/flows/*.md` flow charts are code-verified; the files listed at the
  top of each chart are the source of truth. If a chart drifts from code,
  fix the chart (and flag any real flow change).
- Ask before committing. Never commit without explicit consent.
