# Plan: Multi-User Roles (admin / user)

## Overview

Add multiple user accounts so the app can distinguish **admin** from **user**.
The boundary is the **`/settings` surface**: everything under settings (config,
setup, jobs, tracking, quality profiles, logs, rate-limits, user management) is
admin-only. Everything else — library, playlists, search, discover, the single
shared download queue (view / start / cancel / retry), and library file
downloads — is available to every authenticated user. Playlists, albums, songs,
downloads and the library remain shared/global. The one addition is **DB-only
attribution** (Phase 9): which user requested each item, for display and a
future "my requests" filter — never written to ID3/audio tags.

This plan folds in the security findings from the plan review; see
[README.md](README.md#security-findings-folded-into-the-plans) for the mapping.

## Current state (grounding)

- **Auth is single-user.** `internal/api/auth.go` validates one configured login
  (`cfg.Auth.Username` + bcrypt `cfg.Auth.Password`) and a global
  `cfg.Auth.APIKey`. `internal/api/auth.go:131-132`.
- **Sessions are in-memory and carry only a username string.**
  `internal/api/session.go` (`sessionStore`, `session{Username}`); the username
  is currently unused (`auth.go:47` — "available for future auditing").
- **One middleware wraps the whole mux:** `withAuth(mux)` at
  `internal/api/handlers.go:258`; `withAuth` is defined in `auth.go:22`.
- **Auth config:** `internal/config/config.go:59` (`AuthConfig`), validated at
  `config.go:254-268`; `HashPassword` / `CheckPassword` / `GenerateAPIKey` in
  `internal/config/password.go`.
- **DB handle available to feature stores:** `libStore.DB()` is already used by
  `internal/metadata/sqlite` (`cmd/groovearr/app.go:126`) and
  `internal/tracking/sqlite` (`app.go:265`), which each own their tables in
  `init()`. `internal/user/sqlite` should follow that precedent.
- **Router and server struct:** `internal/api/handlers.go` (`Server`, lines
  36-68; `NewServer`, line 74; route registration, lines 147-241).
- **UI:** `ui/src/context/AuthContext.tsx`, `ui/src/App.tsx` (routes, 145-156),
  `ui/src/components/SidebarNav.tsx` (`DEFAULT_PAGES`, 35-41), `ui/src/api/client.ts`
  (`X-Api-Key` attached on every request, lines 69-76).

### Findings that change this plan

- **C1 — API key leaked in cleartext.** `Config.Mask()` (`config.go:526`) masks
  password + source secrets but **not `Auth.APIKey`**; `handleGetConfig`
  (`handlers.go:484`) returns `Mask()`. `client.ts:69` sends the stored key as
  `X-Api-Key` on every request; SSE hooks pass `?apikey=` (`use-logs.ts:70`,
  `use-tracking-events.ts:39`).
- **C2 — auth check uses an admin-only endpoint.** `AuthContext.checkAuth()`
  calls `getConfig()` (`AuthContext.tsx:57`); a regular user then gets 403, but
  `request()` only redirects on **401** (`client.ts:80`) and throws → login loop.
- **C3 — SSE unfiltered.** The log tailer broadcasts `log_line` via
  `sseHub.Broadcast` (`app.go:243-253`); `SSEHub.Broadcast` fans out to every
  client (`sse/hub.go:83`). `jobs/manager.go:236` likewise broadcasts job events.
- **H3 — `basic` method is a phantom.** `withAuth` never calls `r.BasicAuth()`;
  `handleLogin` accepts only `forms` (`auth.go:115`).

### Attribution create-paths (grounding)

Which code creates the rows record can attach to:

- **System (no user):** `library/scanner.go:579` (`ImportTrack`), `library/organizer.go`,
  `download/handler_enrichment.go` (enrichment job). Auto playlist sync
  (`playlist/service.go:531`) is background too.
- **Download-driven (user = requester):** `download/handler_library.go:81`,
  `download/handler_album_import.go`.
- **Playlist import (user = importer):** `playlist/service.go:784`.

The download `Record` (`download/types.go`) currently has **no requester field**,
and `Queue` / `QueueAlbum` / `QueueAlbumWithFallback` / `QueuePending`
(`download/service.go`) take no actor. Attribution must be threaded
handler → record → import handler.

Additive-column migration pattern: `library/sqlite/store.go:328` `migrations`
list (`ALTER TABLE ... ADD COLUMN`, tolerating `duplicate column name`).

## Goals / Non-goals

**Goals**
- Multiple accounts, each with a role (`admin` | `user`).
- DB-only attribution of who requested each track/album (via the download queue)
  and who imported each playlist — display/audit only, never written to tags.
- Admin-only: everything under `/settings` (config, setup, jobs, tracking,
  quality profiles, logs, rate-limits, users, debug).
- Shared download visibility: users see active + completed downloads and may
  start / cancel / retry.
- Global API key remains an **admin** credential, and is no longer exposed to
  the browser.
- Bootstrap: existing `cfg.Auth` credentials become the first admin.
- Backwards compatible: `auth.method = "none"` keeps working (treated as admin).

**Non-goals**
- Per-user playlists/albums/songs/downloads, ownership columns, per-user queues
  (attribution is a record only — no authorization, no ownership).
- Writing attribution into ID3/audio tags or filenames (DB-only).
- OAuth / external identity providers.
- Per-user API keys, per-user download quotas.
- Changing any existing playlist/library/download behavior.

## Role matrix

Admin boundary = **everything under `/settings`**.

| Surface | admin | user |
|---|---|---|
| `GET/PUT /api/config*`, `/api/config/sources`, `/api/config/test/*` | ✅ | ❌ |
| `/api/setup/*` mutations (keep `GET /api/setup/status` open) | ✅ | ❌ |
| `/api/jobs*` | ✅ | ❌ |
| `/api/tracking/*` | ✅ | ❌ |
| `/api/quality-profiles/*` | ✅ | ❌ |
| `/api/users/*` (new) | ✅ | ❌ |
| `/api/logs`, `/api/rate-limits`, `/api/debug/*` | ✅ | ❌ |
| `/api/library/*`, `/api/covers/*`, `/api/artist-image/*` | ✅ | ✅ |
| `/api/playlists/*` | ✅ | ✅ |
| `/api/search`, `/api/discover/*`, `/api/albums/*`, `/api/download*` | ✅ | ✅ |
| `/api/downloads*` (list / start / cancel / retry) — **shared queue** | ✅ | ✅ |
| new `/api/library/.../download` (library files) | ✅ | ✅ |
| `/api/me`, `/api/login`, `/api/logout`, `/api/health` | ✅ | ✅ |
| `/api/events` — download + import topics | ✅ | ✅ |
| `/api/events` — `log_line` + job topics | ✅ | ❌ |

> `auth.method = "none"`: no sessions → every request is `admin` (today's
> behavior). Document prominently; it is not access control.

## Architecture

```
                       ┌───────────────────────────────┐
  cookie (session) ───▶│ withAuth                      │
  api key            ─▶│  resolves Identity            │──▶ ctx(identity)
  local bypass ──────▶│  {UserID, Username, Role,      │
  method none ───────▶│   ViaAPIKey}                   │
                       └───────────────┬───────────────┘
                                       │
                        ┌──────────────┴───────────────┐
                        │ mux (routes)                 │
                        │  /settings routes → adminOnly│──▶ 403 if role != admin
                        │  shared routes → as-is       │
                        └──────────────────────────────┘

  SSE: Register(client, role) → Broadcast skips
       log_line + job_* for role != admin
```

New package `internal/user`:

```go
// internal/user/store.go
package user

type Role string

const (
    RoleAdmin Role = "admin"
    RoleUser  Role = "user"
)

type User struct {
    ID           int64
    Username     string
    PasswordHash string
    Role         Role
    Disabled     bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type Store interface {
    CreateUser(ctx context.Context, u *User) (int64, error)
    GetUser(ctx context.Context, id int64) (*User, error)
    GetUserByUsername(ctx context.Context, username string) (*User, error)
    ListUsers(ctx context.Context) ([]User, error)
    UpdateUser(ctx context.Context, u *User) error
    DeleteUser(ctx context.Context, id int64) error
    CountUsers(ctx context.Context) (int, error)
}
```

- SQLite impl: `internal/user/sqlite/store.go`, constructed with
  `libStore.DB()`; owns its `users` table in `init()` (same pattern as
  `internal/metadata/sqlite` / `internal/tracking/sqlite`).
- Password hashing reuses `config.HashPassword` / `config.CheckPassword`.

## Tasks

### Phase 0: Auth hardening (prerequisite — must land before roles)
**Goal:** Stop leaking the admin credential and fix the SPA auth check.

- [ ] **0.1 (C1) Mask the API key.** In `Config.Mask()` (`config.go:526`), mask
  `Auth.APIKey` the same way as `Auth.Password`, and add a derived boolean
  (`Auth.HasAPIKey`) so config output can still tell the UI a key exists.
  `GET /api/config` must never return the raw key. Add a config test asserting
  the key is absent from `Mask()` output.
  - **Files:** `internal/config/config.go`, `internal/config/config_test.go`
  - **Estimate:** 45m
  - **Verification:** `Mask()` output contains no key; existing consumers build.

- [ ] **0.2 (C1) Stop the SPA depending on the key.** Remove API-key storage +
  the automatic `X-Api-Key` header (`client.ts:69-76`), and the `?apikey=`
  query in `use-logs.ts:70` / `use-tracking-events.ts:39`. Same-origin fetch and
  EventSource send the session cookie automatically.
  - **Files:** `ui/src/api/client.ts`, `ui/src/context/AuthContext.tsx`,
    `ui/src/hooks/use-logs.ts`, `ui/src/hooks/use-tracking-events.ts`
  - **Estimate:** 45m
  - **Dependencies:** 0.1
  - **Verification:** SPA works on session cookie alone; SSE connects.

- [ ] **0.3 (C2) Add `GET /api/me` and switch the auth check to it.** Response:
  `{username, role, via_api_key}`. In `AuthContext.checkAuth`, replace
  `getConfig()` with `getMe()`; treat 401 as unauthenticated and 403 as a normal
  authenticated error (never a redirect). `/api/me` must not echo the API key.
  - **Files:** `internal/api/auth.go`, `internal/api/handlers.go`,
    `ui/src/api/client.ts`, `ui/src/api/types.ts`,
    `ui/src/context/AuthContext.tsx`
  - **Estimate:** 1h
  - **Dependencies:** 0.1
  - **Verification:** admin/user/`none` all resolve a role; no refresh loop.

- [ ] **0.4 (H3) Retire `auth.method="basic"`.** It has no login path today.
  Either drop it from the accepted methods (`config.go:256`) or implement real
  HTTP Basic; dropping is recommended. Update validation + docs.
  - **Files:** `internal/config/config.go`, `docs/setup.md`
  - **Estimate:** 30m
  - **Verification:** validation rejects `basic` (or Basic works end-to-end).

### Phase 1: User store + bootstrap
**Goal:** Persistent user records + first admin.

- [ ] **1.1** `internal/user/store.go` (types + `Store` interface above).
  - **Estimate:** 30m
  - **Verification:** compiles; documented.

- [ ] **1.2** `internal/user/sqlite/store.go` implementing `Store`; owns the
  table in `init()`:
  ```sql
  CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user',
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
  );
  CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username COLLATE NOCASE);
  ```
  Username uniqueness is case-insensitive via `COLLATE NOCASE`; trim on write.
  - **Files:** `internal/user/sqlite/store.go`
  - **Estimate:** 1.5h
  - **Dependencies:** 1.1
  - **Verification:** unit tests: create/get/list/update/delete, duplicate
    (incl. case-variant) rejected.

- [ ] **1.3** `internal/user/service.go` `EnsureBootstrapAdmin`: if `users` is
  empty and `cfg.Auth.Method` is `forms` with a configured username, seed one
  admin from `cfg.Auth.Username` + already-hashed `cfg.Auth.Password`. No seed
  when method is `none`. Wire in `cmd/groovearr/app.go`.
  - **Estimate:** 1h
  - **Dependencies:** 1.2
  - **Verification:** first start seeds exactly one admin; second is a no-op.

### Phase 2: Identity + request context
**Goal:** Every request knows caller + role.

- [ ] **2.1** Extend `session`/`sessionStore` (`session.go`) to carry
  `UserID int64`, `Role user.Role`; add `Create(user)`, `Validate(token) (session, bool)`,
  and `DeleteByUserID(userID)` (used in Phase 5).
  - **Files:** `internal/api/session.go`
  - **Estimate:** 45m
  - **Dependencies:** Phase 1
  - **Verification:** role round-trips; expiry + reaper unchanged; DeleteByUserID test.

- [ ] **2.2** `internal/api/identity.go`: `Identity{UserID, Username, Role, ViaAPIKey}`
  + `identityFrom(ctx)` / `contextWithIdentity`.
  - **Estimate:** 30m
  - **Verification:** set/get test.

- [ ] **2.3** Update `withAuth` (`auth.go`) to inject `Identity`:
  - method `none` → `{Role: admin}` (backwards compat);
  - valid session → identity from session;
  - API key (any transport) → `{Role: admin, ViaAPIKey: true}`;
  - **local bypass → `{Role: RoleUser}` (C4 — safe default; a LAN host must not
    silently become admin).** Note that this is a behavior change from today
    (bypass previously meant full access); document it.
  - unauthenticated `/api/*` → 401 as today.
  - **Files:** `internal/api/auth.go`
  - **Estimate:** 1.5h
  - **Dependencies:** 2.1, 2.2
  - **Verification:** tests for each transport + each role; bypass yields `user`.

- [ ] **2.4** `adminOnly(h http.HandlerFunc) http.HandlerFunc` → 403
  `{"error":"forbidden"}` when role != admin.
  - **Files:** `internal/api/auth.go` / `identity.go`
  - **Estimate:** 30m
  - **Dependencies:** 2.2
  - **Verification:** 403 for user, pass-through for admin.

### Phase 3: Route gating
**Goal:** Enforce the matrix at the router.

- [ ] **3.1** Wrap the **settings surface** with `s.adminOnly(...)` in
  `handlers.go`: config (≈153-159), setup mutations (keep `GET /api/setup/status`
  open), jobs (180-187), tracking (202-212), quality-profiles (224-231),
  logs (238), rate-limits (148-149), debug (241), and the new `/api/users/*`.
  Do **not** wrap library / playlists / search / discover / downloads.
  - **Files:** `internal/api/handlers.go`
  - **Estimate:** 1.5h
  - **Dependencies:** Phase 2
  - **Verification:** table-driven test per group: user → 403, admin → expected.

- [ ] **3.2** Register `GET /api/me` (from 0.3) and keep `POST /api/login`,
  `/api/logout` open.
  - **Files:** `internal/api/handlers.go`
  - **Estimate:** 15m

### Phase 4: Login against the user store
- [ ] **4.1** Rewrite `handleLogin` (`auth.go:106`) to fetch via
  `GetUserByUsername`, reject disabled, verify bcrypt, then
  `sessions.Create(u)`. Keep the `groovearr_sid` cookie + rate-limited route.
  - **Files:** `internal/api/auth.go`
  - **Estimate:** 1h
  - **Dependencies:** Phase 1, 2.1
  - **Verification:** admin/user login; wrong password; disabled; unknown.

- [ ] **4.2** `cfg.Auth.Username/Password` becomes bootstrap-only; document in
  `docs/setup.md`.
  - **Estimate:** 30m
  - **Verification:** changing config creds post-bootstrap does not change login.

### Phase 5: Session invalidation (H2)
**Goal:** Disabled / password-changed / deleted users lose access immediately.

- [ ] **5.1** Call `sessions.DeleteByUserID(id)` from user update (password or
  disabled change) and delete handlers. Note the user's own current session is
  re-created or they are logged out — decide and document (logout on self
  password change is acceptable).
  - **Files:** `internal/api/handlers_users.go`, `internal/api/session.go`
  - **Estimate:** 45m
  - **Dependencies:** Phase 2.1, Phase 6
  - **Verification:** disabling a logged-in user invalidates their next request.

### Phase 6: User-management API
**Goal:** Admin-only CRUD.

- [ ] **6.1** `internal/api/handlers_users.go`:
  - `GET /api/users` (never return `password_hash`)
  - `POST /api/users` `{username, password, role}`
  - `PATCH /api/users/{id}` `{role?, disabled?, password?}`
  - `DELETE /api/users/{id}`
  Guards: last-admin protection + self-delete prevention **inside a single
  transaction** (avoid concurrent-demotion race); role ∈ {admin,user}; username
  unique (NOCASE); password min length.
  - **Files:** `internal/api/handlers_users.go` (new), `internal/api/handlers.go`
  - **Estimate:** 2.5h
  - **Dependencies:** Phase 1, 2.4
  - **Verification:** handler tests incl. last-admin, self-delete, bad role, weak
    password, duplicate username.

- [ ] **6.2** Wire `user.Store` into `Server` (`NewServer` param + `app.go`).
  Interface at the API layer, never a concrete impl (AGENTS §3).
  - **Files:** `internal/api/handlers.go`, `cmd/groovearr/app.go`
  - **Estimate:** 45m
  - **Verification:** `go build ./...`.

### Phase 7: Role-scoped SSE (C3 / M1)
**Goal:** Non-admins never receive log lines or job events.

- [ ] **7.1** Tag subscribers with a role: change `SSEHub.Register(client)` →
  `Register(client, admin bool)` (or a predicate), update the hub, `ServeHTTP`,
  and the API's `handleEvents` to pass `identity.Role == admin`.
  - **Files:** `internal/sse/hub.go`, `internal/sse/hub_test.go`,
    `internal/api/handlers_download.go` (`handleEvents`)
  - **Estimate:** 1.5h
  - **Dependencies:** Phase 2
  - **Verification:** hub test: non-admin client does not receive `log_line`/`job_*`,
    still receives `download:*` / `import_*`.

- [ ] **7.2** Classify event types: broadcast `log_line` and `job_*` (see
  `jobs/manager.go:236`) to admins only; keep download/import topics to all.
  Centralize the "admin-only event type" check in one function.
  - **Files:** `internal/sse/hub.go` (or `notifier.go`)
  - **Estimate:** 45m
  - **Dependencies:** 7.1
  - **Verification:** unit test per event type.

### Phase 8: UI role gating + user management
**Goal:** Users see only what they may use.

- [ ] **8.1 (H1)** Gate app-wide admin polling: `useJobWatcher()` must not run
  for non-admins (App.tsx:119). Keep `useDownloads` (shared view) and
  `useTrackingEvents` (download/import events still arrive for users), but the
  Tracking **page** queries must not mount for users.
  - **Files:** `ui/src/App.tsx`, `ui/src/hooks/use-job.ts`
  - **Estimate:** 1h
  - **Dependencies:** 0.3, 6.2
  - **Verification:** user sees no `/api/jobs` calls in the network tab.

- [ ] **8.2** `client.ts`: add `getMe()` (if not already in 0.3) and user CRUD
  (`listUsers`, `createUser`, `updateUser`, `deleteUser`). All HTTP via
  client.ts (AGENTS §12).
  - **Files:** `ui/src/api/client.ts`, `ui/src/api/types.ts`
  - **Estimate:** 45m

- [ ] **8.3** Gate nav + routes: hide **Artists (tracking)** and **Settings**
  for non-admins in `SidebarNav`; redirect `/settings` and `/tracking` to
  `/discover` when role != admin. Downloads stays visible.
  - **Files:** `ui/src/components/SidebarNav.tsx`, `ui/src/App.tsx`
  - **Estimate:** 1.5h
  - **Dependencies:** 0.3
  - **Verification:** user cannot reach settings/tracking by URL; downloads open.

- [ ] **8.4** Settings → **Users** (admin-only). Create + manage users in the UI.
  - **Create user form (required):** username (trimmed, unique case-insensitive),
    password (required, min length, with an optional *generate* action), role
    selector (`user` default / `admin`) → `POST /api/users`. On success: toast +
    refresh the list; clear the form. Surface validation errors inline via
    `StatusMessage`.
  - **List:** username, role, active/disabled, created date, row actions
    (change role, disable/enable, reset password, delete). Use `DataTable`.
  - **Guards surfaced in UI:** last-admin delete/demote and self-delete are
    disabled with a reason (server also enforces).
  - Reuse `FormGroup`, `Card`, `Button`, `StatusMessage`.
  - **Files:** `ui/src/features/settings/UsersSection.tsx` (new), `SettingsPage.tsx`
  - **Estimate:** 3h
  - **Dependencies:** 8.2, 8.3
  - **Verification:** admin creates a user from the UI → that user can log in and
    is gated as `user`; non-admin sees no Users section; guards block last-admin/
    self-delete.

### Phase 9: Item attribution (who requested what)
**Goal:** Persist which user *requested* each downloaded track/album and which
user *imported* each playlist. Display/audit only. **DB-only — never written to
ID3/audio tags** (`internal/tagging` is untouched). Enables a future "show only
my requested albums" filter by `added_by_user_id`; grants no authorization.

- [ ] **9.1 Schema (additive).** Add columns to the `migrations` list
  (`library/sqlite/store.go:328`, tolerate `duplicate column name`) **and** to
  the `CREATE TABLE` definitions for fresh DBs:
  - `downloads.requested_by_user_id INTEGER NOT NULL DEFAULT 0`
  - `tracks.added_by_user_id INTEGER`, `tracks.added_by_username TEXT NOT NULL DEFAULT ''`
  - `albums.added_by_user_id INTEGER`, `albums.added_by_username TEXT NOT NULL DEFAULT ''`
  - `playlists.added_by_user_id INTEGER`, `playlists.added_by_username TEXT NOT NULL DEFAULT ''`
  - indexes on each `*_user_id` column.
  NULL/0 = system or unknown; existing rows stay NULL.
  - **Files:** `internal/library/sqlite/store.go`
  - **Estimate:** 1h
  - **Verification:** fresh + pre-existing DB both open; `PRAGMA table_info` shows columns.

- [ ] **9.2 Carry the requester through the queue.** Add `RequestedByUserID int64`
  to `download.Record` (`download/types.go`) and persist it in
  `download/sqlite/store.go`; add the actor parameter to
  `download.Service.Queue` / `QueueAlbum` / `QueueAlbumWithFallback` /
  `QueuePending`. API handlers pass `identity.UserID`
  (`handlers_download.go:154,211,302`, `handlers_discover.go:714`).
  - **Files:** `internal/download/types.go`, `internal/download/service.go`,
    `internal/download/sqlite/store.go`, `internal/api/handlers_download.go`,
    `internal/api/handlers_discover.go`
  - **Estimate:** 2h
  - **Dependencies:** Phase 2 (identity)
  - **Verification:** queued record persists the requester; unit test round-trip.

- [ ] **9.3 Stamp attribution on import.** In `handler_library.go` and
  `handler_album_import.go`, set `added_by_user_id` + `added_by_username` when
  creating track/album rows, from the record's requester. System paths
  (scanner/organizer/enrichment) leave NULL. **Upserts set attribution on
  INSERT only — never clobber on update** (insert-only assignment).
  - **Files:** `internal/library/sqlite/store.go` (`ImportTrack`, `UpsertAlbum`,
    `UpsertTrack`), `internal/download/handler_library.go`,
    `internal/download/handler_album_import.go`
  - **Estimate:** 2h
  - **Dependencies:** 9.2
  - **Verification:** import sets attribution; re-scan / re-enrich does not change it.

- [ ] **9.4 Playlist attribution.** Add an `actorUserID` parameter to
  `playlist.Service.ImportPlaylist`; set attribution on create only.
  `SyncPlaylist` / auto-sync must not overwrite. `handleImportPlaylist` passes
  `identity.UserID`.
  - **Files:** `internal/playlist/service.go`, `internal/api/handlers_playlist.go`,
    `internal/library/sqlite/store.go` (`UpsertPlaylist`)
  - **Estimate:** 1.5h
  - **Dependencies:** Phase 2
  - **Verification:** import stamps the importer; later sync leaves it unchanged.

- [ ] **9.5 Domain + reads.** Add `AddedByUserID int64` + `AddedByUsername string`
  to `domain.Track`, `domain.Album`, `domain.Playlist`; select them in store
  reads and expose in API JSON. No authorization derived from them.
  - **Files:** `internal/domain/{track,album,playlist}.go`,
    `internal/library/sqlite/store.go`, `internal/api/handlers_library.go`,
    `internal/api/handlers_playlist.go`
  - **Estimate:** 1.5h
  - **Dependencies:** 9.1
  - **Verification:** list/detail responses include the fields for newly added
    items; blank for system items.

- [ ] **9.6 UI display.** Show "Requested by <username>" on `TracksTable.tsx` /
  `AlbumDetailView.tsx` and "Added by <username>" on `PlaylistCard.tsx` when
  present. No editing.
  - **Files:** `ui/src/features/library/TracksTable.tsx`,
    `ui/src/features/library/AlbumDetailView.tsx`,
    `ui/src/features/playlists/PlaylistCard.tsx`, `ui/src/api/types.ts`
  - **Estimate:** 1.5h
  - **Dependencies:** 9.5
  - **Verification:** badge appears for user-added items, absent for scanned.

- [ ] **9.7 (D3) User-deletion durability.** Store `added_by_username` as a
  snapshot at insert, so attribution survives even if the user row is hard
  deleted; `added_by_user_id` remains for filtering. (No backfill possible for
  pre-existing rows.)

> **Non-goal:** attribution is **never** written into ID3/FLAC tags or filenames.
> It lives only in `library.db`.

### Phase 10: Docs
- [ ] **10.1** `docs/api.md` (new `/api/me`, `/api/users/*`, admin markers, note
  `/api/downloads*` is shared, new `added_by_*` response fields), `docs/setup.md`
  (bootstrap, first admin, `none` = admin warning, `basic` retired),
  `docs/architecture.md` (role + attribution note).
  - **Estimate:** 1h
  - **Verification:** docs match routes.

## Testing strategy

- [ ] Unit: `config.Mask()` omits the API key.
- [ ] Unit: `internal/user/sqlite` store (CRUD, duplicate case-variant, last-admin count).
- [ ] Unit: `session` role round-trip, `DeleteByUserID`.
- [ ] Unit: `withAuth` identity per transport; `adminOnly` 403; bypass → user.
- [ ] Unit: SSE hub role filtering per event type.
- [ ] Handler: table-driven role matrix (settings → 403 for user; downloads/library → allowed).
- [ ] Handler: user-management guards (last admin, self-delete, weak password, duplicate).
- [ ] Handler: session invalidated after disable/password change.
- [ ] Integration: bootstrap seed → admin login → create user → user login → user can see/cancel downloads, cannot reach settings.
- [ ] Manual: no `/api/config` call from user session; no `log_line` over a user SSE stream.
- [ ] Unit: download queue persists `requested_by_user_id`.
- [ ] Unit: `ImportTrack` / `UpsertAlbum` set attribution on insert, preserve on update.
- [ ] Unit: `UpsertPlaylist` stamps importer on create; sync preserves.
- [ ] Handler: queued download + imported playlist expose `added_by_*`; system adds blank.
- [ ] Manual: a downloaded file's ID3/FLAC tags are unchanged (attribution is DB-only).

## Total estimate

**Time:** ~28 hours (7h from the folded-in findings, ~9h attribution)
**Complexity:** High (auth-sensitive; requires the 3-round security review)

## Notes / risks

- **Order matters:** Phase 0 must ship before roles, or enabling roles breaks
  every non-admin on refresh (C2) while still leaking the key (C1).
- **Security review required** (3 rounds): secret masking, identity injection,
  403 vs 401 semantics, bypass role, last-admin lockout, session invalidation,
  password handling.
- `auth.method=none` = admin is intentional backwards compatibility; document it
  as *not* access control.
- API key is a full admin credential; keep it out of `Mask()` output, `/api/me`,
  and logs.
- Cookie `Secure` is off behind a TLS-terminating proxy (`auth.go:151`,
  `Secure: r.TLS != nil`) — pre-existing; flag in the security round.
- `NewServer` already takes many params; adding `user.Store` follows convention.
  A deps-struct refactor is out of scope.
- **Attribution is DB-only.** Never touch `internal/tagging` (files must not
  differ from a plain download). Scope is display/audit; a per-user "my requests"
  view is a future consumer of `added_by_user_id`, not part of this plan.
- Attribution on **insert only** — scrapes/re-enrichment/organize must not
  rewrite it. Test the preserve-on-update path explicitly.
