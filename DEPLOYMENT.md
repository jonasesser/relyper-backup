# Deployment

Relyper Backup ships as **one Docker image** built from the `Dockerfile` in
this repository. The container runs the complete product: web UI, API and
scheduler (one Go process), plus the tools it drives – restic, rclone and the
OpenSSH client for SFTP repositories. No database container or separate MCP
container is needed. Only application-aware backup (Docker discovery and
database dumps) adds a second, tiny container: the Relyper Docker guard,
the only place that holds the Docker socket.

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
(`internal/resticinstaller`, currently 0.20.0) and verifies its SHA-256 sums
and signature before it is copied into the image.

Network access needed at build time: Docker Hub (base images),
`registry.npmjs.org`, `proxy.golang.org`, `github.com` (restic release),
`dl-cdn.alpinelinux.org` (runtime packages). The runtime stage runs
`apk upgrade`, so a rebuild picks up Alpine security fixes; the Go stage
uses the newest `golang:1.26` patch release. Rebuild regularly (e.g.
monthly and after Go or Alpine security announcements).

## Container hardening

`docker-compose.yml` and `docker-compose.apps.yml` run Relyper Backup with

- `read_only: true` – the image cannot be changed at runtime; only the
  volumes `/config`, `/data`, `/cache`, the restore staging directory and a
  `tmpfs` on `/tmp` are writable;
- `cap_drop: [ALL]` and only `CHOWN`, `DAC_OVERRIDE`, `DAC_READ_SEARCH`,
  `FOWNER`, `FSETID` added back – what restic needs to read every file of
  the sources and to restore owners and permissions; no network, mount,
  module, ptrace or admin capabilities;
- `no-new-privileges:true`.

The process runs as root *inside* the container with these few
capabilities. A non-root user cannot read Docker volume data owned by other
users or restore file owners, so non-root is not offered. On hosts with
user namespace remapping (`userns-remap`) container root is not host root.

`/tmp` is a RAM disk (`tmpfs`); restic keeps temporary pack files there
during a backup. On hosts with very little RAM set `TMPDIR` to a directory
on a volume instead (e.g. `/cache/tmp`, created beforehand).

rclone keeps its configuration in `/root/.config/rclone` by default, which is
read-only now: set `RCLONE_CONFIG=/config/rclone/rclone.conf` when you use
rclone repositories.

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

