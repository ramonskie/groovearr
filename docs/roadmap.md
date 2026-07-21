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
| 21 | **DB migration versioning** — version-tracked schema with `schema_version` table | ✅ Done | S | — |
| 22 | **Wire `QualityConfig`** — actually use `min_bitrate` and `preferred_format` in download filter logic | ✅ Done | S | — |
| 23 | **Use central `download.Engine`** — Engine wired into orchestrator, available for future queue/bandwidth features | ✅ Done | S | — |
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
| 27 | **Metadata provider plugin interface** — `MetadataProvider` interface (separate from `download.Plugin`): `SearchCover(artist, album) *CoverResult`, `EnrichTrack(track) *Metadata`. Registry-based, config-driven. | 🔴 High | M | Plugin architecture (done) |
| 28 | **Cover Art Archive provider** — free cover art via MusicBrainz Cover Art Archive. Search by MBID or artist+album. No auth required. Fills `CoverURL` for slskd downloads. | 🔴 High | M | Metadata provider interface, MusicBrainz ID lookup |
| 29 | **MusicBrainz metadata provider** — artist/album/release IDs, track listings, AcoustID fingerprinting. Backbone of free-tier metadata. | 🔴 High | L | Metadata provider interface |
| 30 | **iTunes Search API provider** — free cover art + metadata fallback. No auth, good for mainstream releases not in CAA. | 🟡 Medium | S | Metadata provider interface |
| 31 | **Metadata enrichment pipeline** — background workers that run metadata plugins against new library additions. Post-import hook that queries all registered metadata providers. | 🔴 High | M | Metadata provider interface + registry |
| 32 | **Last.fm metadata provider** — artist images, similar artists, tags, biographies. Free API key. | 🟡 Medium | M | Metadata provider interface, Last.fm API key |

**Goal**: After Tier 1.5, a user with only slskd configured gets:
- ✅ Cover art (`cover.jpg` in album dirs)
- ✅ Artist images and bios
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
| 48 | **Spotify OAuth** — login flow, token refresh, scoped access | 🟡 In Progress | M | Playlist framework |
| 49 | **Deezer playlist import** — import playlists via ARL, download tracks, separate playlist folder | ✅ Done | M | Playlist framework |
| 50 | **Playlist sync** — refresh, discover missing, download pipeline | 🟡 In Progress | L | Playlist service |
| 51 | **Playlist explorer UI** — browse, import, sync, track view | ✅ Done | M | Handlers + UI |
| 52 | **Artist watchlist** — follow artists, get notifications of new releases | 🟡 Medium | L | Spotify/Deezer APIs |
| 53 | **Automatic watchlist downloads** — new releases auto-downloaded | 🟡 Medium | M | Watchlist + download pipeline |
| 54 | **Wishlist / retry queue** — download queue with Pending tab, batch queue-then-resolve pipeline, orphan recovery on restart | 🟡 In Progress | M | Two-phase QueuePending + resolvePendingDownloads, `/api/downloads?state=active` |
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
| 61 | **Discogs metadata** — release info, catalog numbers | 🟢 Low | M | Discogs API |
| 62 | **AudioDB metadata** — artist images, bios, genre tags | 🟢 Low | S | AudioDB API |

---

## Tier 6: Automation Engine

Scheduled background tasks for hands-off operation.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 63 | **Automation engine** — cron-like scheduler for background tasks | 🟡 Medium | L | Task queue |
| 64 | **Playlist auto-sync** — scheduled playlist refresh + download | 🟡 Medium | M | Automation + playlists |
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
| 70 | **Authentication** — login gate, reverse proxy support, API keys | 🔴 High | L | — |
| 71 | **Docker image + docker-compose with slskd** — one-command `docker compose up` for the free path. Enables quick test cycle: change code → rebuild → test. Primary deployment target. | 🔴 High | M | — |
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
| 1 | Core Quality | 11 features | ✅ 11/11 |
| 1.5 | Metadata Providers | 6 features | ❌ 0/6 |
| 2 | Download Sources | 8 features | ❌ 0/8 |
| 3 | Library & Media Servers | 7 features | ❌ 0/7 |
| 4 | Playlists & Discovery | 10 features | 🟡 2 done, 8 remaining |
| 5 | Metadata Enrichment | 5 features | ❌ 0/5 |
| 6 | Automation | 7 features | ❌ 0/7 |
| 7 | Platform & Ops | 12 features | ❌ 0/12 |
| **Total** | | **81 features** | **28 done, 53 remaining** |

## Known Bugs

| # | Component | Severity | Description |
|---|-----------|----------|-------------|
| B1 | Deezer | 🟡 Medium | **`Test Connection` false positive with invalid ARL.** `authenticate()` only checks `USER_ID != 0` after `deezer.getUserData`. An expired/malformed ARL may still return partial user data with a valid-looking `USER_ID`. Should also verify `license_token` is non-empty or call a protected endpoint to confirm premium access. |
| B2 | Config | ✅ Done | **Masked secret round-trip corruption.** `Merge()` now detects masked strings and preserves original values. Fixed in `internal/config/config.go:90-160`. |
| B3 | Config (design) | 🟡 Medium | **`Merge()` replaces entire source objects.** Any field not sent by the frontend (e.g., `tokens`, `allow_fallback`) is lost on save. Server-managed fields like OAuth tokens can't coexist safely with user-editable config without deep-merge. |
| B4 | Playlist / Download | ✅ Done | **Queued tracks now visible.** `DownloadMissing` uses two-phase batch-queue → background-resolve. `QueuePending` inserts all tracks immediately with `source=pending`. Pending tab shows queue via SSE. `RecoverOrphans` recovers stuck records on restart. |
| B5 | Playlist / Download | 🟡 Medium | **Stuck pending detection.** Records with `source=pending` older than N minutes should be auto-failed or retried. Currently silently stick forever if resolver fails. |
| B6 | Discover / Download | 🟡 Medium | **Discover album download should use two-phase pattern.** Currently does synchronous search+queue per track. Should batch-queue all first (like playlists) for instant visibility. |
| B7 | Download / UI | 🟢 Low | **Server-side pagination for download list.** `GET /api/downloads` returns all records. Large queues (1000+) should paginate with `?limit=` and `?offset=`. |

### Immediate Next Steps

1. **Docker image + compose** — `docker compose up` for one-command dev/test cycle (#71, Tier 7, 🔴 High)
2. **Metadata provider plugin architecture** — interface + registry (#27, Tier 1.5, 🔴 High)
3. **Cover Art Archive provider** — free cover art for slskd users (#28, Tier 1.5, 🔴 High)
4. **MusicBrainz metadata provider** — free ISRC, album grouping, AcoustID (#29, Tier 1.5, 🔴 High)
5. **Authentication** — login gate for API access (#70, Tier 7, 🔴 High)

### First Major Feature Block (Tier 1.5 + Tier 4)

1. **Metadata providers** — free-tier backbone: CAA + MusicBrainz + enrichment pipeline
2. **Spotify OAuth + playlist import** — biggest user-facing premium feature gap (#48, Tier 4)
3. **Playlist sync pipeline** — end-to-end Spotify → download → library (#50, Tier 4)
