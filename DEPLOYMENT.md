# Deployment

Relyper Backup ships as **one Docker image** built from the `Dockerfile` in
this repository. The container runs the complete product: web UI, API and
scheduler (one Go process), plus the tools it drives – restic, rclone and the
OpenSSH client for SFTP repositories. No database container, sidecar or
separate MCP container is needed.

## Quick start (Docker Compose)

```bash
git clone https://github.com/jonasesser/relyper-backup.git
cd relyper-backup
docker compose up -d --build
```

Open `http://<host>:9898`. On first start the UI asks for an instance ID
and the first user. Do not disable authentication on a reachable host.

## Build the image yourself

```bash
docker build \
  --build-arg RELYPER_VERSION=0.1.0 \
  --build-arg RELYPER_COMMIT=$(git rev-parse --short HEAD) \
  -t relyper-backup:0.1.0 .
```

The build has three stages: web UI (Node 22, pnpm 9), Go server (static
binary, `CGO_ENABLED=0`) and an Alpine runtime. During the build the server
downloads the restic version it is tested with
(`internal/resticinstaller`, currently 0.19.1) and verifies its SHA-256 sums
and signature before it is copied into the image.

Network access needed at build time: Docker Hub (base images),
`registry.npmjs.org`, `proxy.golang.org`, `github.com` (restic release),
`dl-cdn.alpinelinux.org` (runtime packages).

## Container layout

| Path | Purpose | Mode |
|---|---|---|
| `/config` | `config.json`, SSH keys and `known_hosts` (`/config/.backrest-ssh`) | writable, persistent, **sensitive** |
| `/data` | operation history (SQLite), logs, key-value store | writable, persistent |
| `/cache` | restic cache (`XDG_CACHE_HOME`) | writable, can be recreated |
| `/tmp` | temporary files (`TMPDIR`) | writable, ephemeral |
| `/restore` | restore staging directory (convention) | writable |
| `/sources/...` | data to back up (convention) | **read-only** bind mounts |
| `/repos` | local restic repository (optional, convention) | writable |

Only `/config`, `/data` and `/cache` are declared volumes. `/restore`,
`/sources` and `/repos` are conventions used by `docker-compose.yml` and the
documentation; mount what you need.

### Rules for mounts

- Mount backup sources **read-only** (`:ro`). Relyper Backup only needs to
  read them.
- Restore into `/restore` (a separate, writable staging directory) and move
  data into production deliberately. Do not mount production data writable
  just to restore into it.
- Do **not** mount the host root (`/:/host`) writable. If you need broad read
  access, mount it read-only and understand that every admin of Relyper Backup
  can then read every file on the host.
- Keep `/config`, `/data`, `/cache`, `/restore` and `/repos` **outside** of
  the paths you back up. Otherwise a plan backs up its own cache, staging
  area or repository (recursive backups, exploding repository size). With the
  layout above this cannot happen because plans only see `/sources/...`.

## Environment variables

| Variable | Default in the image | Meaning |
|---|---|---|
| `BACKREST_PORT` | `0.0.0.0:9898` | Bind address of the web UI and API. Changing the port also changes the health check automatically. |
| `BACKREST_CONFIG` | `/config/config.json` | Configuration file. The SSH directory is derived from it. |
| `BACKREST_DATA` | `/data` | Data directory. |
| `BACKREST_RESTIC_COMMAND` | `/usr/local/bin/restic` | restic binary. |
| `XDG_CACHE_HOME` | `/cache` | restic cache location. |
| `TMPDIR` | `/tmp` | Temporary files. |
| `TZ` | unset (UTC) | Time zone used for schedules and display. |

The `BACKREST_*` names are kept from upstream so existing Backrest
configurations and documentation keep working.

| `RELYPER_PUBLIC_URL` | unset | Public HTTPS address, e.g. `https://backup.example.com`. Used in the OAuth metadata of the MCP server; without it the address is derived from the request headers. |