| `RELYPER_OIDC_*` | unset | Sign-in with Relyper Identity; see `IDENTITY.md`. |
| `RELYPER_GUARD_URL` | `DOCKER_HOST`, else `unix:///var/run/docker.sock` | Address of the Relyper Docker guard, e.g. `tcp://docker-guard:2375`; used for discovery, dumps and restore tests. |
| `RELYPER_HOOK_COMMANDS` | `all` | `dbdump`: command hooks may only run the `relyper-dbdump` command of app plans (and the `relyper-appexport` command of Relyper Budget customer plans); any other shell hook fails without running. `all` (upstream behaviour): any command. The compose files set `dbdump`. |
| `RELYPER_GUARD_TOKEN` | unset | Token for the Relyper Docker guard: the same variable with the same value on both services. Secret; never shown or logged. |
| `RELYPER_HOST_PATH_MAP` | unset | Host paths mounted elsewhere in the container, e.g. `/srv=/sources/srv,/opt/data=/sources/opt`. |
| `RELYPER_DUMP_DIR` | `<data>/relyper/dumps` | Where database dumps are written before the backup. |
| `RELYPER_RESTORE_DIR` | `/restore` if mounted, else `<data>/relyper/restore` | Where the guided app restore (Infrastructure → Restore…) stages data. Mount it writable; never point it at production data. |
| `RELYPER_COVERAGE_MAX_AGE_HOURS` | `48` | A snapshot older than this counts as stale. |
| `RELYPER_VERIFY_INTERVAL_DAYS` | `7` | Automatic database and file restore tests; `0` turns them off. |
| `RELYPER_NOTIFY_URLS` | unset | Notification targets as [shoutrrr URLs](https://shoutrrr.nickfedor.com/), separated by spaces. |
| `RELYPER_NOTIFY_SUCCESS` | `false` | Also notify about successful backups. |
| `RELYPER_LOW_STORAGE_PERCENT` | `10` | Notify when the backup storage has less free space (0 = off). |
| `RELYPER_SELF_CONTAINER` | the hostname | Name or ID of Relyper Backup's own container, excluded from discovery. |
| `RELYPER_PUBLIC_URL` | unset | Public HTTPS address, e.g. `https://backup.example.com`. Used in the OAuth metadata of the MCP server; without it the address is derived from the request headers. |

The MCP endpoint `/mcp` and the OAuth endpoints (`/.well-known/oauth-*`,
`/oauth/*`) are served by the same process and port. The reverse proxy must
pass them through unchanged and must not buffer responses of `/mcp` (it
streams server-sent events). See `MCP.md`.

## Health check and shutdown

- `GET /healthz` returns `200 {"status":"ok"}` without authentication and
  without any other information. The image's `HEALTHCHECK` runs
  `/usr/local/bin/relyper-healthcheck`, which probes the loopback address
  derived from `BACKREST_PORT`. During the first 60 s it checks every 5 s
  (`--start-interval`, Docker 25 or newer), afterwards every 30 s.
- `tini -g` forwards `SIGTERM` to the whole process group, so the server cancels its
  context (scheduler and sync stop) and shuts the HTTP server down cleanly. Use a generous `stop_grace_period` (the compose file uses 2
  minutes). The container exits with code 143 (terminated by SIGTERM) after a
  normal stop.

## Easypanel

Easypanel is the primary target. You create up to **two App services in
the same project**. Each service has its own mounts and environment; nothing
is set twice.

| Service | Needed for | Gets the Docker socket |
|---|---|---|
| `relyper-backup` | always | **no** |
| `docker-guard` | only for Docker apps and database dumps | yes, only this one |

### Service `relyper-backup`

1. **Source:** GitHub repository `jonasesser/relyper-backup`, branch `main`.
2. **Build:** Dockerfile, path `Dockerfile`.
3. **Domains / Proxy:** proxy port `9898`. Easypanel terminates TLS; always
   use the HTTPS domain to sign in.
4. **Mounts** (all on this service):
   - Volume `config` → `/config`
   - Volume `data` → `/data`
   - Volume `cache` → `/cache`
   - Bind mount or volume → `/restore` for restore staging.
   - Bind mount host path → `/sources/<name>` for every directory to back up.
   - Only with `docker-guard`: bind mount `/var/lib/docker/volumes` →
     `/var/lib/docker/volumes` and `/etc/easypanel` → `/etc/easypanel`
     (same path inside as on the host). These are the app data, the compose
     directories (with `.env`) and Easypanel's own settings (domains,
     Traefik routing, certificates) that get backed up.
   - **Never** `/var/run/docker.sock` on this service.

   Mark the bind mounts read-only if your Easypanel version offers it. If it
   cannot, the container can write there: protect the data by file
   permissions and keep that risk in mind.
5. **Environment:**
   - `TZ=Europe/Berlin` (or your zone).
   - Only with `docker-guard`:
     `RELYPER_GUARD_URL=tcp://<project>_docker-guard:2375` (Easypanel's
     internal host name is `<project>_<service>`; check it on the service
     page) and `RELYPER_GUARD_TOKEN=<token>` (same name and value as on
     `docker-guard`).
6. **Hostname:** keep the service name stable. restic stores the hostname in
   every snapshot, and Backrest uses it to match snapshots to plans.

### Service `docker-guard` (optional, for Docker apps and databases)

Create a random token first, e.g. `openssl rand -hex 32`. The same token goes
into both services.

1. **Source:** the same repository and branch.
2. **Build:** Dockerfile, path `Dockerfile.docker-guard`.
3. **Domains / Proxy:** none. Publish no domain and no port.
4. **Mounts:** only one bind mount, `/var/run/docker.sock` →
   `/var/run/docker.sock`.
5. **Environment:** `RELYPER_GUARD_TOKEN=<token>`, `RELYPER_GUARD_DUMPS=1`,
   `RELYPER_GUARD_VERIFY=1` (and `RELYPER_GUARD_LIFECYCLE=1` only if MCP
   clients should stop/restart containers).

Deploy both and press Refresh on the Infrastructure page: it must show the
green "Docker guard" badge. While Docker is not reachable the page shows
these steps too. Coming from an older setup: delete the
`tecnativa/docker-socket-proxy` service and any socket mount on
`relyper-backup`. Setups with the
old names `RELYPER_DOCKER_HOST` / `RELYPER_DOCKER_TOKEN` keep working; rename
them when convenient.

