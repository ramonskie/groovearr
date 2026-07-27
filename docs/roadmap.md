# Groovearr — Feature Roadmap & Gap Analysis

> What Groovearr has vs what original SoulSync 2.x provided.
> Tiers ordered by priority. Status: ✅ Done | 🟡 Partial | ❌ Missing

---

## Architecture Philosophy

Groovearr uses a **plugin-first** architecture. Download sources and metadata providers
are equal peers behind common interfaces. No single source is privileged.

**Two paths to a complete library:**

| Path | Download | Metadata (covers, ISRC, tags) | Cost |
|------|----------|------------------------------|------|
| **Free** | slskd (Soulseek P2P) | MusicBrainz / Cover Art Archive / Last.fm | $0 |
| **Premium** | Deezer ARL (+ Spotify later) | Built-in (rich metadata from streaming APIs) | ~$12/mo |

The plugin system supports mixing: use slskd for downloads + Deezer ARL for metadata,
or go fully free with slskd + MusicBrainz. Each path should produce an equally complete
library (covers, tags, organized files).

---

## Tier 0: MVP — Already Implemented

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 1 | Search via slskd (Soulseek P2P) | ✅ | REST API polling, album grouping |
| 2 | Download via slskd | ✅ | Transfer tracking, cancel, clear |
| 3 | Search Deezer metadata | ✅ | Public API, advanced query syntax |
| 4 | Download via Deezer (ARL — premium) | ✅ | Blowfish decrypt, quality fallback. Requires paid Deezer account. |
| 5 | Quality-based result scoring | ✅ | Format + bitrate + upload speed |
| 6 | Track metadata matching | ✅ | Bigram + version aware + CJK |
| 7 | Library DB (artists/albums/tracks) | ✅ | SQLite, 3 tables, external ID cols |
| 8 | File organization (scanner) | ✅ | Path heuristic import, dedup |
| 9 | Download status tracking | ✅ | Polling UI, speed/progress bars |
| 10 | Cross-source hybrid search | ✅ | Merge all plugins, best-match |
| 11 | Cross-source fallback download | ✅ | Search all, score, pick best |
| 12 | Cancel/clear downloads | ✅ | Per-plugin cancel + remove |
| 13 | Config management | ✅ | JSON persistence, thread-safe, UI |
| 14 | Plugin architecture | ✅ | Interface + registry + aliases |
| 15 | Web UI (embedded SPA) | ✅ | React + TypeScript, all pages |

---

## Tier 1: Core Quality — Fix Current Gaps

