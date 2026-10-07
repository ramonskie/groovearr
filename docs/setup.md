# Setup Guide

Get Groovearr downloading music. Two source options:

- **Soulseek (free P2P)** — free track downloads from other users' shared libraries.
- **RuTracker (via Prowlarr) + qBittorrent** — full-album torrent downloads.

Everything is configured through the **Groovearr web UI** — no config file editing.

---

## 1. Start the stack

```bash
cp .env.example .env        # Soulseek account credentials (edit to your own)
make docker-setup           # generate slskd.yml (account + Groovearr API key)
docker compose up -d
docker compose ps
```

No config file is needed upfront — Groovearr creates one automatically and the
**setup wizard** walks you through connecting your first source on first open.

| Service     | URL                          | Notes                          |
|-------------|------------------------------|--------------------------------|
| Groovearr   | http://localhost:8008        | the app                        |
| slskd       | http://localhost:5030        | Soulseek daemon (step 2)       |
| Prowlarr    | http://localhost:9696        | torrent search (step 3)        |
| qBittorrent | http://localhost:8080        | torrents (step 4)              |
| FlareSolverr| http://localhost:8191       | Cloudflare bypass (step 3)     |

---

## 2. Soulseek (free)

Soulseek needs two credentials: a **Soulseek account** (free, register at slsknet.org)
and a **slskd API key** used by Groovearr.

### 2a. Configure slskd (one-time)

1. Fill in your Soulseek account:
   ```bash
   cp .env.example .env
   # edit .env:
   #   SLSKD_SOULSEEK_USERNAME=your_soulseek_username
   #   SLSKD_SOULSEEK_PASSWORD=your_soulseek_password
   ```
2. Generate the slskd config:
   ```bash
   make docker-setup
   ```
   This writes `slskd.yml` with your username and a Groovearr API key
   (`groovearr-test-key-123456789012345` by default).
3. Apply it:
   ```bash
   make docker-restart
   ```
4. Check slskd connected: open `http://localhost:5030` (login `slskd` / `slskd`) —
   the status dot in the bottom-right should be green.

> Don't use the default API key? Edit the `api_keys` block in `slskd.yml` and
> `make docker-restart`.

### 2b. Add Soulseek to Groovearr

1. Open **http://localhost:8008** → **Settings → Download Sources**.
2. Find **Soulseek** → flip the enable toggle on.
3. Fill in:
   - **slskd URL**: `http://slskd:5030`
   - **API key**: the Groovearr key from `slskd.yml` (default
     `groovearr-test-key-123456789012345`)
4. Click **Test Connection** — it should show Connected.
5. **Save** (auto-saves).

---

## 3. Prowlarr + RuTracker

### 3a. Add the FlareSolverr proxy

RuTracker is behind Cloudflare, so it needs the proxy. Name it **`flare`**.

1. Open **http://localhost:9696**.
2. **Settings → Indexers → FlareSolverr** → **+**.
3. Set **Name**: `flare`, **Host**: `http://flaresolverr:8191`, **Tags**: `flare`.
4. **Save**.

### 3b. Add the RuTracker indexer

1. **Indexers → Add Indexer**.
2. Search `rutracker`, select **RuTracker**.
3. Fill in:
   - **Name**: `RuTracker`
   - **Username / Password**: your rutracker.org credentials
   - **Tags**: `groovearr` and `flare` (add them right in the form)
4. **Save**.

> The `flare` tag is what makes the proxy apply to this indexer. The `groovearr`
> tag is what makes Groovearr use it.

### 3c. Copy the API key

**Settings → General** → copy the **API Key**.

---

## 4. qBittorrent

1. Get the temporary password from the logs:
   ```bash
   docker logs groovearr-qbittorrent 2>&1 | grep -i "temporary password"
   ```
2. Open **http://localhost:8080** → login as `admin` with that password.
3. Set a permanent password: **Tools → Options → Web UI → Authentication**.
4. Copy the **API key** (`qbt_…`) from the same Web UI page (also printed in the
   container logs on first start).

---

## 5. Connect everything in Groovearr

Open **http://localhost:8008** → **Settings → Download Sources**.

**Providers tab** — configure each source (enable toggle → fill in → Test Connection):

- **Prowlarr**: URL `http://prowlarr:9696`, API key (step 3c), indexer tag `groovearr`
- **qBittorrent**: URL `http://qbittorrent:8080`, API key (step 4), category `music`

**Priority tab** — set how downloads are routed:

- **Album Sources**: check Prowlarr
- **Download Client**: select qBittorrent
- **Download Order**: drag the track sources into priority order (Soulseek first;
  add Tidal/Deezer if configured) — this order alone determines which sources are
  used for per-track downloads
- **Metadata Order**: leave the defaults

Everything auto-saves.

### Download path layout & protection

