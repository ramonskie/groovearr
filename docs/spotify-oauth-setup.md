# Spotify OAuth Setup (NAS / Docker)

Connecting a Spotify Developer App account to Groovearr when Groovearr runs in
Docker on a NAS. This covers the OAuth redirect-URI constraints and the
recommended one-time tunnel flow.

> **Only needed when Groovearr is NOT publicly reachable.** If Groovearr is
> already exposed to the internet behind HTTPS (e.g. `https://groovearr.example.com`),
> you skip the tunnel entirely — just register
> `https://groovearr.example.com/api/spotify/callback` in Spotify and set the
> same value in the Groovearr settings. The tunnel workaround exists only for
> LAN-only / HTTP setups, where Spotify refuses the redirect URI.

## Why it's fiddly

Spotify enforces two rules on the OAuth **redirect URI**:

1. Plain `http://` is only allowed on `localhost` / `127.0.0.1`.
   Any other host **must** be `https://`. A LAN IP over HTTP
   (`http://192.168.1.178:8008/api/spotify/callback`) is rejected by the
   Spotify Developer dashboard as **redirect insecure**.
2. The redirect URI must resolve back to the exact Groovearr process that
   started the login. The CSRF check in
   `internal/providers/spotify/handlers.go` validates the OAuth `state`
   against an **in-memory** map in that process.

Using `http://127.0.0.1:8008/api/spotify/callback` as the redirect URI points
the browser at *its own* machine, not the NAS. The callback never reaches the
Groovearr process that holds the state, so it fails with
`invalid state — CSRF check failed`.

## Recommended: temporary HTTPS tunnel (one-time)

The redirect URI only matters during the connect step. After tokens are saved,
Groovearr renews them server-side with the refresh token — no redirect URI
involved — so the tunnel can be taken down right after.

1. Add a cloudflared quick-tunnel service to `docker-compose.yml`:

   ```yaml
   cloudflared:
     image: cloudflare/cloudflared:latest
     container_name: groovearr-cloudflared
     command: tunnel --url http://192.168.1.178:8008
     restart: unless-stopped
   ```

   Use the NAS host IP, not `localhost:8008` — inside the container that would
   point at the cloudflared container itself.

2. Start it and grab the URL:

   ```bash
   docker compose up -d cloudflared
   docker compose logs -f cloudflared
   ```

   Look for a `*.trycloudflare.com` address, e.g.
   `https://wide-goats-click.trycloudflare.com`. The URL is random and changes
   on every restart.

3. **Spotify Developer app** → Edit Settings → Redirect URIs
   (`https://developer.spotify.com/` → Dashboard → your app → Edit Settings):

   ```
   https://wide-goats-click.trycloudflare.com/api/spotify/callback
   ```

   **Save** the app settings before proceeding — the redirect URI is only
   registered after you save, and it can take a moment to propagate.

4. **Groovearr UI** → Settings → Spotify → Redirect URI field, same value:

   ```
   https://wide-goats-click.trycloudflare.com/api/spotify/callback
   ```

   Must match byte-for-byte. The same value is used to build the auth URL and
   to exchange the code, so consistency is automatic once set in config.

5. **Connect** — hit *Connect Spotify Account*. The browser goes to Spotify and
   redirects back through the tunnel into the same NAS process — CSRF passes,
   tokens are saved, you land on `/settings?spotify=connected`.

6. **Take it down**:

   ```bash
   docker compose stop cloudflared
   ```

   Tokens keep working. Consider removing the service from
   `docker-compose.yml` entirely.

> **Security**: while the tunnel is up, Groovearr (which has no auth) is
> publicly reachable. Keep it up only during the connect step. Only the
> tunnel is public — the rest of the stack (slskd, Prowlarr, qBittorrent)
> stays on the LAN.

## Alternative: SSH tunnel (LAN-only)

No public exposure, but the redirect URI stays `http://127.0.0.1:8008/...`.
Register it in Spotify (loopback HTTP is allowed), then from the machine whose
browser you use:

```bash
ssh -L 8008:192.168.1.178:8008 user@nas
```

Browse to `http://127.0.0.1:8008` and connect. The callback loops back through
the tunnel into the same NAS process. Must be re-tunnelled per machine / per
session — fine for a one-off, not for a permanent deploy.

## When is this needed again?

Only once per account connect. The access/refresh tokens are persisted in
`config.json`; renewal uses `client_id` + `refresh_token` and never touches the
redirect URI. Re-run the tunnel only if you re-authorize the account (e.g.
revoked access, changed Spotify password).