Small but impactful fixes to what's already built.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 16 | **Post-download file renaming** — apply `folder_template` via `PathResolver` after download completes | ✅ Done | S | PathResolver (done) |
| 17 | **Cover art download hook** — post-processing hook fetches album/artist covers, caches as `cover.jpg` in album dir, populates `thumb_url` in DB. Currently only Deezer provides `CoverURL` — needs free metadata providers (see Tier 1.5) for slskd-only users. | ✅ Done | M | PostProcessor (done), Deezer API (done) |
| 18 | **Audio metadata parsing** — read ID3/FLAC/Vorbis tags in scanner instead of path-only heuristics | ✅ Done | M | `dhowden/tag` |
| 19 | **Tag writing** — embed metadata into downloaded files (artist, album, title, track#, cover art) | ✅ Done | L | `bogem/id3v2`, `go-flac` |
| 20 | **Config validation** — validate URLs, quality values, path existence at save time | ✅ Done | S | — |
| 21 | ~~DB migration versioning~~ — removed; pre-release simplification, schema is flat idempotent DDL | ✅ Done | S | — |
| 22 | **Wire `QualityConfig`** — actually use `min_bitrate` and `preferred_format` in download filter logic | ✅ Done | S | — |
| 23 | **Use central `download.Engine`** — Engine wired into orchestrator, available for future queue/bandwidth features | ✅ Done | S | — |
| — | **Quality Profiles** — named profiles with ranked format fallback chains (e.g. FLAC → MP3-320 → MP3-128). Replaces per-source quality settings. Profile CRUD, ranked target editor, upgrade-until cutoff. `internal/quality/` with SQLite store + ranker + integration tests. | ✅ Done | L | QualityConfig (done), Settings UI |
| — | **Replace lower quality on import** — toggle to auto-replace existing tracks when a higher-quality version is downloaded. Per-profile setting. `UpgradeOrchestrator` in `internal/download/upgrade.go`. | ✅ Done | M | Quality Profiles |
| 24 | **Artist unique constraint** — add UNIQUE on `artists.name` to prevent duplicates | ✅ Done | S | — |
| 25 | **Library pagination + search** — API query params + UI search/filter beyond 200 limit | ✅ Done | M | — |
| 26 | **Album-art display in UI** — `<img>` tags in library views, `GET /api/covers/{id}` proxy endpoint | ✅ Done | M | Cover art hook |

---

## Tier 1.5: Metadata Providers — Free-Tier Backbone

Metadata plugins are separate from download plugins. A download source provides files;
a metadata provider enriches them with covers, ISRC codes, genres, and external IDs.
This tier enables the fully free path (slskd + free metadata = complete library).

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 27 | **Metadata provider plugin interface** — `Provider` interface (extends `plugin.BasePlugin`): `SearchCover(artist, album)`, `SearchAlbum(artist, title)`, `SearchArtistImage(artist)`, `EnrichTrack(track)`. Registry-based, config-driven. Optional interfaces: `CoverArtArchiveProvider`, `ArtistMetadataProvider`, `LyricsProvider`. | ✅ Done | M | Plugin architecture (done) |
| 28 | **Cover Art Archive provider** — free cover art via MusicBrainz Cover Art Archive. Search by MBID. No auth required. Fills `CoverURL` for slskd downloads. | ✅ Done | M | Metadata provider interface, MusicBrainz ID lookup |
| 29 | **MusicBrainz metadata provider** — artist/album/release IDs, ISRC enrichment, genre/label/release-date via release lookup. Album lookup via aggregate-frequency across 100 recordings (finds original album among compilations). Rate-limited API client (1 req/s). Backbone of free-tier metadata. | ✅ Done | L | Metadata provider interface |
| 30 | **Deezer metadata provider** — uses public `api.deezer.com/search` (no ARL, no auth). `SearchAlbum` via field-scoped `search/track`, `SearchCover` returns CDN cover URLs directly. Integrated into existing Deezer plugin — metadata always available, downloads need ARL. Much better album resolution than MusicBrainz alone (~85% vs ~30% correct). | ✅ Done | M | Metadata provider interface |
| 31 | **Metadata enrichment pipeline** — `MetadataEnrichmentHandler` in download post-processing chain. Runs all configured metadata providers against imported tracks: cover art fetch (CAA + Deezer CDN), ISRC/genre/label enrichment (MusicBrainz), album title resolution when missing, re-tagging with new metadata. Syncs `thumb_url` from on-disk `cover.jpg`. | ✅ Done | M | Metadata provider interface + registry |
| 32 | **Configurable metadata provider order** — `metadata_order` config field (`["deezer", "musicbrainz", "coverartarchive"]`). Resolver and enrichment handler query providers in specified priority. Unlisted providers fall to end. | ✅ Done | S | Metadata resolver + enrichment handler |
| 33 | **Comma-separated artist handling** — Spotify free mode returns co-artist strings like `"The Moon, DJ Ghost"`. Resolver tries full artist first, then primary artist (before first comma) as fallback for both album and cover lookup. | ✅ Done | S | Metadata resolver |
| 34 | **Last.fm metadata provider** — artist images, similar artists, tags, biographies. Free API key. | ✅ Done | M | Metadata provider interface, Last.fm API key |
| — | **iTunes Search API provider** — free cover art + metadata fallback. No auth, good for mainstream releases not in CAA. | 🟡 Medium | S | Metadata provider interface |

**Goal**: After Tier 1.5, a user with only slskd configured gets:
- ✅ Cover art (`cover.jpg` in album dirs)
- ✅ Artist images and bios (via Last.fm provider)
- ✅ ISRC codes for dedup
- ✅ Genre tags
- ✅ Album grouping by MusicBrainz release ID

---

## Tier 2: Additional Download Sources

Plugin interface makes adding new sources straightforward.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 33 | **YouTube downloads** — via yt-dlp subprocess or API wrapper | 🟡 Medium | L | `yt-dlp` binary or Go yt-dlp lib |
| 34 | **Tidal downloads** — OAuth device auth, search + download + quality selection | 🟡 Medium | L | Tidal API (`tidalapi` port) |
| 35 | **Qobuz downloads** — REST API auth, search + download | 🟢 Low | L | Qobuz API |
| 36 | **SoundCloud downloads** — yt-dlp extractor or direct API | 🟢 Low | M | — |
| 37 | **Torrent downloads (Prowlarr)** — Prowlarr API → qBittorrent/Transmission | 🟢 Low | L | Prowlarr + torrent client |
| 38 | **Usenet downloads (Prowlarr)** — Prowlarr → SABnzbd/NZBGet | 🟢 Low | L | Prowlarr + usenet client |
| 39 | **Lidarr integration** — use Lidarr as download source via its API | 🟢 Low | M | Lidarr instance |
| 40 | **Direct URL download** — paste Tidal/Qobuz track URLs directly | 🟢 Low | S | Tidal/Qobuz clients |

---

## Tier 3: Library & Media Server Integration

Auto-sync library state with external media servers.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 41 | **Plex integration** — scan library, detect missing tracks, trigger refresh | 🟡 Medium | L | Plex API |
| 42 | **Jellyfin integration** — same as Plex but against Jellyfin API | 🟡 Medium | L | Jellyfin API |
| 43 | **Navidrome integration** — Subsonic API for library sync | 🟡 Medium | L | Subsonic API |
| 44 | **Library duplicate detection** — SHA256 content hash, filename fuzzy match | 🟡 Medium | M | File scanner |
| 45 | **Library issues dashboard** — show missing tracks, dupes, stale files, tag mismatches | 🟢 Low | L | Scanner + store |
| 46 | **Listening stats page** — play counts, top artists, recent additions | 🟢 Low | M | Plex/Jellyfin play history |
| 47 | **M3U playlist export** — export library playlists as .m3u files | 🟢 Low | S | — |

---

## Tier 4: Playlists & Discovery

Spotify integration for playlist import/sync, artist following, and discovery.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 48 | **Spotify OAuth** — login flow (PKCE), token refresh (auto on 401), scoped access. Persists tokens to config, rebuilds plugin on connect. | ✅ Done | M | Playlist framework |
| 49 | **Deezer playlist import** — import playlists via ARL, download tracks, separate playlist folder | ✅ Done | M | Playlist framework |
| 50 | **Playlist sync** — refresh (detect additions/removals/reordering), discover missing tracks (ISRC → fuzzy → unmatched), download pipeline (two-phase queue+resolve), playlist folder builder with cleanup | ✅ Done | L | Playlist service |
| 51 | **Playlist explorer UI** — browse, import, sync, track view | ✅ Done | M | Handlers + UI |
| 52 | **Artist watchlist** — follow artists, get notifications of new releases | 🟡 Medium | L | Spotify/Deezer APIs |
| 53 | **Automatic watchlist downloads** — new releases auto-downloaded | 🟡 Medium | M | Watchlist + download pipeline |
| 54 | **Wishlist / retry queue** — two-phase batch-queue pipeline (✅), Pending tab with Active/Queue sections (✅), orphan recovery on restart (✅). `POST /api/downloads/{id}/retry` endpoint (✅), Retry button in UI (✅), auto-retry loop with exponential backoff (✅). Missing: **stuck pending timeout** — records with missing artist/title metadata loop forever in `queued` state instead of auto-failing. See #B5. | 🟡 In Progress | M | Timeout guard for unresolved pending records |
| 55 | **Discovery pool** — AI-curated recommendations from Spotify/Deezer/Last.fm | 🟢 Low | L | Multiple APIs |
| 56 | **Personalized playlists** — Daily Mix, Discover Weekly, Release Radar sync | 🟢 Low | L | Spotify OAuth |
| 57 | **Beatport charts** — top charts imported for discovery | 🟢 Low | M | Web scraping |

---

## Tier 5: Metadata Enrichment

Rich metadata from external services. Reprioritized — free services (MusicBrainz, CAA, Last.fm)
moved to Tier 1.5 as foundational infrastructure. Remaining items are premium/nice-to-have.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 58 | **Last.fm scrobbling** — scrobble plays, fetch similar artists/tags | 🟡 Medium | M | Last.fm API |
| 59 | **ListenBrainz scrobbling** — open alternative to Last.fm | 🟢 Low | M | ListenBrainz API |
| 60 | **Genius lyrics** — fetch + embed lyrics into audio files | 🟢 Low | L | Genius API |
| 61 | **Discogs metadata** — release info, catalog numbers | ✅ Done | M | Discogs API |
| 62 | **AudioDB metadata** — artist images, bios, genre tags | 🟢 Low | S | AudioDB API |

---

## Tier 6: Automation Engine

Scheduled background tasks for hands-off operation.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 63 | **Automation engine** — cron-like scheduler for background tasks | 🟡 Medium | L | Task queue |
| 64 | **Playlist auto-sync** — scheduled refresh of playlists with `AutoSync=true`. Background goroutine runs on configurable interval (default 30 min), re-links tracks against upstream source, rebuilds playlist folder. New tracks stay unmatched until manually downloaded. | ✅ Done | M | Playlist service |
| 65 | **Watchlist auto-scan** — scheduled watchlist check for new releases | 🟡 Medium | M | Automation + watchlist |
| 66 | **Wishlist auto-process** — scheduled retry of failed downloads | 🟡 Medium | M | Automation + wishlist |
| 67 | **Library auto-scan** — scheduled filesystem scan for new/missing files | 🟡 Medium | S | Automation + scanner |
| 68 | **Personalized playlist auto-refresh** — scheduled Daily Mix/Discover Weekly sync | 🟢 Low | M | Automation + Spotify |
| 69 | **Cleanup tasks** — auto-remove completed downloads, duplicate cleanup | 🟢 Low | M | Automation + library |

---

## Tier 7: Platform & Operations

Deployment, security, and operational concerns.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 70 | **Authentication** — Sonarr-style auth with API key + Forms login. Auto-generated UUID API key via `X-Api-Key`/`?apikey=`/`Bearer`. Forms: username+password → bcrypt → session cookie (7-day sliding). Security tab in settings: method picker, credentials, CIDR subnet bypass, API key display. Login page with auth guard. Rate-limited login (5/min). Session store with `HttpOnly` cookies, `Secure` flag on TLS. | ✅ Done | L | — |
| 71 | **Docker image + docker-compose with slskd** — one-command `docker compose up` for the free path. Multi-stage build (golang → alpine), three named volumes, slskd sidecar. | ✅ Done | M | — |
| 72 | **Multi-profile support** — separate libraries/configs per profile | 🟢 Low | L | Auth |
| 73 | **Setup wizard** — first-run guided config (choose free/premium path) | 🟡 Medium | M | — |
| 74 | **Systemd service** — service file + install target | 🟡 Medium | S | — |
| 75 | **Download queue prioritization** — priority tiers, bandwidth limits | 🟢 Low | L | Download engine refactor |
| 76 | **Resume partial downloads** — checkpoint + resume for large files | 🟢 Low | L | Download engine refactor |
| 77 | **Lossy conversion** — transcode FLAC → MP3 via ffmpeg for Plex | 🟢 Low | M | ffmpeg binary |
| 78 | **ReplayGain scanning** — loudness analysis + tag writing | 🟢 Low | L | Audio analysis lib |
| 79 | **Content quarantine** — import safety review before adding to library | 🟢 Low | M | Library + UI |
| 80 | **Public REST API v1** — documented, versioned API for external tools | 🟢 Low | L | Auth + API spec |
| 81 | **Custom scripts** — user-defined automation scripts | 🟢 Low | L | Automation engine |

---

## Summary

| Tier | Name | Count | Status |
|------|------|-------|--------|
| 0 | MVP | 15 features | ✅ 15/15 |
| 1 | Core Quality | 13 features | ✅ 13/13 |
| 1.5 | Metadata Providers | 8 features | ✅ 8/8 |
| 2 | Download Sources | 8 features | ❌ 0/8 |
| 3 | Library & Media Servers | 7 features | ❌ 0/7 |
| 4 | Playlists & Discovery | 10 features | 🟡 4 done, 6 remaining |
| 5 | Metadata Enrichment | 5 features | 🟡 1/5 (Discogs done) |
| 6 | Automation | 7 features | 🟡 1/7 (auto-sync done) |
| 7 | Platform & Ops | 12 features | 🟡 2/12 (70,71 done; 72-81 remain) |
| **Total** | | **85 features** | **44 done, 41 remaining** |

## Known Bugs

| # | Component | Severity | Description |
|---|-----------|----------|-------------|
| B1 | Deezer | ✅ Done | **`Test Connection` false positive with invalid ARL.** Now verifies `license_token` is non-empty in addition to `USER_ID != 0`. Fixed in `internal/providers/deezer/authenticate.go`. |
| B2 | Config | ✅ Done | **Masked secret round-trip corruption.** `Merge()` now detects masked strings and preserves original values. Fixed in `internal/config/config.go:90-160`. |
| B3 | Config (design) | 🟡 Medium | **`Merge()` replaces entire source objects.** Any field not sent by the frontend (e.g., `tokens`, `allow_fallback`) is lost on save. Server-managed fields like OAuth tokens can't coexist safely with user-editable config without deep-merge. |
| B4 | Playlist / Download | ✅ Done | **Queued tracks now visible.** `DownloadMissing` uses two-phase batch-queue → background-resolve. `QueuePending` inserts all tracks immediately with `source=pending`. Pending tab shows queue via SSE. `RecoverOrphans` recovers stuck records on restart. |
| B5 | Playlist / Download | 🟡 Medium | **Stuck pending timeout for missing metadata.** Records with `source=pending` AND empty `artist`/`title` loop forever in `resolvePendingSources` (skipped, never transitioned). Records with valid metadata that fail search ARE handled (auto-transition to `failedPending` + retry loop). Fix: auto-fail records stuck in `queued`+`pending` state for >15 minutes. |
| B6 | Discover / Download | 🟡 Medium | **Discover album download should use two-phase pattern.** Currently does synchronous search+queue per track. Should batch-queue all first (like playlists) for instant visibility. |
| B25 | Metadata | ✅ Done | **Resolver guard prevents album lookup.** `resolvePendingDownloads` required `rec.Album != ""` to call `EnrichMetadata`, but `EnrichMetadata` was designed to FIND the album when missing. Guard changed to `rec.Artist != "" && rec.Title != ""` (`internal/playlist/service.go`). |
| B26 | Metadata | ✅ Done | **"Unknown Album" hardcoded fallback.** `extractMetadata` in `LibraryImporterHandler` defaulted to `library.DefaultAlbumTitle` ("Unknown Album") before enrichment handler could resolve it. Removed fallback, album stays empty until enrichment (`internal/download/handler_library.go`). |
| B27 | Metadata | ✅ Done | **Enrichment handler never resolved album title.** `MetadataEnrichmentHandler` updated ISRC, genres, covers but not the album title itself. Added `SearchAlbum` call in provider loop when `album.Title == ""` (`internal/download/handler_enrichment.go`). |
| B28 | Metadata | ✅ Done | **Worker progress polling zeroed `cover_url`.** `UpdateProgress` always set `cover_url=''` during download, wiping the cover URL set at queue time. Changed to `CASE WHEN ? = '' THEN cover_url ELSE ? END` — preserves existing when empty passed (`internal/download/sqlite/store.go`). |
| B29 | Metadata | ✅ Done | **`thumb_url` never persisted when cover already exists.** CoverArtHandler (step 3) downloads `cover.jpg` but `updateAlbumThumb` fails (album not in library yet). Enrichment handler (step 6) skips `ThumbURL` update because cover already on disk. Added sync: if `cover.jpg` exists, set `thumb_url` regardless of who downloaded it (`internal/download/handler_enrichment.go`). |
| B30 | Metadata | ✅ Done | **MusicBrainz returned compilations instead of original albums.** `SearchRecording` picked first recording's first release title (often a compilation). Rewrote to aggregate release-group titles across 100 recordings, pick most frequent Album type. For tracks with 20+ recording matches, original album beats compilations (`internal/providers/musicbrainz/api.go`). |
| B31 | Metadata | ✅ Done | **Comma-separated Spotify artist strings failed lookup.** Spotify free mode returns co-artist strings like `"The Moon, DJ Ghost"`. Deezer/MusicBrainz search fails on full string. Added `primaryArtist` fallback: try full artist, then first segment before comma (`internal/metadata/resolver.go`). |
| B32 | Download / Domain | 🟡 Medium | **TrackID not propagated through cross-provider retries.** `resolveRetrySource` (monitor) and playlist's `resolvePendingDownloads` update `rec.Filename` from search results but never set `rec.TrackID`. Deezer relies on `TrackID` in `DownloadMeta`; cross-provider retries from non-Deezer sources hit Deezer's Filename-parsing fallback (`"trackID\|\|display"`). Fix: add `TrackID` field to `SearchResult` / `TrackResult` so all download paths can propagate source-specific IDs without string parsing. |
| B7 | Download / UI | 🟢 Low | **Server-side pagination for download list.** `GET /api/downloads` returns all records. Large queues (1000+) should paginate with `?limit=` and `?offset=`. |
| B8 | Observability | ✅ Done | **No structured logging / request tracing.** Replaced `log.Printf` with `log/slog` throughout codebase. Added request ID middleware (`withRequestID`) for HTTP correlation. Fixed in commit `1e6b8de`. |
| B9 | API | ✅ Done | **No HTTP server timeouts configured.** `http.Server` now has Read=10s, Write=30s, Idle=120s timeouts set in `internal/api/handlers.go:148-150`. |
| B10 | Designer | ✅ Done | **Download dispatch now handled by MonitoringService.** Old two-phase `SetWorkerPool()` pattern removed. `DownloadService` is a thin API layer — `MonitoringService` scans DB for queued records and drives the state machine via provider polling. |
| B11 | Code Quality | 🟡 Medium | **Silent error drops throughout codebase.** `_ = store.UpdateProgress(...)` in worker.go, scanner errors lost in `handleLibraryScan`, "not found" vs "DB down" indistinguishable. Standardize: wrap errors with context, don't use `_` for important operations. |
| B12 | Download | 🟡 Medium | **FileRenamer.resolveSourcePath blocks without context/timeout.** `filepath.WalkDir` over entire download root with no cancellation, no max depth, no file limit. Large directories block import chain. |
| B13 | Discovery | 🟡 Medium | **Buggy error propagation in search handler.** `SearchArtists` success + `SearchAlbums` failure → only album error reported, artist success hidden. Track errors separately or use `errors.Join`. |
| B14 | API | ✅ Done | **No API rate limiting.** Search, download, scan, and login endpoints now IP-rate-limited with configurable rates via env vars (`RATE_SEARCH`, `RATE_DOWNLOAD`, `RATE_SCAN`, `RATE_LOGIN`). Fixed in commit `29e5901`. |
| B15 | Download | ✅ Done | **Shutdown drops queued jobs silently.** Old `workerPoolImpl.Shutdown()` closed `jobQueue` — remaining items lost. Replaced by `MonitoringService` ticker-based polling which scans DB for queued records on every tick. No job queue to drain. Orphan recovery handles crash-restart. |
| B16 | Playlist | 🟢 Low | **Dead code: `Service.findAndQueueDownload`.** Defined but never called. Actual resolution uses `resolvePendingDownloads` with `orch.FindBestMatch`. |
| B17 | Download | ✅ Done | **Removed worker pool — replaced by MonitoringService.** Old pool had hardcoded default of 3 workers. New `MonitoredProvider` model uses per-plugin `MaxConcurrent()` for fine-grained concurrency control. Global concurrency is bounded by individual provider limits. |
| B18 | Download | 🟢 Low | **Weak download ID generation.** Uses `math/rand` (not crypto-grade) + 4 hex digits (65k entropy) + nanosecond timestamp. Collisions unlikely but not impossible. Switch to UUID. (`google/uuid` already in go.sum as indirect.) |
| B19 | SSE | 🟢 Low | **No SSE client limit.** `SSEHub.clients` unbounded. Many idle clients exhaust file descriptors. |
| B20 | API | 🟢 Low | **Health endpoint returns no component status.** `GET /api/health` returns `{"status":"ok"}` only — no DB ping, no plugin health, no goroutine/queue stats. Load balancer can't distinguish healthy from degraded. |
| B21 | Download | 🟢 Low | **No cleanup of terminal download records.** `DeleteTerminal()` exists in store but never called. Completed/failed/ignored downloads accumulate indefinitely. |
| B22 | Code Quality | 🟢 Low | **main.go growing linearly with features.** Each new plugin adds ~5 lines. At 10+ plugins becomes unwieldy. Consider `App` struct with builder pattern. |
| B23 | Download | 🟢 Low | **No download retention policy.** Terminal records accumulate forever. Add configurable retention (auto-clean records older than N days). |
| B24 | Settings / Providers | 🟡 Medium | **Provider connection badges not persisted.** After testing a connection, the badge shows "Connected" but reverts to "Configured" when returning to the settings page or restarting the app. Connection state is transient (in-memory only). Should persist last-known status or re-test on page load. |

### Immediate Next Steps

1. **Structured logging** — replace `log.Printf` with `log/slog`, add request IDs (#B8, ✅ Done)
2. **HTTP server timeouts** — set Read/Write/Idle timeouts (#B9, ✅ Done)
3. **Authentication** — forms login + API key + security settings (#70, ✅ Done)
4. **API rate limiting** — protect downstream providers from excessive calls (#B14, ✅ Done)
5. **Worker pool init fix** — constructor now accepts pool directly (#B10, ✅ Done)
6. **iTunes Search API provider** — free cover art + metadata fallback (#—, Tier 1.5, 🟡 Medium)
7. **Last.fm metadata provider** — artist images, similar artists, tags (#34, Tier 1.5, ✅ Done)
8. **Wishlist retry + UI** — endpoint (✅), Retry button (✅), auto-retry loop (✅). Missing: stuck pending timeout (#B5).
9. **Stuck pending timeout** — auto-fail records stuck in `queued`+`pending` for >15 minutes (#B5, 🟡 Medium)

### First Major Feature Block (Platform hardening + Tier 1.5 completion + Tier 7 security)

1. **Observability foundation** — structured logging (#B8 ✅), HTTP timeouts (#B9 ✅), health endpoint (#B20), rate limiting (#B14 ✅)
2. **Free-tier metadata completion** — iTunes provider to round out free path coverage
3. **Authentication** — forms login + API keys + security settings (#70 ✅, Tier 7)
4. **Wishlist polish** — retry endpoint, retry UI, stuck pending detection (#54 gap + #B5)
5. **Systemd service** — production deployment target (#74, Tier 7)