Each download provider stages its files in its **own directory**, separate from the
library and from each other. Do **not** point a provider's download path at the
general `library.download_path` root itself, and do **not** nest one provider's
directory inside another's — use sibling directories:

```
/downloads                     ← library.download_path (root)
├── slskd/                     ← Soulseek download_path   (e.g. /downloads/slskd)
└── qbittorrent/               ← qBittorrent download_path (e.g. /downloads/qbittorrent)
/music                         ← library.library_path (the only directory scanned)
```

Guidelines:

- **Soulseek** (`Settings → Download Sources → Soulseek`): set `download_path` to a
  dedicated folder, e.g. `/downloads/slskd`. If you share the slskd instance with
  other applications, keep its shared directory out of Groovearr's download root —
  Groovearr must never import files it did not download.
- **qBittorrent** (`Settings → Download Sources → qBittorrent`): set `download_path`
  to a dedicated folder, e.g. `/downloads/qbittorrent`.
- **Tidal / Deezer** (when configured) use the general download root; keep that root
  exclusively for Groovearr downloads.

Groovearr has a **protection layer** that makes these rules safe by construction:

- Library scans (Settings → Jobs → Scan, and playlist syncs) only ever walk the
  library path. They **refuse** to enter the download root or any provider
  `download_path` — a scan pointed at a staging directory is rejected with an error
  instead of importing its contents.
- Downloads are imported into the library exclusively by the download pipeline,
  one file at a time, from the file that was actually downloaded. Playlists link
  tracks that already exist in the library and queue the rest through the download
  pipeline; they never scan the filesystem.
- When two playlists from the same source share a name (e.g. two Tidal playlists
  both called "My Mix"), each gets its own suffixed folder (`My Mix (a1b2c3d4)`)
  so their files never mix. If both playlists already existed before this
  behavior, the old shared folder keeps stale copies after re-sync — remove it
  manually.
- If a sweep ever did slip multi-artist entries into the library (e.g. "2Pac feat.
  Anthony Hamilton" as its own artist), run **Settings → Jobs → Artist Cleanup** to
  merge them back into the primary artist ("2Pac").

---

## 6. Verify

1. Open **http://localhost:8008** → **Discover**.
2. Search an album → **Download**.
3. Watch it in the **Downloads** tab: the source is picked by priority, the album
   downloads, and lands in the **Library** with tags and cover art.

---

## Authentication

`auth.method` accepts two values:

- `none` (default) — no login. **This is not access control:** every request is
  treated as admin, so only use it on a trusted network.
- `forms` — cookie-based login page with username + password.

HTTP Basic (`basic`) is retired: it was never implemented end-to-end. **On save**
(`PUT /api/config`) a config that sets `basic` is rejected by validation. A
pre-existing `config.json` that already sets `basic` is not fatal —
`Config.Load` logs a validation warning and continues — but the setting yields
no working login: `withAuth` never evaluated Basic credentials and `POST
/api/login` only accepts `forms`, so Groovearr treats the request as if no
credential were present. Use `forms` instead.

### Bootstrap credentials (`auth.username` / `auth.password`)

`auth.username` and `auth.password` are **bootstrap-only**. They create the very
first account and are ignored afterwards:

- **First run (`forms` only):** when `auth.method` is `forms`, both a username
  and password are configured, and the `users` table is still empty, Groovearr
  seeds a single **admin** from those credentials at startup
  (`internal/user` `EnsureBootstrapAdmin`). Once any user exists it is a no-op,
  so it is safe on every subsequent start.
- **After bootstrap:** login authenticates against the `users` table, not the
  config. Changing `auth.username` / `auth.password` later does **not** change
  an existing account's password, and does **not** create another account — the
  configured values are simply unused.
- **Managing users:** create, disable, change roles, and set passwords from the
  admin-only **Users** screen (backed by `/api/users`). Usernames are fixed at
  creation — there is no rename. The bootstrap credentials are not a live login.
- **`auth.method="none"` never seeds an account:** with no login there is no one
  to authenticate as, and every request is treated as admin (see the warning
  above).

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `no indexers with tag "groovearr"` | Tag the RuTracker indexer `groovearr` (step 3b) |
| Cloudflare errors on RuTracker | Ensure the indexer has the `flare` tag (step 3b); check http://localhost:8191 |
| Can't log into qBittorrent | Password is temporary — re-check `docker logs groovearr-qbittorrent` (step 4) |
| Torrent added but never downloads | Check the qBittorrent API key + category in Groovearr (step 5) |
| Prowlarr `401` in Groovearr | Re-paste the API key (steps 3c + 5) |
| slskd not connecting | Verify credentials in `.env`, re-run `make docker-setup` + `make docker-restart` |
| Soulseek `401` in Groovearr | Match the Groovearr API key to the one in `slskd.yml` |
