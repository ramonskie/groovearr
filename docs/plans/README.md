# Groovearr Plans

Planning docs for upcoming features. Each plan is self-contained and follows the
repo's task-breakdown format (overview → prerequisites → phased tasks with
files/estimates/verification → testing → estimate).

| Plan | Covers | Depends on |
|---|---|---|
| [multi-user-roles.md](multi-user-roles.md) | Multiple accounts with `admin` / `user` roles; admin-only `/settings` surface; shared read-only view of the single download queue | — |
| [library-downloads.md](library-downloads.md) | Download a track file or an album zip from the shared library to a client | multi-user-roles (identity is required for audit; can ship after) |

## Locked decisions (from product owner)

1. **Users exist only to separate admin vs user.** No per-user ownership of
   playlists, albums, songs, library files, or downloads. "Add playlists /
   albums / songs" flows stay exactly as they are today — global and shared.
   The one exception is **attribution** (decision 6): a DB-only record of who
   requested each item.
2. **Admin-only = everything under `/settings`:** config + setup, jobs,
   tracking, quality profiles, logs, rate-limits, user management, debug.
   The global API key is an admin token.
3. **Downloads are shared.** One queue (single rate-limit bucket). Regular
   users see active **and** completed downloads, and may **cancel / retry**.
   They may also start downloads/acquire (search, discover), use playlists,
   browse the library, and download library files.
4. **No new tables** except `users` (plus an optional download-audit table in
   `library-downloads.md`). Additive **attribution columns** only:
   `requested_by_user_id` on `downloads`, and
   `added_by_user_id` + `added_by_username` on `tracks`, `albums`, `playlists`.
5. **Plan location:** `docs/plans/`.
6. **Attribution is DB-only and display/audit only.** It records who requested
   a download and who imported a playlist; scanner/background items have none.
   It is **never written to ID3/audio tags** — the `internal/tagging` path is
   untouched. Reserved for a future "show only my requested albums" filter; it
   grants no authorization and creates no per-user rows.

## Security findings folded into the plans

Discovered while validating the first draft against the code. Each maps to a
task in the plans.

| # | Finding | Plan / task |
|---|---|---|
| C1 | `Config.Mask()` does **not** mask `Auth.APIKey`; `GET /api/config` returns the raw admin key, which the SPA stores and sends on every request | multi-user Phase 0.1 / 0.2 |
| C2 | SPA auth check calls admin-only `GET /api/config`; a regular user gets 403 → client only redirects on 401 → login loop on every refresh | multi-user Phase 0.3 |
| C3 | `/api/events` broadcasts `log_line` to every SSE subscriber with no role filter | multi-user Phase 7 |
| C4 | `local_bypass_subnets` silently becomes an admin grant when mapped to identity | multi-user Phase 2.3 |
| H1 | App-wide UI calls admin endpoints (`useJobWatcher` → `GET /api/jobs`) | multi-user Phase 8.1 |
| H2 | No session invalidation when a user is disabled / password changed / deleted | multi-user Phase 5 |
| H3 | `auth.method="basic"` is a phantom (no `r.BasicAuth()`, login rejects non-forms) | multi-user Phase 0.4 |
| H4 | `WriteTimeout: 30s` cuts large album zips mid-stream | library-downloads Phase 1.4 |
| H5 | No audio MIME fallback (`.flac/.mp3/.opus/.m4a`) when `/etc/mime.types` is absent | library-downloads Phase 1.2 |
| M1 | SSE job events also reach non-admins | multi-user Phase 7 |
| M7 | Album zip entry names can collide; multi-disc needs disc prefix | library-downloads Phase 2.1 |
| M8 | `withAccessLog` carries no username → no per-user download audit | library-downloads Phase 3.2 |

## Build order

```
multi-user-roles
  ├─ 0. auth hardening (mask API key, cookie-only SPA, /api/me)   ← FIRST
  ├─ 1. user store + users table + bootstrap admin
  ├─ 2. session/request identity + role middleware
  ├─ 3. route gating (settings baseline)
  ├─ 4. login against user store
  ├─ 5. session invalidation on user change
  ├─ 6. user-management API
  ├─ 7. role-scoped SSE (log_line / job → admin only)
  ├─ 8. UI role gating + users screen
  ├─ 9. item attribution (who requested what — DB only, no ID3)
  └─ 10. docs
        │
        ▼
library-downloads
  ├─ 1. track stream (+ path guard, MIME map, write-deadline)
  ├─ 2. album zip (+ dedupe/disc, cover, existing m3u)
  ├─ 3. authz / rate limit / audit (username)
  └─ 4. UI download actions
```

Rule references: `AGENTS.md` §§1 (provider isolation), 3 (layering), 4 (thin
handlers), 5 (additive schema), 8 (rate limiting), 10 (errors/logging),
11 (testing), 12 (UI all HTTP via `ui/src/api/client.ts`).