Easypanel runs services as Swarm services named `<project>_<service>` and
keeps bind-mounted data under `/etc/easypanel/projects/<project>/<service>/`;
Relyper Backup makes every Easypanel service its own app
(`easypanel:<project>_<service>`) and shows the Easypanel project only as a
label. To group services the way you think of them (also across Easypanel
projects) give them tags on the Infrastructure page ("Tags"): each tag forms
a tag group that can get one backup plan with the data and database dumps
of all its services. Tags exist only in Relyper Backup; they are stored in
`relyper-tags.json` next to `config.json`, so a backup of the config
directory keeps them. Plans created for a whole Easypanel project before
(`easypanel:<project>`) keep working and keep dumping all databases of the
project. These conventions have not yet been
checked on a real Easypanel server – look at the Infrastructure page after
the first start and report anything that is grouped wrongly. Details of what
the guard allows: "Relyper Docker guard" below.

Easypanel's UI cannot set `read_only`, `cap_drop` or `no-new-privileges` for
App services, so on Easypanel the container hardening of the compose files
is not applied; the guard still keeps the socket away from Relyper Backup.
The guard is reachable on the project's internal network by every service of
that project, which is why the token is required there.

**Zero Downtime works.** Only one process may use the data directory, so
during a zero-downtime deploy the two containers hand over:

1. The new container finds `/data` in use, asks the old one for a handover
   (file `relyper-handover.request` in `/data`) and answers its health check
   with 503 while it waits.
2. The old container lets running backups, restores, prunes and checks
   finish (at most `RELYPER_HANDOVER_MAX_SECONDS`, default 100) and confirms.
3. The new container reports healthy, Easypanel stops the old one, the new
   one takes over the data directory and starts.

With an idle old container the handover takes a few seconds: the old one
confirms within 2 s and the health check of the new one runs every 5 s
while it starts. The UI is unavailable for the few seconds between the old
container stopping and the new one starting. If an operation runs longer than the
maximum wait, it is interrupted; restic handles that safely (no snapshot,
the next run redoes it). Keep **one replica**; two replicas on one `/data`
cannot work. An old image without handover support (before #37) does not confirm;
it is stopped after the maximum wait, so that one deploy takes about two
minutes. `RELYPER_LOCK_WAIT_SECONDS` (default 300) is how long the new
container waits for the old one to stop before it gives up and restarts.

## Application-aware backup (Docker apps and databases)

Use `docker-compose.apps.yml` instead of `docker-compose.yml`:

```bash
echo "RELYPER_GUARD_TOKEN=$(openssl rand -hex 32)" > .env
docker compose -f docker-compose.apps.yml up -d --build
```

It adds three things:

1. **The Relyper Docker guard** (`Dockerfile.docker-guard`,
   `cmd/relyper-docker-guard`): a separate container of about 10 MB (one
   static binary, no shell) that holds the Docker socket. Relyper Backup
   itself has **no** access to the socket; it reaches the guard over an
   internal network (`RELYPER_GUARD_URL=tcp://docker-guard:2375`) with a
   token (`RELYPER_GUARD_TOKEN`, set to the same value in both services). See
   "Relyper Docker guard" below for what it allows.
2. **Docker volumes at their real path**, read-only:
   `/var/lib/docker/volumes:/var/lib/docker/volumes:ro`. Plans use the paths
   Docker reports, so they must exist at the same path inside the container.
3. **Bind-mounted app data**, read-only, either at the same path or mapped
   with `RELYPER_HOST_PATH_MAP` (the file maps `/srv` to `/sources/srv`).
   Data that is not mounted shows as "not reachable" on the Infrastructure
   page. For Easypanel add `/etc/easypanel:/etc/easypanel:ro` (app data in
   `projects/` plus Easypanel's settings, domains, Traefik configuration and
   certificates).

Then open **Infrastructure** in the web UI (or use `discover_apps` through
MCP), pick an app, choose a repository and create its backup plan. The plan:

- runs `relyper-dbdump` as a hook right before every backup; it dumps each
  database inside its own container with the engine's tool, so the database
  password never leaves that container; a failed dump fails the backup;
- backs up the dump directory, the compose directory (compose files, `.env`,
  other files), for Easypanel apps the service folder
  (`/etc/easypanel/projects/<project>/<service>`), and the app's volumes and
  bind mounts, without the live database files. The
  preview lists every item with a checkbox; cache and temporary mounts are
  off by default, the Docker socket is never included;
- runs daily at 02:30 and keeps 7 daily, 4 weekly and 12 monthly snapshots
  (change it on the plan page).

To keep a snapshot no matter what the retention says (for example a known
good state before a big change, in case a problem shows up weeks later),
open it on the plan page and choose "Pin (keep forever)". A pinned snapshot
carries the restic tag `relyper-pinned`: every retention run keeps it
(`restic forget --keep-tag relyper-pinned`) and it cannot be forgotten until
you unpin it. Infrastructure → Storage lists the pinned snapshots of each
repository. restic rewrites a snapshot when its tags change, so it gets a new
ID. MCP clients can pin (`pin_snapshot`) but not unpin.

Easypanel's own settings (everything in `/etc/easypanel` except
`projects/`: environment variables, domains, ports, Traefik routes,
certificates) are shared by all services, so they are only in the plan of
the app "Easypanel" (`system:easypanel`, plan `app-system-easypanel`). To
start small, choose "Easypanel configuration only (no app data)" in "Set up
backups": one plan with these settings and what each Easypanel service
needs to deploy again: the deployment files at the top of `code/`
(`.env*`, compose files, `Dockerfile*`, `nixpacks.toml`, `Procfile`,
`Caddyfile`, `*.conf`) and the files at the top of the service folder. The
whole `code/` only for code that was uploaded or written in Easypanel: the
service runs an image Easypanel built (`easypanel/<project>/<service>`) and
`code/` is no Git checkout. Code from Git, Docker images and app templates is
fetched again on deploy and left out; the preview lists the source of every
service ("Where the code comes from"). A service that is not running has an
unknown source and gets only its deployment files. Easypanel's own backups
and action logs and the other folders of a service (`data/`, mounts) are not
in it either, nor `volumes/`, app volumes and databases; `.git`, `node_modules`, `dist`, `build`, `.next`, caches and virtual
environments are excluded everywhere in this plan. The preview shows the measured size of every path. Add the app
plans later.

