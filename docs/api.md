# Groovearr — API Reference

Base URL: `http://localhost:8008`

All responses are JSON. Errors use `{"error": "message"}`.

## Authentication & Authorization

Authentication is chosen by `auth.method`:

- `none` (default) — no login. Every request is treated as **admin**. This is
  **not access control**; only run it on a trusted network.
- `forms` — username/password login backed by the `users` table. Log in with
  `POST /api/login`, which sets the `groovearr_sid` session cookie.

With `forms`, every `/api/*` route requires an identity except `GET /api/health`
and `POST /api/login`. Three transports resolve an identity:

- **Session cookie** `groovearr_sid` — set by login; carries the account's role.
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

Clear the `groovearr_sid` session cookie.

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
- `password` — required, minimum 8 characters
- `role` — optional, `admin` | `user`; empty defaults to `user`

**Response** `201`: the created `userResponse`.

**Errors**:
- `400` — invalid body, missing username, password shorter than 8, or invalid role
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
- `400` — invalid user ID, invalid body, invalid role, or password shorter than 8
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

---

## Library

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

### `GET /api/library/artists`

List/search library artists.

**Query params**: `q`, `offset`, `limit`

**Response** `200`: `[Artist, ...]`

### `GET /api/library/albums`

List/search library albums.

**Query params**: `q`, `offset`, `limit`

**Response** `200`: `[Album, ...]`

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

### `POST /api/jobs/cancel`

**Admin only.**

Request cancellation of the running job.

**Response** `200`: the current `Job`

Job progress is also streamed to SSE clients (`/api/events`) as `job_started`,
`job_progress`, `job_completed`, `job_failed`, and `job_cancelled` events.

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

Start the `tracked-search` job: queue the artist's monitored `wanted` albums
(album-first, per-track fallback). **Bounded and rotating per run**: each pass
selects at most `searchBatchSize` wanted albums (default 5) ordered
never-searched first, then oldest `last_searched_at` first, and records the
search time. An album that cannot be queued this pass (nothing found, or its
provider is cooling down) is counted as `skipped` and retried on a later run —
it no longer blocks the albums behind it. The rest stay `wanted` and are picked
up by the next run. The job summary reports `Remaining`, the count still wanted
after the cap, so a whole discography is never dumped into the queue at once.

A tracked album stuck in `downloading` whose download is a re-armable exhausted
failure is reset to `wanted` and retried; `downloading` → `wanted` is the only
allowed status regression.

Album-first vs per-track is the shared canonical policy implemented only by
`download.Service.QueueAlbumWithFallback`; the discover album-download handler
(`POST /api/discover/albums/{id}/download`) calls the same method. No endpoint
re-implements the policy.

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
same `tracked-refresh` job and skips a busy tick. Progress streams over SSE —
see [SSE Streaming](#sse-streaming) — and the job snapshot carries a summary
**message** string: `refreshed N artists, skipped M cooling down` for a refresh
and `queued X, skipped Y, errors Z, remaining R` for a search-missing pass
(`R` is the wanted albums left after the per-run batch cap). The underlying
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

HTTP status codes used: `200`, `201`, `202`, `400`, `401`, `403`, `404`, `405`, `409`, `500`, `503`.

- `401` — no valid credential (any `/api/*` route under `forms`).
- `403` — authenticated but not allowed (admin-only route reached by a `user`).

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
