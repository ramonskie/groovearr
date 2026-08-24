# Provider Rate Limits

> **Status**: Verified 2026-08-23 against official provider documentation.
> **Principle**: No assumptions. Every figure below was checked against the
> provider's official docs — or explicitly marked unverifiable. Self-hosted
> services get no artificial rate limit. External APIs are paced conservatively,
> and the app uses **one shared cooldown bucket** that is never wiped between
> runs, so a server-requested backoff (e.g. Spotify `Retry-After`) is honored
> app-wide until it expires.

---

## Rate limit inventory

### External APIs with documented limits

| Provider | Local limit | Documented quota (official) | Source |
|---|---|---|---|
| **MusicBrainz** | 1 req/s | **1 req/s sustained avg per source IP**; exceed → 100% decline (HTTP 503); UA exceptions ~50 req/s for known clients (headphones, python-musicbrainz, anonymous) | musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting |
| **Discogs** | 0.4 req/s (24/min) | **25 req/min unauthenticated, 60 req/min authenticated**, moving 60s window; `X-Discogs-Ratelimit(-Used/-Remaining)` headers; no Retry-After documented | discogs.com/developers |
| **Last.fm** | **1 req/s** (was 3) | No numeric limit. Docs: *"continuously making several calls per second"* → suspension risk; ToS: limits "in our sole discretion", 100 MB data cap | last.fm/api/intro, last.fm/api/tos |
| **Spotify Web API** | **2 req/s** (was 10) | **No public number.** Rolling 30-second window per app; dev-mode quota per developer account (endpoint buckets, numbers unpublished); `Retry-After` documented (seconds). SoulSync paces ~2.85/s in production — 2/s sits under that proven rate | developer.spotify.com/documentation/web-api/concepts/rate-limits, /quota-modes |
| **Cover Art Archive** | 5 req/s | *"There are currently no rate limiting rules in place"* — yet HTTP 503 documented for "exceeded their rate limit" (threshold unpublished). Community practice ~1 req/s | musicbrainz.org/doc/Cover_Art_Archive/API |

### External APIs without a verifiable public number

| Provider | Local limit | Notes | Source |
|---|---|---|---|
| **Deezer** | metadata 1/s, gateway **1/s** (was 10), download **1/s** (was 30) | API docs login-gated (live + Wayback 2022/2025). User-confirmed quota: **50 requests per 5 seconds per IP address**. SoulSync also paces Deezer at 1/s — rates sit ~10× below the cap | developers.deezer.com (login-gated) |
| **Tidal** | ~0.83 req/s (1.2s interval) | Developer portal is a login-gated SPA; no public rate docs | developer.tidal.com |
| **Spotify oEmbed** | 5 req/s | No limit documented (separate host: open.spotify.com/oembed) | developer.spotify.com/documentation/embeds/reference/oembed |

### Self-hosted (no external quota — no artificial rate limit)

| Provider | Local limit | Notes |
|---|---|---|
| **Soulseek (slskd)** | none (limiter removed) | Requests go to our own slskd instance |
| **Prowlarr** | none on own Torznab API | Per-indexer `Query Limit` / `Grab Limit` are configurable in Prowlarr. Note: prowlarr's `ResolveTracks` routes album track listings through the shared MusicBrainz client (1 req/s) — separate from its own API |
| **qBittorrent** | none | Local API |

---

## Decisions & changes (2026-08-23)

1. **Last.fm 3 → 1 req/s** — 3 req/s *is* "several calls per second", which Last.fm documents as a suspension trigger. The previous comment claimed "conservative — no published limit", which was wrong.
2. **Soulseek limiter removed** — self-hosted, no external quota.
3. **Deezer download 30 → 10 → 1 req/s; gateway 10 → 1 req/s** — Deezer's quota is per IP: 50 requests per 5 seconds. Rates now sit ~10× below the cap, matching SoulSync's production pacing (1/s).
4. **Spotify Web API 10 → 2 req/s** — SoulSync runs ~2.85/s in production; 10/s was the outlier and empirically tripped Spotify (429 with 1h53m Retry-After). 2/s sits under SoulSync's proven rate.
5. **Deezer quota error codes detected** — Deezer signals quota via JSON `error.code` 4/700 (not always HTTP 429). These now surface through the shared `ErrRateLimited` sentinel so the cooldown kicks in.
6. **Tidal, Cover Art Archive, Discogs, MusicBrainz unchanged** — either already at the documented limit (MusicBrainz 1/s), within it (Discogs 24/min < 25/min), or no verifiable number to tune against.