The MCP endpoint `/mcp` and the OAuth endpoints (`/.well-known/oauth-*`,
`/oauth/*`) are served by the same process and port. The reverse proxy must
pass them through unchanged and must not buffer responses of `/mcp` (it
streams server-sent events). See `MCP.md`.

## Health check and shutdown

- `GET /healthz` returns `200 {"status":"ok"}` without authentication and
  without any other information. The image's `HEALTHCHECK` runs
  `/usr/local/bin/relyper-healthcheck`, which probes the loopback address
  derived from `BACKREST_PORT`.
- `tini -g` forwards `SIGTERM` to the whole process group, so the server cancels its
  context (scheduler and sync stop) and shuts the HTTP server down cleanly. Use a generous `stop_grace_period` (the compose file uses 2
  minutes). The container exits with code 143 (terminated by SIGTERM) after a
  normal stop.

## Easypanel

Easypanel is the primary target. Create an **App** service:

1. **Source:** GitHub repository `jonasesser/relyper-backup`, branch `main`.
2. **Build:** Dockerfile, path `Dockerfile`.
3. **Domains / Proxy:** proxy port `9898`. Easypanel terminates TLS; always
   use the HTTPS domain to sign in.
4. **Mounts:**
   - Volume `config` → `/config`
   - Volume `data` → `/data`
   - Volume `cache` → `/cache`
   - Bind mount host path → `/sources/<name>` for every directory to back up.
     Check whether your Easypanel version can mark a bind mount read-only. If
     it cannot, the container can write there: protect the data by file
     permissions and keep that risk in mind.
   - Bind mount or volume → `/restore` for restore staging.
5. **Environment:** `TZ=Europe/Berlin` (or your zone). Nothing else is required.
6. **Hostname:** keep the service name stable. restic stores the hostname in
   every snapshot, and Backrest uses it to match snapshots to plans.

Where Easypanel keeps the data of other projects on the host (volumes,
`/etc/easypanel`) has to be verified on the actual server before it is
mounted; Relyper Backup does not assume any path yet. Automatic Easypanel
discovery is planned for Phase 3.

## Privileged options and their risk

| Option | What it allows | Risk |
|---|---|---|
| Docker socket (`/var/run/docker.sock`), even `:ro` | hook scripts using `docker`, the planned Docker discovery | The Docker API can start privileged containers that mount the host's root filesystem. **Access to the socket is equivalent to root on the host**, the `:ro` flag on the socket file does not restrict the API. Prefer a filtering socket proxy that only allows read endpoints (`GET /containers`, `/volumes`, `/networks`, `/services`). |
| Broad read-only host mount | backing up arbitrary host paths | Every admin of Relyper Backup can read every mounted file, including secrets on the host. |
| Writable production mounts | restoring in place | A wrong restore or a compromised instance can overwrite production data. Restore to `/restore` instead. |
| `privileged: true`, `cap_add` | not needed | Not required by any feature. Do not use. |

## Backing up Relyper Backup itself

Everything needed to recreate an instance is in `/config`:
`config.json` (repositories including their passwords, plans, users with
password hashes) and `.backrest-ssh/` (SSH private keys and trusted host
keys). `/data` holds history, logs, MCP access tokens and the MCP audit log;
it can be lost without losing any backup (MCP clients then have to connect
again).

- Keep an offline copy of `config.json` and the SSH keys in a password
  manager or another secure place **outside the protected host**.
- Above all, store every **repository password** outside the host. Without
  it, the data in the repository cannot be decrypted by anyone – not by
  Relyper Backup, not by restic. See `DISASTER-RECOVERY.md`.
- A plan that backs up `/config` into the same repository is useful for
  history, but it cannot replace the offline copy: you need the password to
  open that repository in the first place.

## Updating

```bash
git pull
docker compose up -d --build
```

Configuration migrations run automatically on start (upstream
`internal/config/migrations`). Read `CHANGELOG.md` before updating.
