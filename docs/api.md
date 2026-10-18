# Groovearr — API Reference

Base URL: `http://localhost:8008`

All responses are JSON. Errors use `{"error": "message"}`.

## Authentication & Authorization

Authentication is chosen by `auth.method`:

- `none` (default) — no login. Every request is treated as **admin**. This is
  **not access control**; only run it on a trusted network.
- `forms` — username/password login backed by the `users` table. Log in with
  `POST /api/login`, which sets the `groovearr_sid` session cookie.

Enabling `forms` is guarded so a live switch cannot lock you out of the
admin-only settings it protects. When a save turns the method **from**
`none`/empty **to** `forms`, the server:

- **seeds the first admin** from the request's `auth.username` /
  `auth.password` when the `users` table is empty. Only a password supplied in
  that same request is used — a stored hash the request merely preserved (an
  omitted/empty/masked password) is never re-seeded, because it may be a
  credential the operator no longer knows. The password must meet the same
  boundary as `POST /api/users` (at least 8 characters, at most 72 bytes); and
- **refuses the switch** (`400`) when the store holds no *enabled admin*
  account, because a store of regular users alone can never reach settings to
  grant admin. Create or enable an admin in the **Users** tab first, then
  switch.

The guard is keyed on the transition, so ordinary saves made while `forms` is
already active are neither re-checked nor refused. `auth.username` /
`auth.password` are otherwise only a startup bootstrap and are never consulted
by `POST /api/login`, which authenticates against the `users` table alone.

With `forms`, every `/api/*` route requires an identity except `GET /api/health`
and `POST /api/login`. Three transports resolve an identity:

- **Session cookie** `groovearr_sid` — set by login; carries the account's role.
  It is `HttpOnly`, `SameSite=Lax`, and `Secure` when the request arrived over
  TLS directly or via a reverse proxy that set `X-Forwarded-Proto: https`
  (`Secure` is only ever added, never removed, so a spoofed header cannot
  downgrade it).
- **API key** — a full **admin** credential, accepted as `X-Api-Key: <key>`,
  `?apikey=<key>`, or `Authorization: Bearer <key>`. The key itself is never
  returned by any endpoint and is masked in `GET /api/config`.
- **Local bypass** — a host inside `auth.local_bypass_subnets` that presents no
  credential is authenticated as a regular **user**, never admin. (Behavior
  change: it previously carried the same access as a valid credential.)

