# Plan: Download Songs / Albums From the Library

## Overview

Let a client download actual audio files from the shared music library: one
track file, or a zipped album. The album zip also carries the album cover (when
present on disk) and any `.m3u` / `.m3u8` playlist already present in the album
directory (never generated). The library stays shared and read-only; the
endpoints only stream files already imported into `library_path`. Available to
every authenticated user (not admin-gated).

This plan folds in the review findings H4, H5, M7, M8 from
[README.md](README.md#security-findings-folded-into-the-plans).

## Current state (grounding)

- **Tracks already carry absolute server paths.** `domain.Track.FilePath`
  (`internal/domain/track.go:14`), persisted in `tracks`
  (`internal/library/sqlite/store.go:98`) and already exposed by
  `GET /api/library/tracks` (`internal/api/handlers_library.go:350`).
- **Library root** = `cfg.Library.LibraryPath` (`internal/config/config.go:33`,
  default `/music` at `config.go:142`).
- **Store access:** `Server.store library.Store` (`internal/api/handlers.go:45`):
  `GetTrack`, `GetAlbum`, `GetTracksByAlbum` (`internal/library/store.go`).
  `GetTracksByAlbum` is already `ORDER BY disc_number, track_number`
  (`library/sqlite/store.go:722`) — correct zip order for free.
- **Server timeouts:** `WriteTimeout: 30 * time.Second`
  (`internal/api/handlers.go:260`) — too short for large album zips (H4).
- **No download/stream endpoint exists today.** `/api/download*` is the
  *acquire* flow, unrelated.
- **Auth state for downloads:** the shared download SSE events (`download:*`,
  `import_*`) already reach all users; library file downloads are new and also
  shared. Don't route them through the admin-only event filter.

## Goals / Non-goals

**Goals**
- `GET /api/library/tracks/{trackID}/download` — stream one track file.
- `GET /api/library/albums/{albumID}/download` — stream a zip of the album.
- Album zip also contains the album cover (if present on disk) and any existing
  `.m3u` / `.m3u8` in the album directory (never generated; see Phase 2).
- Correct `Content-Type` + `Content-Disposition: attachment`.
- Path-traversal safe: never serve a path outside `library_path`.
- Range/resume for single files (`http.ServeContent`).
- Correct MIME for audio extensions without relying on `/etc/mime.types` (H5).
- Per-request write deadline so large zips aren't cut (H4).
- Audit each download with the caller's username (M8) and honor the shared
  rate-limit bucket.

**Non-goals**
- Artist-level or playlist-level zip.
- Transcoding / format conversion.
- Per-user download quotas or per-user history UI (audit only).
- Serving files outside `library_path` (e.g. staging) — deliberately refused.
- Generating or modifying an M3U — existing playlists are passed through as-is.
- Cover / M3U for single-track downloads (album zip only).

## Architecture

```
GET /api/library/tracks/{id}/download
        │  GetTrack(id)
        ▼
   FilePath != "" ? ──no──▶ 404
        │ yes
        ▼
   ResolveWithinRoot(libraryRoot, FilePath)   ← reject escape / symlink
        │
        ▼
   os.Open → http.ServeContent (Range, Last-Modified, Content-Length)
        │
        ▼
   access log (incl. username) + audit

GET /api/library/albums/{id}/download
        │  GetAlbum(id) + GetTracksByAlbum(id)   (disc, track order)
        ▼
   archive/zip → streaming writer (no temp files, chunked)
        │
        ▼
   access log (incl. username) + audit
```

### Path-safety helper (new, `internal/library/pathsafe.go`)

```go
// ResolveWithinRoot returns an absolute, symlink-resolved path guaranteed to
// live under root, or an error. Used before serving any library file over HTTP.
func ResolveWithinRoot(root, target string) (string, error)
```

Rules:
1. `abs := filepath.Clean(target)`; `rootAbs := filepath.Clean(root)`.
2. `rel, err := filepath.Rel(rootAbs, abs)`; reject `rel == ".."` or
   `strings.HasPrefix(rel, ".."+string(os.PathSeparator))`.
3. `filepath.EvalSymlinks(abs)` and repeat step 2 against the resolved root.
4. `os.Lstat` → reject non-regular files (no dirs, devices, symlinks).

### Audio MIME map (H5)

`mime.TypeByExtension` may return empty in the Docker image (no
`/etc/mime.types`). Provide an explicit map, falling back to
`mime.TypeByExtension`:

```go
flac → audio/flac   mp3 → audio/mpeg   m4a → audio/mp4
opus → audio/ogg    ogg → audio/ogg    wav → audio/wav   aac → audio/aac
```

## Tasks

### Phase 1: Track file stream endpoint
**Goal:** Download a single track.

- [ ] **1.1** Add `library.ResolveWithinRoot` + table-driven tests (`..`,
  absolute escape, nested-ok, symlink escape, non-regular file).
  - **Files:** `internal/library/pathsafe.go` (new), `internal/library/pathsafe_test.go`
  - **Estimate:** 1h
  - **Verification:** tests pass; all escape attempts rejected.

- [ ] **1.2** `GET /api/library/tracks/{trackID}/download` in
  `internal/api/handlers_library_download.go`:
  - `GetTrack`; 404 if missing / `FilePath == ""`; **do not leak server paths**
    in 403/404 bodies;
  - `ResolveWithinRoot(cfg.Get().Library.LibraryPath, t.FilePath)`;
  - `os.Open`; `http.ServeContent(w, r, name, modtime, f)` (Range + HEAD);
  - Content-Type via the audio MIME map (H5), fallback `mime.TypeByExtension`;
  - `Content-Disposition: attachment` with RFC 5987 `filename*` for non-ASCII.
  - **Files:** `internal/api/handlers_library_download.go` (new), route in `handlers.go`
  - **Estimate:** 2h
  - **Dependencies:** 1.1
  - **Verification:** 200 + bytes, 404 unknown, 404 empty path, traversal
    rejected, Range 206, correct Content-Type for `.flac`.

- [ ] **1.3** Register the route next to other `/api/library/*` routes
  (≈165-176). Shared (not `adminOnly`).
  - **Estimate:** 15m
  - **Verification:** route resolves; user allowed.

- [ ] **1.4 (H4) Extend the per-request write deadline.** In both download
  handlers, use `http.ResponseController` to clear/extend `WriteTimeout` for the
  duration of the response (e.g. `SetWriteDeadline(time.Time{})`), so a large
  album on a slow link is not cut at 30s. Keep a finite cap if desired.
  - **Files:** `internal/api/handlers_library_download.go`
  - **Estimate:** 30m
  - **Dependencies:** 1.2
  - **Verification:** a download longer than 30s completes (manual or a slow-reader test).

### Phase 2: Album zip stream endpoint
**Goal:** Download a whole album as one zip, including the cover and any
existing M3U.

- [ ] **2.1** `GET /api/library/albums/{albumID}/download`:
  - `GetAlbum` + `GetTracksByAlbum` (already disc/track ordered); skip tracks
    with empty `FilePath`; 404 if none remain;
  - `ResolveWithinRoot` every track; skip + log any that fail;
  - `Content-Type: application/zip`;
    `Content-Disposition: attachment; filename="<Artist> - <Album>.zip"` (sanitized);
  - `archive/zip.NewWriter(w)`; per entry `zip.FileHeader{Name, Method: zip.Deflate}`
    + `SetModTime`; `io.Copy` from the opened file; no temp files, no buffering;
  - **M7:** default entry name `"<NN> - <Title>.<ext>"`; for multi-disc albums
    prefix the disc (`"<DD>-<NN> - <Title>.<ext>"`); **deduplicate** colliding
    sanitized names (append ` (2)`, ` (3)` …) so extractors don't clobber;
  - on writer error (client disconnect) log and return.
  - **Files:** `internal/api/handlers_library_download.go`
  - **Estimate:** 2.5h
  - **Dependencies:** Phase 1
  - **Verification:** handler test unzips in-memory: entry count + names,
    disc prefix, no duplicate names; album with no files → 404.

- [ ] **2.2** Concurrency guard: cap simultaneous album zips (semaphore, size
  2-3); return 503 when saturated. Prevents a few large requests exhausting the
  process.
  - **Files:** `internal/api/handlers_library_download.go` / `handlers.go`
  - **Estimate:** 45m
  - **Dependencies:** 2.1
  - **Verification:** saturation returns 503; slot released after completion.

- [ ] **2.3 Include the album cover (conditional).** Resolve the album directory
  from the first track (`library.AlbumDirFromTrack`), then
  `library.CoverFilePath(albumDir)` (`scanner.go:120` — matches
  `cover.jpg/cover.png/folder.jpg/front.*`, any format). If non-empty, validate
  with `ResolveWithinRoot` and add it as a zip entry using its original basename
  (e.g. `cover.jpg`). No cover on disk → skip silently.
  This is a **lookup, not a fetch** — the download import already writes
  `cover.jpg` (`handler_cover.go`) and the scanner extracts sidecar/embedded art.
  - **Files:** `internal/api/handlers_library_download.go`
  - **Estimate:** 45m
  - **Dependencies:** 2.1
  - **Verification:** album with `cover.jpg` → entry present; album without →
    no entry, zip still succeeds.

- [ ] **2.4 Include existing `.m3u` / `.m3u8` (conditional, pass-through).**
  Never generate or modify one. Scan the album directory **and its
  subdirectories** for regular files ending in `.m3u` / `.m3u8`; for each,
  validate with `ResolveWithinRoot` and add it as a zip entry under its path
  **relative to the album dir** (preserve subdir layout so its own relative
  references keep working). None found → skip silently, zip still succeeds.
  - The file is copied **verbatim**; we do not rewrite its internal paths. If it
    references filenames that 2.1 deduped/renamed (M7), that is the file's
    author's concern — not ours.
  - **Files:** `internal/api/handlers_library_download.go`,
    `library.FindM3UFiles(dir) []string` helper (`internal/library/`)
  - **Estimate:** 45m
  - **Dependencies:** 2.1
  - **Verification:** album with `album.m3u` → entry present, byte-identical;
    album with none → no entry, zip still succeeds.

### Phase 3: Authz, rate limit, audit
**Goal:** Attribute and protect the endpoint.

- [ ] **3.1** Apply the existing shared rate-limit bucket to both routes
  (`withRateLimit("download", ...)`).
  - **Files:** `internal/api/handlers.go`
  - **Estimate:** 30m
  - **Verification:** burst triggers rate-limit response.

- [ ] **3.2 (M8) Per-user audit.** `withAccessLog` currently logs no identity
  (`handlers.go:348-359`). Either (a) add the resolved `Identity.Username` to
  the access-log line, or (b) write a `library_downloads` row
  (`id, user_id, kind, track_id, album_id, bytes, created_at, remote_addr`).
  (a) is minimal and covers every request; (b) gives queryable history. Pick one
  and document.
  - **Files:** `internal/api/handlers.go` (`withAccessLog`), or
    `internal/api/handlers_library_download.go` + additive table
  - **Estimate:** 1h
  - **Dependencies:** multi-user-roles Phase 2 (identity)
  - **Verification:** download produces a log line / row carrying the username.

### Phase 4: UI download actions
**Goal:** Buttons that don't bypass the API client.

- [ ] **4.1** In `client.ts` add same-origin URL builders
  `libraryTrackDownloadUrl(trackId)` / `libraryAlbumDownloadUrl(albumId)`
  returning `/api/library/.../download` **without** an `apikey` query (the
  session cookie is sent automatically; per multi-user Phase 0.2 the SPA no
  longer holds the key). Keeps URL knowledge in client.ts (AGENTS §12).
  - **Files:** `ui/src/api/client.ts`
  - **Estimate:** 30m
  - **Verification:** typecheck; `<a href>` download works logged in.

- [ ] **4.2** Track row action in `ui/src/features/library/TracksTable.tsx` and
  album action in `ui/src/features/library/AlbumDetailView.tsx`:
  `<a href={url} download>` (server streams; browser saves). Optional download
  icon in `AlbumCard.tsx`.
  - **Files:** `TracksTable.tsx`, `AlbumDetailView.tsx`, `AlbumCard.tsx`
  - **Estimate:** 1.5h
  - **Dependencies:** 4.1
  - **Verification:** manual: single track + full album download in browser.

### Phase 5: Docs
- [ ] **5.1** `docs/api.md`: both endpoints (params, headers, responses,
  errors), note only files under `library_path` are served, and that access is
  authenticated shared (not admin).
  - **Estimate:** 45m
  - **Verification:** docs match implementation.

## Testing strategy

- [ ] Unit: `ResolveWithinRoot` (escape, symlink, nested, non-regular).
- [ ] Unit: audio MIME map (`.flac`, `.mp3`, `.m4a`, unknown).
- [ ] Handler: track happy path, 404s, traversal → 403/404, Range 206, HEAD, Content-Type.
- [ ] Handler: album zip entries/names (disc prefix, dedupe); no files → 404; concurrency 503.
- [ ] Handler: album zip contains `cover.jpg` when present, omits it when absent.
- [ ] Handler: album zip includes an existing `.m3u`/`.m3u8` byte-identically;
  no file → no entry.
- [ ] Handler: auth required; regular user allowed; unauthenticated → 401.
- [ ] Manual: large album downloads past 30s; client cancel stops the stream.

## Total estimate

**Time:** ~13.5 hours (2.5h from H4/H5/M7/M8, +1.5h cover/M3U)
**Complexity:** Medium (security-sensitive: path handling + streaming)

## Notes / risks

- **Path traversal is the primary security risk.** Run the guard on the
  *resolved* path; `EvalSymlinks` the resolved root once; add a negative test per
  bypass technique.
- **Streaming, not buffering.** `http.ServeContent` gives Range/resume; the zip
  must stream directly to `w`.
- **H4:** don't just raise the global `WriteTimeout`; scope the deadline change
  to these two routes via `http.ResponseController`.
- **M8:** decide access-log field vs audit table; the access log is the simplest
  and covers all requests.
- **Do not leak server paths** in error bodies.
- **No transcoding** — serve original bytes; MIME from the explicit map.
- Only files resolving under `library_path` are served; staging/playlist dirs
  are out of scope.
- **Cover and M3U are album-zip only, and both are lookups — never generated,
  fetched, or rewritten.** Cover via `library.CoverFilePath`; M3U via a new
  `library.FindM3UFiles` scan of the album dir. If the album dir has no such
  file, it is simply absent from the zip.
- Download SSE events remain available to all users; do not add these endpoints
  to any admin-only filter.
