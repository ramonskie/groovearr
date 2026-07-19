# Groovearr — API Reference

Base URL: `http://localhost:8008`

All responses are JSON. Errors use `{"error": "message"}`.

## Health & Config

### `GET /api/health`

Health check.

**Response** `200`:
```json
{"status": "ok"}
```

### `GET /api/config`

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
  }
}
```

### `PUT /api/config`

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
    "playlist_id": "5"
  }
]
```

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
    "created_at": "2026-07-19T12:00:00Z",
    "updated_at": "2026-07-19T12:00:00Z"
  }
]
```

### `GET /api/library/artists`

List/search library artists.

**Query params**: `q`, `offset`, `limit`

**Response** `200`: `[Artist, ...]`

### `GET /api/library/albums`

List/search library albums.

**Query params**: `q`, `offset`, `limit`

**Response** `200`: `[Album, ...]`

### `POST /api/library/scan`

Trigger a filesystem scan of the library path. Imports new files and skips duplicates
(by file path).

**Response** `200`:
```json
{
  "scanned": 150,
  "imported": 12,
  "skipped": 138,
  "errors": 0,
  "paths": ["/music"]
}
```

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
    "auto_sync": false
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

**Path**: `id` — integer playlist ID

**Response** `202`:
```json
{"status": "syncing"}
```

### `DELETE /api/playlists/{id}`

Delete an imported playlist.

**Path**: `id` — integer playlist ID

**Response** `200`:
```json
{"status": "deleted"}
```

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

HTTP status codes used: `200`, `202`, `400`, `404`, `500`, `503`.

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