Roles are `admin` and `user` (DB-only — see
[architecture](architecture.md#roles--attribution)). The **admin-only** settings
surface — config, rate limits, background jobs, artist tracking, quality
profiles, logs, debug, user management, and the provider OAuth connect flows
(`/api/spotify/*`, `/api/tidal/*`) — returns
`403 {"error": "forbidden"}` to a non-admin. The **shared** surface — search,
downloads, library, playlists, discovery, and events — is available to any
authenticated user. A missing or invalid credential returns
`401 {"error": "unauthorized"}`.

`GET /api/setup/status` is the first-run probe. It is **not** admin-gated, but
it is still wrapped by `withAuth` like every other `/api/*` route: under
`auth.method=forms` it requires an authenticated identity and returns `401`
without one — it is **not** unauthenticated. There are no setup mutation routes
today (any future `/api/setup/*` mutation is part of the admin-only settings
surface).

### `GET /api/me`

Return the caller's coarse identity. Never exposes the API key.

**Response** `200`:
```json
{"username": "admin", "role": "admin", "via_api_key": false}
```

- Session — the account's `username` and `role`.
- API key — `{"username": "", "role": "admin", "via_api_key": true}`.
- `auth.method=none` — `{"username": "", "role": "admin", "via_api_key": false}`.
- Local-bypassed host — `{"username": "", "role": "user", "via_api_key": false}`.

**Errors**: `401` — no identity was resolved.

### `POST /api/login`

Only available when `auth.method` is `forms`. Authenticates a username/password
against the `users` table and, on success, sets the `groovearr_sid` session
cookie. The bootstrap `auth.username` / `auth.password` are not consulted here.

**Request**:
```json
{"username": "admin", "password": "secret"}
```

**Response** `200`:
```json
{"status": "ok"}
```

**Errors**:
- `400` — invalid body, or `{"error": "login not available with current auth method"}` when `auth.method` is not `forms`
- `401` — `{"error": "invalid credentials"}` for an unknown user, disabled account, or wrong password (all indistinguishable)
- `500` — `{"error": "authentication unavailable"}` when no user store is wired

### `POST /api/logout`

Clear the `groovearr_sid` session cookie. The clearing cookie carries the same
`HttpOnly` / `SameSite=Lax` treatment (and `Secure` when the request is TLS, per
the same rule as login) so the browser matches and drops it.

**Response** `200`:
```json
{"status": "ok"}
```

## Users (Admin only)

Account management. Every route below is gated by `s.adminOnly` and returns
`403 {"error": "forbidden"}` to a non-admin.

A user response is the safe wire shape — it **never** includes the stored
password hash:

```json
{
  "id": 1,
  "username": "admin",
  "role": "admin",
  "disabled": false,
  "created_at": "2026-10-01T09:00:00Z",
  "updated_at": "2026-10-01T09:00:00Z"
}
```

### `GET /api/users`

List every account.

**Response** `200`: `[userResponse, ...]`

### `POST /api/users`

Create an account.

**Request**:
```json
{"username": "alice", "password": "at-least-8-chars", "role": "user"}
```

- `username` — required
- `password` — required, minimum 8 characters, maximum 72 bytes
- `role` — optional, `admin` | `user`; empty defaults to `user`

**Response** `201`: the created `userResponse`.

**Errors**:
- `400` — invalid body, missing username, password shorter than 8 or longer than 72 bytes, or invalid role
- `409` — `{"error": "username already exists"}` (case-insensitive duplicate)
- `500` — internal error

### `PATCH /api/users/{id}`

Partial update. Omitted fields keep their current value.

**Path**: `id` — integer user ID

**Request** (at least one field):
```json
{"role": "admin", "disabled": false, "password": "new-password-8+"}
```

**Response** `200`: the updated `userResponse`.

Live sessions for the target account (and its open SSE stream) are invalidated
only when a `role`, `disabled`, or `password` field **actually changes** — a
PATCH that echoes the current value is a no-op and does not retire sessions. This
makes a real role/password change take effect immediately. A self password
change logs the acting admin out on their next request (no fresh session is
re-issued).

**Errors**:
- `400` — invalid user ID, invalid body, invalid role, or password shorter than 8 or longer than 72 bytes
- `404` — user not found
- `409` — `{"error": "cannot remove the last active admin"}` (demoting/disabling the last admin)
- `500` — internal error

### `DELETE /api/users/{id}`

Delete an account.

**Path**: `id` — integer user ID

**Response** `200`:
```json
{"status": "deleted"}
```

**Errors**:
- `400` — invalid user ID
- `403` — `{"error": "cannot delete your own account"}`
- `404` — user not found
- `409` — `{"error": "cannot remove the last active admin"}`
- `500` — internal error

## Health & Config

### `GET /api/health`

Health check.

**Response** `200`:
```json
{"status": "ok"}
```

### `GET /api/version`

Build version stamped into the binary at link time (`internal/version`, set via
`-ldflags "-X .../internal/version.Version=..."` by the Makefile and Dockerfile).
Authenticated but **not** admin-only — every signed-in user sees it in the
sidebar, above the Settings tab.

**Response** `200`:
```json
{"version": "v0.9.0"}
```

### `GET /api/config`

**Admin only.**

Get current configuration. API keys are partially masked.

**Response** `200`:
```json
{
  "soulseek": {
    "slskd_url": "http://localhost:5030",
    "api_key": "sk************",
    "search_timeout": 60,
    "min_upload_speed": 0
  },
  "deezer": {
    "arl": "",
    "quality": "flac",
    "allow_fallback": true,
    "access_token": ""
  },
  "library": {
    "download_path": "/downloads",
    "library_path": "/music",
    "folder_template": "{artist}/{album} ({year})/{track:02d} - {title}",
    "playlist_path": "/playlists",
    "playlist_template": "{position:02d} {artist} - {title}"
  },
  "quality": {
    "preferred_format": "flac",
    "min_bitrate": 0
  },
  "auth": {
    "method": "forms",
    "username": "admin",
    "password": "********",
    "api_key": "********",
    "has_api_key": true,
    "local_bypass_subnets": []
  }
}
```

Sensitive fields are masked (`Config.Mask()`): `auth.password` and `auth.api_key`
are never returned raw. The **raw API key is never returned by any endpoint**;
instead `auth.has_api_key` is a derived boolean (`true` when a key is
configured, `false` otherwise) that signals presence without revealing the
credential. Provider source secrets (`soulseek.api_key`, `deezer.arl`,
`deezer.access_token`, …) are masked in the same response.

### `PUT /api/config`

**Admin only.**

Merge partial config and persist. Triggers plugin reload and directory creation.

**Request**:
```json
{
  "soulseek": {"slskd_url": "http://slskd:5030", "api_key": "my-key"},
  "library": {"download_path": "/data/downloads"}
}
```

**Response** `200`:
```json
{"status": "saved"}
```

**Errors**:
- `400` — validation failed: `{"error": "validation failed", "errors": ["..."]}`
- `400` — a switch to `auth.method=forms` was refused because no enabled admin
  account exists, or because an empty user store was not given a first-admin
  username and a strong enough password (>=8 chars, <=72 bytes); the previous
  method is left in place.
- `500` — `{"error": "internal error"}` when the user store could not be read
  while checking/enabling `forms` (an infrastructure failure, not bad input).

### `GET /api/config/sources`

**Admin only.**

List registered download source plugins with status.

**Response** `200`:
```json
[
  {"name": "soulseek", "display_name": "Soulseek", "configured": true, "status": "connected"},
  {"name": "deezer",  "display_name": "Deezer",   "configured": true, "status": "configured"}
]
```

Status values: `connected`, `configured`, `not_configured`.

### `POST /api/config/test/{source}`

**Admin only.**

Test connectivity to a download source.

**Path**: `source` — plugin name (e.g. `soulseek`, `deezer`)

**Response** `200`:
```json
{"status": "connected"}
```
Or if unreachable but configured:
```json
{"status": "configured", "error": "connection refused"}
```

**Errors**:
- `400` — source not configured: `{"error": "source not configured", "status": "not_configured"}`
- `404` — unknown source

### Provider OAuth (Admin only)

Provider OAuth connect flows are registered through the plugin `RouteRegistrar`
and are **admin-only** — they mutate global provider credentials (server-side
tokens) and are part of the settings surface. Each returns
`403 {"error": "forbidden"}` to a non-admin.

| Route | Purpose |
|-------|---------|
| `GET /api/spotify/login` | Start the Spotify PKCE flow; redirects to Spotify's authorization page. |
| `GET /api/spotify/callback` | OAuth callback; verifies `state`, exchanges the code, and stores tokens in `sources.spotify`. Redirects to `/settings?spotify=connected`. |
| `GET /api/tidal/login` | Start the Tidal device-code flow; returns an HTML page with the user code and a self-polling status. |
| `GET /api/tidal/poll` | Poll device-code completion; on success stores tokens in `sources.tidal`. Returns `{"status": "pending" \| "connected" \| "expired" \| "error", "message": "..."}`. |

---

## Search

### `POST /api/search`

Search tracks and albums across download sources.

**Request**:
```json
{
  "query": "daft punk get lucky",
  "source": ""        // "" = first configured, "hybrid" = all, "soulseek" = specific
}
```

**Response** `200`:
```json
{
  "tracks": [
    {
      "username": "peer123",
      "filename": "Daft Punk - Get Lucky.flac",
      "size": 30123456,
      "bitrate": 909,
      "duration": 369000,
      "quality": "flac",
      "free_upload_slots": 2,
      "upload_speed": 1048576,
      "queue_length": 0,
      "artist": "Daft Punk",
      "title": "Get Lucky",
      "album": "Random Access Memories",
      "track_number": 7,
      "cover_url": "https://..."
    }
  ],
  "albums": []
}
```

**Errors**:
- `400` — `query` is required, or no sources configured
- `404` — specified source not found

---

## Downloads

The download queue is **shared**: any authenticated user, admin or not, may
search, queue, list, cancel, and retry downloads. These routes are not
admin-gated (they are rate-limited per client IP).

### `POST /api/download`

Queue a single download from a search result.

**Request**:
```json
{
  "source": "soulseek",
  "username": "peer123",
  "filename": "Daft Punk - Get Lucky.flac",
  "size": 30123456,
  "artist": "Daft Punk",
  "album": "Random Access Memories",
  "title": "Get Lucky",
  "track_number": 7,
  "disc_number": 1,
  "year": 2013
}
```

**Response** `202`:
```json
{"download_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890"}
```

### `POST /api/download/match`

Search across all configured sources for the best matching track and queue it.

**Request**:
```json
{
  "title": "Get Lucky",
  "artist": "Daft Punk",
  "duration": 369000,
  "exclude_source": ""
}
```

**Response** `202`:
```json
{
  "download_id": "a1b2c3d4-...",
  "source": "soulseek",
  "confidence": 0.85
}
```

**Errors**:
- `400` — `title` is required
- `404` — no matching track found across any source

### `GET /api/downloads`

List all downloads with full state.

**Response** `200`:
```json
[
  {
    "id": "a1b2c3d4-...",
    "source_name": "soulseek",
    "filename": "Daft Punk - Get Lucky.flac",
    "display_name": "Get Lucky",
    "state": "downloading",
    "progress": 45.2,
    "size": 30123456,
    "transferred": 13616166,
    "speed": 524288,
    "file_path": "",
    "error": "",
    "artist": "Daft Punk",
    "album": "Random Access Memories",
    "title": "Get Lucky",
    "track_number": 7,
    "year": 2013,
    "playlist_id": "5",
    "requested_by_user_id": 3,
    "requested_by_username": "alice"
  }
]
```

Each record carries `requested_by_user_id` / `requested_by_username` — the
account that queued the download. `requested_by_username` is a snapshot taken at
queue time and survives deletion of the account; `0` / `""` means a
system-queued download. This attribution is **DB-only** and is never written to
audio tags.

**States**: `queued` → `downloading` → `importPending` → `importing` → `imported` | `failed` | `ignored`

### `DELETE /api/downloads/{id}`

Cancel an active download and remove its record.

**Path**: `id` — download UUID

**Response** `200`:
```json
{"status": "cancelled"}
```

**Errors**:
- `404` — download not found

### `GET /api/events`

Server-Sent Events stream for real-time download progress.

**Response**: `text/event-stream` with keep-alive heartbeat (15s).

**Event types**:

| Event | Data |
|-------|------|
| `download:stateChanged` | `{"id":"...", "state":"downloading", "source_name":"soulseek", "filename":"..."}` |
| `download:progress` | `{"id":"...", "progress":45.2, "transferred":13616166, "size":30123456, "speed":524288}` |
| `download:completed` | `{"id":"...", "state":"importPending", "file_path":"/downloads/file.flac"}` |
| `download:failed` | `{"id":"...", "state":"failed", "error":"..."}` |
| `import:completed` | `{"id":"...", "state":"imported", "library_track_id":42}` |
| `metadata_refresh` | `{"album_id": 42}` — an album's provider metadata finished materializing |

---

## Library

The library is **shared and read-only**: every authenticated user — admin or
not — may list it and download its files. The two download routes below serve
**only files that resolve under `library.library_path`**; the stored path is
resolved and symlink-checked before any bytes are sent, and server paths are
never written to a response body. Both download routes are rate-limited on the
shared `download` bucket, the same one the download queue uses, and extend the
per-request write deadline to a finite cap (re-armed per archive entry) so a
large file on a slow link is not cut off by the server's global 30s timeout,
while a stalled client is still eventually cut.

### `GET /api/library/tracks`

List/search library tracks.

**Query params**: `q` (search), `offset` (default 0), `limit` (default 200, max 1000)

**Response** `200`:
```json
[
  {
    "id": 42,
    "album_id": 10,
    "artist_id": 3,
    "title": "Get Lucky",
    "track_number": 7,
    "disc_number": 1,
    "duration": 369000,
    "file_path": "/music/Daft Punk/RAM (2013)/07 - Get Lucky.flac",
    "bitrate": 909,
    "file_size": 30123456,
    "isrc": "USQX91300105",
    "added_by_user_id": 3,
    "added_by_username": "alice",
    "created_at": "2026-07-19T12:00:00Z",
    "updated_at": "2026-07-19T12:00:00Z"
  }
]
```

Tracks, albums, and playlists carry `added_by_user_id` / `added_by_username` —
the account whose download imported the row. `0` / `""` means scanned or
system-imported. The fields are set on insert only (re-scans and re-enrichment
never rewrite them) and are **DB-only** — never written to audio tags or
filenames.

### `GET /api/library/tracks/{trackID}/download`

Stream a single library track file. Available to any authenticated user
(shared, not admin-gated).

**Path**: `trackID` — integer library track ID

**Response** `200`: the raw audio file, with:

- `Content-Type` from the explicit audio MIME map — `.flac` → `audio/flac`,
  `.mp3` → `audio/mpeg`, `.m4a` → `audio/mp4`, `.opus`/`.ogg` → `audio/ogg`,
  `.wav` → `audio/wav`, `.aac` → `audio/aac` — falling back to the stdlib MIME
  lookup, then `application/octet-stream`.
- `Content-Disposition: attachment` with both a legacy ASCII `filename=` and an
  RFC 5987 `filename*=UTF-8''…` parameter, so non-ASCII titles survive intact.
- `Range` and `HEAD` support (partial content / resume) via `http.ServeContent`,
  plus `Last-Modified` and `Content-Length`.

**Errors**:
- `404` `{"error": "not found"}` — unknown `trackID`, a track with no stored
  file path, a path that escapes `library_path` (traversal or symlink), or a
  non-regular file. These are deliberately indistinguishable and never reveal
  the server path.
- `429` — shared `download` rate-limit bucket exhausted; carries `Retry-After`
  and `X-RateLimit-Bucket: download`.
- `500` `{"error": "internal error"}` — the store could not be read.

### `GET /api/library/artists`

List/search library artists.

**Query params**: `q` (search), `offset` (default 0), `limit` (default 200, max 1000)

**Response** `200`: `[Artist, ...]`

### `GET /api/library/artists/{artistID}/albums`

List one artist's albums, **paginated**.

**Path**: `artistID` — integer library artist ID

**Query params**: `offset` (default 0), `limit` (default 200, max 1000). `q` is
accepted for shape-compatibility but ignored — the artist scopes the result.

**Response** `200`: `[Album, ...]`

The artist album grid loads **progressively**: the SPA issues offset/limit pages
(50 visible rows per page) as the user scrolls and appends them, so this endpoint
is pagination-critical. For a cross-library album search use
`GET /api/library/albums`.

### `GET /api/library/albums`

List/search library albums.

**Query params**: `q` (search), `offset` (default 0), `limit` (default 200, max 1000)

**Response** `200`: `[Album, ...]`

This is the global, cross-library album list. It is **not surfaced in the UI** —
the SPA only ever shows an artist's albums through
`GET /api/library/artists/{artistID}/albums` (see above), so this endpoint is used
by API clients and diagnostics only.

### `GET /api/library/albums/{albumID}`

Return one library album by ID.

**Path**: `albumID` — integer library album ID

**Response** `200`: `Album`

Resolves a single album independently of the paginated artist-album listing, so
the library page's deep link `?artist=X&album=Y` shows the album even when it is
past the first loaded page of the artist's album grid.

**Errors**:
- `400` `{"error": "invalid album ID"}` — non-numeric `albumID`.
- `404` `{"error": "album not found"}` — unknown `albumID`.
- `500` — the store could not be read.

### `GET /api/library/albums/{albumID}/download`

Stream an album as a ZIP archive. Available to any authenticated user (shared,
not admin-gated).

**Path**: `albumID` — integer library album ID

**Response** `200` `application/zip`, written straight to the response (no temp
files, no buffering). `Content-Disposition: attachment` suggests
`"<Artist> - <Album>.zip"`. Entries are added in the store's disc/track order:

- Audio tracks named `<NN> - <Title>.<ext>`, or `<DD>-<NN> - <Title>.<ext>` for a
  multi-disc album. Colliding names are deduplicated with ` (2)`, ` (3)`, …
  before the extension.
- The album cover when a recognized cover file already exists on disk — a
  lookup, never generated or fetched.
- Any existing `.m3u` / `.m3u8` playlists, copied **verbatim** under their
  album-relative path — never generated or rewritten.

Tracks whose stored path is empty or fails the `library_path` containment check
are skipped rather than failing the archive, and a missing cover or playlist
never fails it either. An album with no resolvable tracks returns `404` before
any bytes are written; a track that passes containment but cannot be opened
(removed or unreadable at read time) is skipped like any other, so a rare
all-unopenable album yields a valid but entry-less archive.

At most **3 album ZIPs stream at once**; beyond that the request is rejected
rather than queued.

**Errors**:
- `404` `{"error": "not found"}` — unknown `albumID`, or no track resolves to a
  servable file.
- `503` `{"error": "album download capacity reached, retry shortly"}` with
  `Retry-After: 5` — the album-ZIP concurrency cap (3) is saturated; retry
  shortly.
- `429` — shared `download` rate-limit bucket exhausted; carries `Retry-After`
  and `X-RateLimit-Bucket: download`.
- `500` `{"error": "internal error"}` — the store could not be read.

### `GET /api/library/albums/{albumID}/discovery`

Return an album's **expected tracklist** merged with live local download state.
Served from the durable metadata catalog — a materialized (warm) album is a pure
SQLite read with **zero provider calls**. The first open of an album that has no
materialized tracklist triggers one bounded, deduplicated cold fetch (Option A);
if that does not resolve, the response falls back to the local library tracks.
An album parked `unmatched`/`failed` before its long retry window is the one
exception: it is served from the persisted metadata row with local library tracks
and **no** provider call (see below).

**Path**: `albumID` — integer library album ID

**Response** `200`:
```json
{
  "provider": "deezer",
  "provider_album_id": "302127",
  "tracks": [
    {
      "title": "Get Lucky",
      "track_number": 7,
      "duration_ms": 369000,
      "downloaded": true,
      "library_track_id": 42,
      "file_path": "/music/Daft Punk/RAM (2013)/07 - Get Lucky.flac",
      "file_size": 30123456,
      "bitrate": 909,
      "format": "flac"
    }
  ],
  "metadata_pending": false,
  "metadata_status": "fresh",
  "metadata_terminal": false
}
```

- `tracks` rows come from the materialized provider tracklist merged with the
  local library tracks (matched by ISRC, then disc/track number, then title).
  `downloaded` is `true` and the `library_track_id` / file fields are present
  when a local file matched; otherwise `downloaded` is `false` and those fields
  are omitted.
- `metadata_pending` — `false` for a warm read; `true` when the provider
  tracklist is not yet materialized (cold, or a cold fetch that timed out,
  errored, or found no match). A **terminal** album also reports
  `metadata_pending=true` because its tracklist never materialized, so this flag
  alone must not be read as "still working" — use `metadata_terminal` to tell a
  cold/in-flight album apart from a parked one. When `true`, `tracks` carries the
  local library tracks as a best-effort fallback and `provider` /
  `provider_album_id` may be empty.
- `metadata_status` — the album's persisted catalog refresh status:
  `pending` | `fresh` | `stale` | `unmatched` | `failed` (empty string when the
  album has no catalog metadata row yet).
- `metadata_terminal` — `true` when `metadata_status` is `unmatched` or `failed`:
  a terminal match failure. `unmatched` is retried after the long interval
  (90d, `catalog.UnmatchedRetry`). `failed` has no queue row and is not
  auto-retried, but a cold open re-searches it after the same 90d read gate; a
  recheck/manual refresh re-arms it sooner. It is `false` for a cold/pending
  album (still materializing) and for a warm `fresh`/`stale` read. The UI uses it
  to render an accurate parked state instead of an indefinite "fetching" spinner.
- A matched album is served from the catalog **even when its metadata is older
  than the soft TTL** (`catalog.album_refresh_days`, default 30d): the stale rows
  are returned immediately and a low-priority refresh is enqueued behind the
  response. An album parked `unmatched`/`failed` before the read gate elapses is
  also served without a provider call — its persisted metadata row plus local
  library tracks, never re-searched on open, and **not** re-enqueued. Only
  `unmatched` keeps a parked queue row with a 90-day `due_at`; `failed` has no
  queue row and is re-searched by a cold open after the same read gate or by a
  recheck/manual refresh.

**Errors**:
- `400` — invalid album ID.
- `404` `{"error": "album not found"}` / `{"error": "artist not found"}` — the
  album (or its artist row) is unknown.
- `429` — shared `search` rate-limit bucket exhausted.

### `POST /api/library/albums/{albumID}/refresh`

Enqueue a **high-priority** background metadata refresh for one album
(`reason=manual`, `priority=high`). The refresh engine materializes the album
asynchronously; when it completes the server broadcasts a `metadata_refresh`
SSE event (`{"album_id": <id>}`, see [SSE Streaming](#sse-streaming)). The
request never waits on a provider.

**Path**: `albumID` — integer library album ID

**Response** `202`:
```json
{"queued": true}
```

The album is validated before enqueueing: an unknown `albumID` is a `404`, so a
refresh cannot enqueue a permanent terminal no-match row for a missing album.

**Errors**:
- `400` — invalid album ID.
- `404` `{"error": "album not found"}` — no such library album.
- `429` — shared `download` rate-limit bucket exhausted (the same bucket as the
  sibling library-album actions).
- `503` `{"error": "refresh service not available"}` — no refresh engine wired.
- `500` — the queue write failed.

### `POST /api/library/albums/{albumID}/download-missing`

Queue every not-yet-downloaded track of an album as pending downloads, resolving
sources in the background via the monitor's `resolvePendingSources` (the same
pattern as `POST /api/playlists/{id}/download-missing`). The expected tracklist is
read from the materialized catalog via the discovery read path, so a warm album
queues from SQLite.

**Path**: `albumID` — integer library album ID

**Response** `200` — tracks were queued (an `errors` array is added if some
tracks failed to queue):
```json
{"queued": 9}
```

**Response** `202` — the album has **no materialized provider tracklist yet**
(cold, or a terminal no-match awaiting its retry). A best-effort refresh is
queued and the caller should retry rather than read a silent `{"queued":0}` as
"nothing missing":
```json
{"queued": 0, "pending": true, "message": "album metadata is not materialized yet; a refresh was queued and the download will retry"}
```

**Errors**:
- `400` — invalid album ID.
- `404` `{"error": "album not found"}` / `{"error": "artist not found"}`.
- `429` — shared `download` rate-limit bucket exhausted.

### `GET /api/jobs`

**Admin only.**

Return the current (or last) background job, or `null` when none has run.

**Response** `200`:
```json
{
  "type": "scan",
  "state": "running",
  "progress": 42.5,
  "message": "03 - Nightcall.flac",
  "done": 850,
  "total": 2000,
  "started_at": "2026-08-18T10:00:00Z",
  "finished_at": null,
  "error": ""
}
```

`state` is one of `idle | running | completed | failed | cancelled`.

### `POST /api/jobs/scan`

**Admin only.**

Start a background filesystem scan of the library path. Imports new files,
skips duplicates (by file path), and backfills embedded cover art. If a job is
already running, the current job is returned with `started: false` instead.

**Response** `202` (or `200` when a job was already running):
```json
{
  "job": { "type": "scan", "state": "running", "progress": 0, "done": 0, "total": 2000 },
  "started": true
}
```

Rate-limited: 2 req/min per client IP (override via `RATE_SCAN`).

### `POST /api/jobs/enrich`

**Admin only.**

Start a background metadata enrichment of the whole library — fills in missing
ISRC, genres, release dates, external IDs, cover art, and artist images from
the configured metadata providers. Outgoing requests honor each provider's own
rate limits; the artist-image refresh runs once per artist. Tracks that are
already fully enriched (ISRC, external IDs, genres, release date, cover, and
artist image all present) are skipped without hitting the providers. Provider
order matters: once the top provider fills a track's fields, lower-priority
providers are not called. Enrichment runs with a small worker pool (saturating
provider rate limits) while serializing per album.

**Response** `202`: `{ "job": {...}, "started": true }` as above.

Rate-limited: 2 req/min per client IP (override via `RATE_ENRICH`).

### `POST /api/jobs/warm-library`

**Admin only.**

Start the opt-in **bulk metadata warm** job. It materializes provider metadata
for a bounded batch of albums that need warming, priority-ordered (new →
never-materialized → incomplete; complete albums are skipped). There is **no
periodic sweep** — this job is default off and must be triggered explicitly.
Candidates are recomputed on every run, so an album materialized earlier drops
out and re-triggering the job resumes with the remaining candidates. Progress
and a pace-based ETA stream over SSE.

**Response** `202` (or `200` when a job was already running):
```json
{"job": {"type": "warm-library", "state": "running"}, "started": true}
```

Rate-limited: shared `scan` bucket, 2 req/min per client IP (override via
`RATE_SCAN`).

### `POST /api/jobs/recheck-unmatched`

**Admin only.**

Start the explicit **recheck of parked unmatched albums**. It first runs the
identity-first resolver: probe missing durations for only the parked albums'
tracks, look up each track by provider track id or ISRC, score the candidate with
title, artist, duration, album, and track-count evidence, and materialize
high-confidence releases through the refresh engine's normal catalog write path.
Ambiguous, wrong-ISRC, and conflicting candidates stay parked for review.

If no identity resolver is wired (or the resolver reports no configured provider
with the identity capability), it falls back to the original behavior:
re-arming the terminal (`unmatched`/`failed`) album queue rows to *due now*, so
the background refresh engine re-searches them with the current matcher without
waiting out the 90-day `UnmatchedRetry` park.

The request returns immediately. The identity pass performs cooldown-aware
provider I/O in the job runner (paced by the provider HTTP clients); progress
and the resolved/parked counts appear in the job's `message` (poll `GET
/api/jobs` or watch the SSE `job_completed` event).
The **Recheck unmatched** button on the settings catalog card calls this endpoint.

**Response** `202` (or `200` when a job was already running):
```json
{"job": {"type": "recheck-unmatched", "state": "running"}, "started": true}
```

A 1-track local release is materialized as exactly one provider row. It is marked
partial only when the resolved track belongs to a provider release known to have
more than one track; a true single (or an unknown track count) stays refreshable.
A multi-track release is materialized only when every track maps to the same
provider album with high confidence.

Rate-limited: shared `scan` bucket, 2 req/min per client IP (override via
`RATE_SCAN`).

### `POST /api/jobs/cancel`

**Admin only.**

Request cancellation of the running job.

**Response** `200`: the current `Job`

Job progress is also streamed to SSE clients (`/api/events`) as `job_started`,
`job_progress`, `job_completed`, `job_failed`, and `job_cancelled` events.

### `GET /api/catalog/status`

**Admin only.**

Read-only snapshot of the materialized metadata catalog and its background
refresh engine. The engine is a **service-owned loop**, not a `jobs.Manager`
job, so it never appears in `GET /api/jobs` — this endpoint is the engine's own
status surface; the parked-album rows behind `albums.unmatched` are listed by
`GET /api/catalog/unmatched`. Settings → Jobs renders it as the "Metadata Catalog"
card, whose **Warm library** button calls `POST /api/jobs/warm-library` and
whose **Recheck unmatched** button calls `POST /api/jobs/recheck-unmatched`.

**Response** `200`:
```json
{
  "enabled": true,
  "queue": {"queued": 3, "due_now": 1, "scheduled": 2},
  "albums": {"pending": 0, "fresh": 12, "stale": 1, "unmatched": 2, "failed": 0},
  "artists": {"materialized": 5},
  "engine": {
    "running": true,
    "interval_seconds": 60,
    "batch_size": 8,
    "last_drain_at": "2026-10-12T12:00:00Z",
    "last_drain_count": 3
  }
}
```

- `enabled` — the live `catalog.enabled` gate.
- `queue` — refresh-queue depth; `scheduled` counts rows waiting in the future
  (backoff or the stale TTL).
- `albums` — how many materialized album rows carry each refresh status; every
  status is always present (zero-filled) so the card is stable.
- `artists.materialized` — materialized artist overviews.
- `engine` — effective interval/batch (defaults applied when the config values
  are zero), whether the drain loop is alive, and the most recent tick.
  `last_drain_at` is `null` until the first tick, and the whole block is zero
  (`interval_seconds`/`batch_size` `0`) when no engine is wired.

The queue fills **on demand only** — a cold album open, a stale warm read, an
album import, an explicit refresh, or the `warm-library` job. An idle engine
with an empty queue on a freshly upgraded instance is expected, not a fault.

**Errors**:
- `500` — `{"error": "catalog status unavailable"}` when a catalog read fails.
- `503` — `{"error": "catalog stats not available"}` when no catalog store is wired.

### `GET /api/catalog/unmatched`

**Admin only.**

Read-only listing of the albums parked with `refresh_status=unmatched`. The
status card only exposes the unmatched **count**; this endpoint returns the rows
behind it so an operator can inspect which albums are parked and why. It reads
the materialized catalog only — no provider calls, no queue changes.

**Response** `200` (up to 1000 rows, ordered by title then album id):
```json
[
  {
    "album_id": 4332,
    "artist_id": 682,
    "artist": "R.E.M.",
    "title": "1992 Fanclub Single",
    "status": "unmatched",
    "error": "no provider match",
    "updated_at": "2026-10-14T17:55:12Z"
  }
]
```

- `status` — always `unmatched` today; kept in the row so a future terminal
  listing can share the shape.
- `error` — the persisted `refresh_error` (omitted when empty).
- `updated_at` — when the parked metadata row last changed.
- Returns `[]` when nothing is parked.
- `X-Result-Truncated: true` is set when more than 1000 rows are parked; the body
  is then the first 1000 in title order. Compare with `albums.unmatched` from
  `GET /api/catalog/status` to detect truncation.

**Errors**:
- `500` — `{"error": "catalog unmatched unavailable"}` when the catalog read fails.
- `503` — `{"error": "catalog stats not available"}` when no catalog store is wired.

### `GET /api/covers/{albumID}`

Serve the `cover.jpg` image for an album.

**Path**: `albumID` — integer album ID

**Response**: `image/jpeg` binary

**Errors**:
- `400` — invalid album ID
- `404` — album not found or no cover image

---

## Playlists

### `GET /api/playlists/sources`

List available playlist source plugins.

**Response** `200`:
```json
[
  {"name": "deezer", "display": "Deezer"}
]
```

### `GET /api/playlists/sources/{source}`

Browse playlists from an external source.

**Path**: `source` — source name (e.g. `deezer`)

**Response** `200`:
```json
[
  {
    "id": "123456789",
    "name": "My Favorites",
    "track_count": 42,
    "cover_url": "https://..."
  }
]
```

### `GET /api/playlists`

List imported playlists.

**Response** `200`: `[Playlist, ...]`

Each playlist includes two derived (non-persisted) fields:
- `name_conflict` — `true` when another playlist from the same source shares this name. The UI shows a conflict badge.
- `folder_name` — the resolved on-disk folder name. On a name conflict the folder gets an ID suffix (e.g. `My Mix (a1b2c3d4)`) so the two playlists never share a directory.

Playlists also carry `added_by_user_id` / `added_by_username` (the account that
imported the playlist; `0` / `""` for system imports). DB-only, as above.

### `GET /api/playlists/{id}`

Get a single playlist with its tracks.

**Path**: `id` — integer playlist ID

**Response** `200`:
```json
{
  "playlist": {
    "id": 1,
    "source": "deezer",
    "source_playlist_id": "123456789",
    "name": "My Favorites",
    "track_count": 42,
    "cover_url": "https://...",
    "owner_name": "user123",
    "is_public": true,
    "auto_sync": false,
    "added_by_user_id": 3,
    "added_by_username": "alice"
  },
  "tracks": [
    {
      "playlist_id": 1,
      "position": 1,
      "track_id": 42,
      "source_track_id": "987654321",
      "title": "Get Lucky",
      "artist": "Daft Punk",
      "album": "Random Access Memories",
      "duration_ms": 369000,
      "isrc": "USQX91300105"
    }
  ]
}
```

### `POST /api/playlists/import`

Import a playlist from an external source.

**Request**:
```json
{
  "source": "deezer",
  "playlist_id": "123456789"
}
```

**Response** `200`:
```json
{
  "playlist": {"id": 1, "name": "My Favorites", ...},
  "tracks": [{"position": 1, "title": "Get Lucky", ...}],
  "linked": 15,
  "unmatched": 27
}
```

`linked` — tracks already matched to library. `unmatched` — tracks not yet in library.

### `POST /api/playlists/{id}/download-missing`

Queue downloads for unmatched playlist tracks.

**Path**: `id` — integer playlist ID

**Response** `200`:
```json
{"queued": 27}
```

### `POST /api/playlists/{id}/sync`

Sync a playlist with its source (triggers background import + re-match).
Runs through the job Manager: single-flight, cancellable, SSE progress,
persisted/restored on restart.

**Path**: `id` — integer playlist ID

**Response** `202` (job started) — same shape as every other job start:
```json
{"job": {"type": "sync", "state": "running", "progress": 0, "done": 0, "total": 0}, "started": true}
```

**Response** `200` when another job is already running:
```json
{"job": {...}, "started": false}
```

### `DELETE /api/playlists/{id}`

Delete an imported playlist.

**Path**: `id` — integer playlist ID

**Response** `200`:
```json
{"status": "deleted"}
```

---

## Tracking

**All tracking routes are Admin only.**

Tracked artists and their discovered discographies. A tracked artist is keyed
by a provider pair (`provider_name` + `provider_artist_id`); each discovered
album carries a `status` of `wanted` / `downloading` / `downloaded` /
`ignored`. Status is forward-only with one requeue: reconcile only promotes
`wanted` → `downloaded`, and the post-import chain promotes the matching
album to `downloaded` as soon as its download is imported (via
`TrackingLinkHandler` → `LinkImportedAlbum`) — so an album flips on import,
not only on the next refresh. A `downloading` album whose download is a
re-armable exhausted failure is reset to `wanted` and retried
(`downloading` → `wanted` is the only allowed regression); `downloaded` and
`ignored` are terminal. `ignored` is written only by the per-album PATCH
`status` field. Per-album `monitored`
toggles survive every refresh — `monitor_mode` is applied to existing albums
only by the explicit Set Artist Monitor action
(`PATCH /api/tracking/artists/{artistID}`). Refresh and search-missing do
provider I/O, so they only enqueue a runner on the shared `jobs.Manager`; every
provider call respects the shared per-provider cooldown (AGENTS §8). Every
tracking route returns `503` when the tracking service is not wired, and
`POST /api/tracking/artists` also returns `503` when the discovery provider is
cooling down.

### `GET /api/tracking/artists`

List every tracked artist.

**Response** `200`:
```json
[
  {
    "id": 1,
    "name": "Daft Punk",
    "provider_name": "deezer",
    "provider_artist_id": "27",
    "monitored": true,
    "monitor_mode": "all",
    "library_artist_id": 4,
    "auto_refresh": true,
    "last_refreshed_at": "2026-09-26T10:00:00Z",
    "created_at": "2026-09-01T09:00:00Z",
    "updated_at": "2026-09-26T10:00:00Z"
  }
]
```

### `POST /api/tracking/artists`

Start tracking an artist. **Synchronous**: it fetches the discography *before*
writing the artist row, reconciles it, and returns the resolved artist (`201`),
not a job. The call is bounded by a per-call timeout, so an unresponsive
provider fails the request and leaves no tracked artist behind. The response
includes `library_artist_id` when the artist is already in the local library.

**Body**:
```json
{
  "provider_name": "deezer",
  "provider_artist_id": "27",
  "name": "Daft Punk",
  "monitored": true,
  "monitor_mode": "all",
  "auto_refresh": true,
  "search_on_add": false
}
```
- `provider_name`, `provider_artist_id` — required
- `monitored` — optional, default `true`
- `monitor_mode` — optional, `all` (default) | `future` | `none`; seeds `monitored` on albums discovered by this call (`none` → `false`, `future` → only releases after the current year) and forces the artist's own `monitored=false` when `none`
- `auto_refresh` — optional, defaults to whether `tracking.refresh_mins` is enabled
- `search_on_add` — optional, default `false`. When `true`, one bounded
  `SearchMissing` run is triggered immediately after the reconcile (Lidarr's
  "Start Search for Missing Albums"). When `false`, adding an artist only builds
  the wanted list; albums are queued later by manual Search Missing, the
  `tracked-search` job, or the periodic refresh when
  `tracking.auto_search_missing` is enabled.

Idempotent: an existing provider pair is reconciled in place, never duplicated.

**Response** `201`: the created/updated tracked artist (same shape as above).

**Errors**:
- `400` — missing provider fields, invalid `monitor_mode`, or the discovery
  provider is not registered
- `503` — the discovery provider is currently cooling down (shared rate-limit
  bucket, AGENTS §8)
- `500` — any other failure, including the per-call timeout

### `GET /api/tracking/artists/{artistID}`

Get one tracked artist with its discovered albums.

**Response** `200`:
```json
{
  "artist": {
    "id": 1,
    "name": "Daft Punk",
    "provider_name": "deezer",
    "provider_artist_id": "27",
    "monitored": true,
    "monitor_mode": "all",
    "auto_refresh": true
  },
  "albums": [
    {
      "id": 10,
      "tracked_artist_id": 1,
      "provider_album_id": "302127",
      "provider_name": "deezer",
      "title": "Discovery",
      "year": 2001,
      "album_type": "album",
      "monitored": true,
      "status": "downloaded",
      "library_album_id": 7
    }
  ]
}
```

**Errors**: `404` tracked artist not found.

### `PATCH /api/tracking/artists/{artistID}`

Set Artist Monitor: update an artist's monitoring and re-apply `monitor_mode`
to its existing albums. At least one field is required; omitted fields keep
their current value.

**Body**:
```json
{"monitored": false, "monitor_mode": "future"}
```
`monitor_mode` is `all` | `future` | `none`; `none` forces the artist's
`monitored=false`. When `monitor_mode` changes it is applied to existing albums:
- `none` — unmonitors the artist's albums, except albums with status `ignored`,
  which keep their explicit skip
- `all` — re-monitors them
- `future` — monitors only releases with `year >` the current year

This is the only path that rewrites `monitored` on existing albums. The
background refresh never does, so per-album toggles survive every refresh until
the next Set Artist Monitor call.

**Response** `200`: the updated tracked artist.

**Errors**: `400` neither field sent or invalid `monitor_mode`; `404` not found.

### `DELETE /api/tracking/artists/{artistID}`

Delete a tracked artist; its discovered albums cascade (`ON DELETE CASCADE`).

**Response** `200`:
```json
{"status": "deleted"}
```

### `GET /api/tracking/artists/{artistID}/albums`

List every album discovered for a tracked artist.

**Response** `200`: array of tracked albums (shape as above; `[]` when none).

### `POST /api/tracking/artists/{artistID}/refresh`

Start the `tracked-refresh` job for one artist: re-fetch the discography,
reconcile it, then auto-search when `tracking.auto_search_missing` is on. The
auto-search uses the same rotating per-run batch as `search-missing` (at most
`searchBatchSize`, default 5 — never-searched first, then oldest
`last_searched_at`; the rest stay `wanted`).

**Response** `202`:
```json
{"job": {"type": "tracked-refresh", "state": "running"}, "started": true}
```
When another job is already running the current snapshot is returned with
`started: false` (`200`) — see [Job single-flight](#job-single-flight).

### `POST /api/tracking/artists/{artistID}/search-missing`

Start the `tracked-search` job for one artist: queue the artist's monitored
`wanted` albums (album-first, per-track fallback). **Draining**: the runner calls
the service repeatedly until the artist's wanted backlog is empty
(`Remaining == 0`), each pass processing at most `searchBatchSize` wanted albums
(default 5) ordered never-searched first, then oldest `last_searched_at` first,
bounded by `maxSearchPassesPerArtist` (50) and stopped early if the provider is
cooling down. An album that cannot be queued this pass (nothing found, or its
provider is cooling down) is counted as `skipped` and retried on a later pass —
it does not block the albums behind it.

A tracked album stuck in `downloading` whose download is a re-armable exhausted
failure is reset to `wanted` and retried; `downloading` → `wanted` is the only
allowed status regression.

Album-first vs per-track is the shared canonical policy implemented only by
`download.Service.QueueAlbumWithFallback`; the discover album-download handler
(`POST /api/discover/albums/{id}/download`) calls the same method. No endpoint
re-implements the policy.

The job summary reports `queued`, `skipped`, `no_source`, `errors`, and
`remaining`. `no_source` counts albums where neither album-first nor any
per-track input was available (typically `album_sources`/`download_client`
unset) — surfaced so a missing acquisition path is visible rather than a silent
skip.

**Response**: `202 {job, started:true}` or `200 {job, started:false}` as above.

### `POST /api/tracking/search-missing`

Start the `tracked-search` job for **every** tracked artist — the global "Search
all missing" action. It drains each artist's wanted backlog exactly like the
per-artist route (same batch cap, same one-rotation drain bound), skipping any
artist whose provider is cooling down; one artist's failure is logged and
counted but does not stop the run (the job is marked failed if any artist
failed).

It follows the `monitored` flag, not `auto_refresh`: an artist is searched when
it has monitored `wanted` albums, regardless of its auto-refresh setting. Its
job **message** is the aggregate shape
`searched N artists: queued X, skipped Y, no-source Z, errors E, remaining R`
(distinct from the per-artist `queued X, skipped Y, no-source Z, errors E,
remaining R`).

**Response**: `202 {job, started:true}` or `200 {job, started:false}` as above.

### `POST /api/tracking/refresh`

Start the bulk `tracked-refresh` job over every `auto_refresh` artist
(sequential; cooling-down providers skipped). Any auto-search it triggers is
bounded per run like `search-missing` (at most `searchBatchSize`, default 5 —
never-searched first, then oldest `last_searched_at`; the rest stay `wanted`).

**Response**: `202 {job, started:true}` or `200 {job, started:false}` as above.

### `GET /api/tracking/wanted`

List the globally wanted albums — monitored albums with status `wanted` or
`downloading` across all tracked artists.

**Response** `200`: array of tracked albums.

### `PATCH /api/tracking/albums/{albumID}`

Update a single album's monitoring and/or status. The `monitored` flag survives
every background refresh — only Set Artist Monitor re-applies `monitor_mode`.
The `status` field is the per-album skip control and the only writer of
`ignored`.

**Body**:
```json
{"monitored": true, "status": "ignored"}
```
- `monitored` — optional boolean; omitted keeps the current value
- `status` — optional; only `"wanted"` or `"ignored"` accepted. `"ignored"` is
  the explicit user skip; `"wanted"` clears it back into the search queue
- at least one of `monitored` / `status` is required

**Response** `200`:
```json
{"status": "updated", "id": 10, "monitored": true, "album_status": "ignored"}
```

**Errors**: `400` neither field sent or `status` not `wanted`/`ignored`;
`404` album not found.

### Job single-flight

Refresh and search-missing endpoints only enqueue a runner. The shared
`jobs.Manager` permits one running job at a time app-wide; a second request
while a job runs returns the current job with `started:false` (`200`) rather
than erroring. The scheduled refresh loop (`tracking.refresh_mins`) starts the
same `tracked-refresh` job and skips a busy tick; when
`tracking.auto_search_missing` is enabled the scheduled tick runs the combined
refresh + global search-missing drain. Progress streams over SSE —
see [SSE Streaming](#sse-streaming) — and the job snapshot carries a summary
**message** string: `refreshed N artists, skipped M cooling down` for a refresh,
`queued X, skipped Y, no-source Z, errors E, remaining R` for a per-artist
search, and `searched N artists: queued X, skipped Y, no-source Z, errors E,
remaining R` for the global drain (`remaining` is the wanted albums left after a
drain hit its rotation/safety bound — a deferral, not data loss). The underlying
`RefreshResult` / `SearchResult` structs are not surfaced in the snapshot.

> **Note:** manual and scheduled runs share the `tracked-refresh` job type.
> Both persist their snapshot to `job.json`, so an interrupted scheduled
> refresh is restored after restart exactly like a manual one.

---

## Common Patterns

### Pagination

Library endpoints accept query parameters:
- `q` — search query (case-insensitive substring match)
- `offset` — 0-based offset (default 0)
- `limit` — max results (default 200, capped at 1000)

### Error Responses

All errors follow this format:
```json
{"error": "human-readable message"}
```

HTTP status codes used: `200`, `201`, `202`, `400`, `401`, `403`, `404`, `405`, `409`, `429`, `500`, `503`.

- `401` — no valid credential (any `/api/*` route under `forms`).
- `403` — authenticated but not allowed (admin-only route reached by a `user`).
- `429` — per-client-IP rate limit exceeded; carries `Retry-After` and
  `X-RateLimit-Bucket` identifying the bucket.

### SSE Streaming

The `/api/events` endpoint uses standard Server-Sent Events protocol. Connect with:

```javascript
const es = new EventSource('/api/events');
es.addEventListener('download:progress', (e) => {
  const data = JSON.parse(e.data);
  console.log(data.progress + '%');
});
```

The server sends a `:heartbeat` comment every 15 seconds to keep the connection alive.

`/api/events` is available to any authenticated user. Two event classes are
admin-only and withheld from a regular user's stream: `log_line` and every
`job_*` event (`job_started`, `job_progress`, `job_completed`, `job_failed`,
`job_cancelled`). Download and import events are delivered to everyone.