Read-only binds that contain the data of other apps (such as
`/var/lib/docker/volumes` and `/etc/easypanel` of Relyper Backup itself)
are source mounts: they are never part of a plan of the app that mounts
them, also when Relyper Backup cannot recognize its own container.

Restore tests run weekly (`RELYPER_VERIFY_INTERVAL_DAYS`) and on demand. A
database restore test starts a container from the database's own image
without network, labelled `relyper.verify=true`, restores the dump from the
snapshot, compares the counts and removes the container again; the image
must be present on the host.

Limits:

- Swarm services running on other nodes are listed as `remote`; their
  volumes are only reachable on their own node.
- Databases are detected by image name (postgres, mysql, mariadb, mongo,
  redis and common variants such as bitnami or postgis). Others are backed
  up as files only.
- `RELYPER_DUMP_DIR` must be inside the container's own storage (default
  `/data/relyper/dumps`); the dumps take as much space as the databases.

### Relyper Docker guard

Whoever can use the Docker API freely is root on the host: one
`docker run --privileged -v /:/host` is enough. A generic socket proxy that
allows `exec` and `create` (as Relyper Backup used before) does not change
that. The guard therefore does not filter by endpoint but by content. It
knows the exact calls Relyper Backup makes and refuses everything else with
`403 blocked by relyper-docker-guard: …`:

| Allowed | Conditions |
|---|---|
| Reading containers, volumes, Swarm services and tasks, system info | Always. Values of environment variables are removed (other apps keep their secrets); names stay. Only database connections stay readable without credentials (`DATABASE_URL=postgres://host:5432/name`, `DB_HOST=host`, SQLite file paths), so the Infrastructure page can show which database an app uses. |
| Database dumps (`RELYPER_GUARD_DUMPS=1`) | Only `sh -c <script>` where the script is one of the fixed dump and statistics scripts of `internal/relyper/dbdump` for the detected engine; only in running containers of a known database image (postgres, mysql, mariadb, mongo, redis and variants); no extra environment variables, no other user, no TTY. |
| Restore tests (`RELYPER_GUARD_VERIFY=1`) | Create only containers named `relyper-verify-<hex>`, from an image that already runs as a database on the host (never pulled), with network `none`, no mounts, not privileged, `no-new-privileges`, a process limit, label `relyper.verify=true` and only random hex passwords as environment. The guard builds the request itself. Start, copy the dump into the fixed restore directory, run the fixed restore scripts: only for containers this guard created. Remove: only containers labelled `relyper.verify=true` with that name pattern. |
| Stop and restart containers (`RELYPER_GUARD_LIFECYCLE=1`) | Off by default. Needed for the MCP tools `stop_container` / `restart_container` (they also need an approval in the web UI). |

