# Groovearr — Feature Roadmap & Gap Analysis

> What Groovearr has vs what original SoulSync 2.x provided.
> Tiers ordered by priority. Status: ✅ Done | 🟡 Partial | ❌ Missing

---

## Tier 0: MVP — Already Implemented

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 1 | Search via Soulseek (slskd) | ✅ | REST API polling, album grouping |
| 2 | Download via Soulseek | ✅ | Transfer tracking, cancel, clear |
| 3 | Search Deezer metadata | ✅ | Public API, advanced query syntax |
| 4 | Download via Deezer (ARL) | ✅ | Blowfish decrypt, quality fallback |
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
| 15 | Web UI (embedded SPA) | ✅ | Vanilla JS, all pages, no framework |

---

## Tier 1: Core Quality — Fix Current Gaps

Small but impactful fixes to what's already built.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 16 | **Post-download file renaming** — apply `folder_template` via `PathResolver` after download completes | 🔴 High | S | PathResolver (done) |
| 17 | **Audio metadata parsing** — read ID3/FLAC/Vorbis tags in scanner instead of path-only heuristics | 🔴 High | M | `dhowden/tag` |
| 18 | **Tag writing** — embed metadata into downloaded files (artist, album, title, track#, cover art) | 🔴 High | L | `bogem/id3v2`, FLAC Vorbis |
| 19 | **Config validation** — validate URLs, quality values, path existence at save time | 🟡 Medium | S | — |
| 20 | **DB migration versioning** — `golang-migrate/migrate` or embed version SQL instead of idempotent CREATE | 🟡 Medium | S | — |
| 21 | **Wire `QualityConfig`** — actually use `min_bitrate` and `preferred_format` in download filter logic | 🟡 Medium | S | — |
| 22 | **Use central `download.Engine`** — make plugins use the shared record tracker instead of managing their own | 🟢 Low | S | — |
| 23 | **Artist unique constraint** — add UNIQUE on `artists.name` to prevent duplicates | 🟡 Medium | S | — |
| 24 | **Library pagination + search** — API query params + UI search/filter beyond 200 limit | 🟡 Medium | M | — |
| 25 | **Album-art display** — fetch + cache cover images, show in UI | 🟡 Medium | M | — |

---

## Tier 2: Additional Download Sources

Plugin interface makes adding new sources straightforward.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 26 | **YouTube downloads** — via yt-dlp subprocess or API wrapper | 🟡 Medium | L | `yt-dlp` binary or Go yt-dlp lib |
| 27 | **Tidal downloads** — OAuth device auth, search + download + quality selection | 🟡 Medium | L | Tidal API (`tidalapi` port) |
| 28 | **Qobuz downloads** — REST API auth, search + download | 🟢 Low | L | Qobuz API |
| 29 | **SoundCloud downloads** — yt-dlp extractor or direct API | 🟢 Low | M | — |
| 30 | **Torrent downloads (Prowlarr)** — Prowlarr API → qBittorrent/Transmission | 🟢 Low | L | Prowlarr + torrent client |
| 31 | **Usenet downloads (Prowlarr)** — Prowlarr → SABnzbd/NZBGet | 🟢 Low | L | Prowlarr + usenet client |
| 32 | **Lidarr integration** — use Lidarr as download source via its API | 🟢 Low | M | Lidarr instance |
| 33 | **Direct URL download** — paste Tidal/Qobuz track URLs directly | 🟢 Low | S | Tidal/Qobuz clients |

---

## Tier 3: Library & Media Server Integration

Auto-sync library state with external media servers.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 34 | **Plex integration** — scan library, detect missing tracks, trigger refresh | 🔴 High | L | Plex API |
| 35 | **Jellyfin integration** — same as Plex but against Jellyfin API | 🟡 Medium | L | Jellyfin API |
| 36 | **Navidrome integration** — Subsonic API for library sync | 🟡 Medium | L | Subsonic API |
| 37 | **Library duplicate detection** — SHA256 content hash, filename fuzzy match | 🟡 Medium | M | File scanner |
| 38 | **Library issues dashboard** — show missing tracks, dupes, stale files, tag mismatches | 🟢 Low | L | Scanner + store |
| 39 | **Listening stats page** — play counts, top artists, recent additions | 🟢 Low | M | Plex/Jellyfin play history |
| 40 | **M3U playlist export** — export library playlists as .m3u files | 🟢 Low | S | — |

---

## Tier 4: Playlists & Discovery

Spotify integration for playlist import/sync, artist following, and discovery.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 41 | **Spotify OAuth** — login flow, token refresh, scoped access | 🔴 High | M | Spotify Developer App |
| 42 | **Spotify playlist import** — list user playlists, import tracks to DB | 🔴 High | M | Spotify OAuth |
| 43 | **Playlist sync (Spotify → library)** — REFRESH → DISCOVER → SYNC → DOWNLOAD pipeline | 🔴 High | L | Playlist + matching + download |
| 44 | **Playlist explorer UI** — browse playlists, view tracks, trigger sync | 🟡 Medium | M | Playlist sync |
| 45 | **Artist watchlist** — follow artists, get notifications of new releases | 🟡 Medium | L | Spotify/Deezer APIs |
| 46 | **Automatic watchlist downloads** — new releases auto-downloaded | 🟡 Medium | M | Watchlist + download pipeline |
| 47 | **Wishlist / retry queue** — failed downloads go to wishlist for auto-retry | 🟡 Medium | M | Download pipeline |
| 48 | **Discovery pool** — AI-curated recommendations from Spotify/Deezer/Last.fm | 🟢 Low | L | Multiple APIs |
| 49 | **Personalized playlists** — Daily Mix, Discover Weekly, Release Radar sync | 🟢 Low | L | Spotify OAuth |
| 50 | **Beatport charts** — top charts imported for discovery | 🟢 Low | M | Web scraping |

---

## Tier 5: Metadata Enrichment

Rich metadata from external services to improve library quality.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 51 | **Last.fm scrobbling** — scrobble plays, fetch similar artists/tags | 🟡 Medium | M | Last.fm API |
| 52 | **ListenBrainz scrobbling** — open alternative to Last.fm | 🟢 Low | M | ListenBrainz API |
| 53 | **Genius lyrics** — fetch + embed lyrics into audio files | 🟡 Medium | L | Genius API |
| 54 | **MusicBrainz metadata** — artist/album/release IDs, AcoustID fingerprinting | 🟡 Medium | L | MusicBrainz API + `fpcalc` |
| 55 | **Discogs metadata** — release info, catalog numbers | 🟢 Low | M | Discogs API |
| 56 | **iTunes metadata** — additional external IDs + artwork | 🟢 Low | M | iTunes Search API |
| 57 | **AudioDB metadata** — artist images, bios, genre tags | 🟢 Low | S | AudioDB API |
| 58 | **Metadata enrichment pipeline** — background workers for all sources, priority queue | 🟢 Low | L | All metadata clients |

---

## Tier 6: Automation Engine

Scheduled background tasks for hands-off operation.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 59 | **Automation engine** — cron-like scheduler for background tasks | 🟡 Medium | L | Task queue |
| 60 | **Playlist auto-sync** — scheduled playlist refresh + download | 🟡 Medium | M | Automation + playlists |
| 61 | **Watchlist auto-scan** — scheduled watchlist check for new releases | 🟡 Medium | M | Automation + watchlist |
| 62 | **Wishlist auto-process** — scheduled retry of failed downloads | 🟡 Medium | M | Automation + wishlist |
| 63 | **Library auto-scan** — scheduled filesystem scan for new/missing files | 🟡 Medium | S | Automation + scanner |
| 64 | **Personalized playlist auto-refresh** — scheduled Daily Mix/Discover Weekly sync | 🟢 Low | M | Automation + Spotify |
| 65 | **Cleanup tasks** — auto-remove completed downloads, duplicate cleanup | 🟢 Low | M | Automation + library |

---

## Tier 7: Platform & Operations

Deployment, security, and operational concerns.

| # | Feature | Priority | Effort | Dependencies |
|---|---------|----------|--------|--------------|
| 66 | **Authentication** — login gate, reverse proxy support, API keys | 🔴 High | L | — |
| 67 | **Multi-profile support** — separate libraries/configs per profile | 🟢 Low | L | Auth |
| 68 | **Setup wizard** — first-run guided config | 🟡 Medium | M | — |
| 69 | **Docker image** — Dockerfile + docker-compose with slskd | 🟡 Medium | M | — |
| 70 | **Systemd service** — service file + install target | 🟡 Medium | S | — |
| 71 | **Download queue prioritization** — priority tiers, bandwidth limits | 🟢 Low | L | Download engine refactor |
| 72 | **Resume partial downloads** — checkpoint + resume for large files | 🟢 Low | L | Download engine refactor |
| 73 | **Lossy conversion** — transcode FLAC → MP3 via ffmpeg for Plex | 🟢 Low | M | ffmpeg binary |
| 74 | **ReplayGain scanning** — loudness analysis + tag writing | 🟢 Low | L | Audio analysis lib |
| 75 | **Content quarantine** — import safety review before adding to library | 🟢 Low | M | Library + UI |
| 76 | **Public REST API v1** — documented, versioned API for external tools | 🟢 Low | L | Auth + API spec |
| 77 | **Custom scripts** — user-defined automation scripts | 🟢 Low | L | Automation engine |

---

## Summary

| Tier | Name | Count | Status |
|------|------|-------|--------|
| 0 | MVP | 15 features | ✅ 15/15 |
| 1 | Core Quality | 10 features | ❌ 0/10 |
| 2 | Download Sources | 8 features | ❌ 0/8 |
| 3 | Library & Media Servers | 7 features | ❌ 0/7 |
| 4 | Playlists & Discovery | 10 features | ❌ 0/10 |
| 5 | Metadata Enrichment | 8 features | ❌ 0/8 |
| 6 | Automation | 7 features | ❌ 0/7 |
| 7 | Platform & Ops | 12 features | ❌ 0/12 |
| **Total** | | **77 features** | **15 done, 62 remaining** |

### Immediate Next Steps (Tier 1)

1. **Post-download renaming** — wire PathResolver (already built) into download completion flow
2. **Audio tag reading** — add `dhowden/tag` to scanner for accurate metadata
3. **Audio tag writing** — embed metadata after download (ID3 + FLAC Vorbis)
4. **Authentication** — basic login gate for API access
5. **Config validation** — validate on save in UI + server-side

### First Major Feature Block (Tier 3-4)

1. **Spotify OAuth + playlist import** — biggest user-facing gap
2. **Plex media server integration** — library sync with external server
3. **Playlist sync pipeline** — end-to-end Spotify → download → library