---

## Shared cooldown — one bucket, never wiped

A single app-wide `ProviderCooldown` instance is wired into:

- **Enrichment job** (`MetadataEnrichmentHandler`) — skips a provider that recently returned a rate-limit error, honors the server's `Retry-After`.
- **Discover search / album discovery** (API layer) — pre-filters cooled providers.
- **Health checker** — skips probing a provider in cooldown, and **re-marks** the shared bucket when its own probe returns a rate-limit error. A 429 is evidence the API is reachable: the probe records `connected` with the backoff noted (rather than disconnected), and skipped probes keep the last status.

`ResetBulk()` (enrichment job start) no longer wipes the cooldown — it only clears the per-run artist-image retry dedup. Wiping the cooldown between runs went around the server-requested backoff and re-armed longer `Retry-After` values (observed: Spotify 1h53m after a second enrichment run).

**Jobs share the bucket**: `jobs.Runners` receives the same `ProviderCooldown`
via `RunnerDeps.RateLimit` (wired from `Server.SetProviderCooldown`). The
duplicates job's canonical lookups skip cooling providers before calling and
mark the bucket on `ErrRateLimited`, so a rate-limited MusicBrainz parks
app-wide and no job re-probes a throttled API to re-arm a longer ban.

### Cooldown behavior (SoulSync / *arr-inspired)

- **Server `Retry-After` honored**, capped at **4h** (was 10 min — too short to ride out a real ban like Spotify's 1h53m).
- **Post-ban grace**: bans (Retry-After ≥ 1 min) get +5 min cooldown after expiry, preventing the "immediate re-probe → re-ban" loop SoulSync observed.
- **Escalation**: when the server sends no backoff (we're guessing), repeated hits within a 1h window double the cooldown (2m → 4m → 8m … capped 4h). A server-provided backoff resets the counter. Circuit-breaker style, like the *arr family's `EscalationBackOff`.
- **Persisted**: cooldowns and rate-limit events are written to SQLite (`provider_cooldowns`, `rate_limit_events` tables in library.db) so a long backoff survives a restart and is observable. Expired cooldowns and the event log (last 500) are pruned on startup.
- **Observability**: `GET /api/rate-limits` returns active cooldowns + recent rate-limit events (provider, duration, source: server/server+grace/default/escalated). `DELETE /api/rate-limits/{provider}` clears a parked provider manually (escape hatch for a long/malformed Retry-After).

---

## Caveats

- **Deezer / Tidal / Spotify oEmbed** numbers are not verifiable against public docs (login walls, SPAs). Deezer's 50/5s per-IP quota is user-confirmed knowledge, not public-doc-verified.
- **Deezer per-IP cap and combined throughput**: gateway (1/s) + download (1/s) combined = 2 req/s (10 per 5s) — well under the 50/5s cap. Headroom for faster downloads if needed.
- **Spotify's historical "~3600 requests/hour" quota is NOT in current docs** — the old support article 404s. Treat the rolling-30s-window docs as canonical; no numeric target exists. 2/s was chosen from SoulSync's production evidence, not a published number.
- **Discogs headroom**: 24/min leaves only 1 req/min margin under the 25/min unauthenticated cap. Raise to 1 req/s (60/min) only if OAuth authentication is added.
- **Burst vs sustained**: all local limiters pace at a *sustained* rate. Spotify's limit is burst-oriented (rolling 30s window) — 2/s (120 requests per 30s window) is conservative and sustained-safe.