Everything else is refused: images, build, networks, volumes create/remove,
secrets, configs, plugins, swarm, system, `attach`, `update`, `kill`,
file copies out of containers, exec of any other command, and any request
body with fields the guard does not know. Each allowed write and each
refusal is logged on the guard's stdout (without environment values).

Options of the guard:

| Variable | Default | Meaning |
|---|---|---|
| `RELYPER_GUARD_TOKEN` | unset | Required bearer token. Without it, every container on the guard's network could use it (a warning is logged). |
| `RELYPER_GUARD_DUMPS` | off | Allow the fixed dump scripts. |
| `RELYPER_GUARD_VERIFY` | off | Allow restore-test containers. |
| `RELYPER_GUARD_LIFECYCLE` | off | Allow stop/restart of containers. |
| `RELYPER_GUARD_REQUIRE_LABEL` | off | Dumps and restore-test images only for containers labelled `relyper.dump=true` (opt-in per database). |
| `RELYPER_GUARD_LISTEN` | `:2375` | Listen address. Never publish this port. |
| `RELYPER_GUARD_UPSTREAM` | `unix:///var/run/docker.sock` | Docker socket. |

With no write option set the guard is read-only: discovery and coverage
work, database dumps and restore tests fail with a clear message.

What remains possible for someone who takes over Relyper Backup: read the
database contents through the dump scripts (the backups contain them anyway),
start isolated restore-test containers, see container metadata (not
environment values), and stop or restart containers if
`RELYPER_GUARD_LIFECYCLE=1`. Not possible: run own commands in your
containers, start privileged containers, mount host paths, pull images.
The guard itself is the component that must stay trustworthy: it has no
shell, no other API, runs read-only with all capabilities dropped and should
only be reachable from Relyper Backup.

The Infrastructure page shows a green "Docker guard" badge when Relyper
Backup talks to the guard and an orange "Unrestricted Docker access" badge
when it is connected to the raw socket or another proxy.

### Notifications

Set `RELYPER_NOTIFY_URLS` to one or more
[shoutrrr](https://shoutrrr.nickfedor.com/) URLs, separated by spaces, for
example `ntfy://ntfy.sh/<topic>`, `telegram://<token>@telegram?chats=<id>`
or `smtp://user:pass@host:587/?from=a@example.com&to=b@example.com`.
Relyper Backup sends a message when

- a backup, repository check, prune, forget or restore fails, or a backup
  finishes with warnings (for every plan and repository, as it happens);
- a backup succeeds – only with `RELYPER_NOTIFY_SUCCESS=true`;
- a scheduled plan is overdue (latest snapshot older than
  `RELYPER_COVERAGE_MAX_AGE_HOURS`), and when it is up to date again;
- the coverage of an app with a plan becomes stale, failing or unprotected,
  and when it is protected again;
- a database or file restore test fails;
- the backup storage has less than `RELYPER_LOW_STORAGE_PERCENT` (default
  10) percent free, and when it has enough again. Free space is measured
  hourly for local repositories and daily for SFTP repositories with a key
  (the server must support the statvfs extension); other backends do not
  report free space.

The same subject is sent at most once per hour. After a restart an open
problem is reported once more. Upstream's per-plan hooks (Discord, Slack,
Gotify, Healthchecks, …) keep working in addition. The URLs contain
secrets; they are never shown in the UI or logs.

## Privileged options and their risk

| Option | What it allows | Risk |
|---|---|---|
| Docker socket (`/var/run/docker.sock`), even `:ro` | discovery, dumps and restore tests | The Docker API can start privileged containers that mount the host's root filesystem. **Access to the socket is equivalent to root on the host**, the `:ro` flag on the socket file does not restrict the API. Never mount it into Relyper Backup; mount it only into the Relyper Docker guard (see above). |
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
