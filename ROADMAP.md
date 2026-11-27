# Roadmap

Relyper Backup – Intelligent backup management. Your infrastructure, your
data, your control.

Status markers: **done** (built, tested, on `main`), **open** (not started),
**partial** (some parts done, the rest listed). Issue numbers refer to
`jonasesser/relyper-backup`. Nothing marked open is shown in the UI as if it
existed.

## Phase 1 – Foundation (done)

| Item | Status | Notes |
|---|---|---|
| Import upstream Backrest with history | done | v1.14.1, `upstream` remote, see `UPSTREAM.md` |
| Upstream maintenance strategy | done | Relyper-owned paths, hook point table, merge procedure (`UPSTREAM.md`) |
| Builds verified | done | Go, web UI, Docker image |
| Existing tests run | done | Go: all pass except root-only environment cases (see `UPSTREAM.md`); web UI 162/162 |
| Build without CDN at build time | done | #3 – i18n plugin vendored and checksummed |
| Single Docker image that starts everything | done | #11 – tini, `/healthz`, HEALTHCHECK, compose file, `DEPLOYMENT.md` |
| SFTP configuration and diagnostics | done | #12, #13 – host key confirmed by fingerprint, step-by-step test with error categories, verified against OpenSSH |
| Branding | done | #14 – name, logo, favicon, About dialog with attribution |
| Architecture docs and roadmap | done | this file, `ARCHITECTURE.md`, `SECURITY.md`, `MCP.md`, `DISASTER-RECOVERY.md` |

Known gaps from Phase 1:

- The About dialog is reached via the version link in the sidebar, which is
  not shown on narrow screens.
- Some multihost texts still say "Backrest" because they refer to the peer
  protocol and peers may run upstream Backrest.
- SFTP was tested against OpenSSH (atmoz/sftp), not yet against the Host
  Europe storage account.

## Phase 2 – Native MCP (partial)

Details: `MCP.md`.

| Item | Status | Notes |
|---|---|---|
| MCP server (Streamable HTTP, official Go SDK v1.8.0) at `/mcp` | done | #17 |
| Personal access tokens with scopes | done | #17 – hashed, expiring, revocable; MCP credentials only work on `/mcp` |
| OAuth 2.1 for remote connectors | done | #17 – metadata, dynamic client registration, PKCE S256, rotating refresh tokens; client ID metadata documents not yet |
| Read-only tools | done | #17 – status, repos, plans, snapshots, operations, errors, storage, validation, recommendations |
| Setup and administrative tools | done | #17 – repositories (incl. SFTP key, host key scan, connection test), plans, schedules, enable/disable, backup, check, stats, cancel |
| Server-side approval for restricted operations | done | #17 – restore, delete snapshot, prune, retention, delete plan/repository, trust host key |
| Audit log | partial | #17 – MCP, approvals, tokens, OAuth; web UI config changes not yet |
| MCP page in the web UI | done | #17 – connect, approvals, tokens, audit log |
| Roles and permissions for web UI users | open | Today every web UI user can create tokens and approve requests |
| Tested with claude.ai / ChatGPT connectors | open | Needs a publicly reachable instance |
| Docker, database, coverage and recovery tools | done | #21 – discovery, coverage, app plans, dumps, restore tests, readiness, report |
| Container start/stop/restart tools | not planned | Relyper Backup only reads from production containers |

## Phase 3 – Infrastructure discovery (done, Easypanel unverified)

| Item | Status | Notes |
|---|---|---|
| Docker discovery via the Engine API | done | #20 – stdlib client, unix socket or a filtering socket proxy (`docker-compose.apps.yml`) |
| Apps: Compose projects, Swarm stacks, single containers | done | #20 |
| Easypanel projects | partial | #20 – detected from `/etc/easypanel/projects` paths and `<project>_<service>` names; not yet checked on a real Easypanel server |
| Swarm services on other nodes | partial | Listed as `remote`; their data is not reachable from this node |
| App-level backup plan (volumes + dumps) | done | #21 – one plan per app, created in the web UI or through MCP |
| Coverage based on completed snapshots | done | #21 – web UI page "Infrastructure", `get_backup_coverage` |

## Phase 4 – Database protection (done)

| Item | Status | Notes |
|---|---|---|
| Detection of PostgreSQL, MySQL, MariaDB, MongoDB, Redis | done | #20 – by image |
| Consistent dumps with the databases' own tools | done | #20 – `relyper-dbdump`, run as a hook right before each backup; passwords stay inside the database container |
| Dump manifest with checksums and counts | done | #20 |
| Restore verification in a throwaway container | done | #20, #21 – from the snapshot, compares tables/collections/keys |
| Other engines (SQLite inside app volumes, MSSQL, …) | open | SQLite files are backed up as files; no consistency guarantee |

## Phase 5 – Disaster recovery (done)

| Item | Status | Notes |
|---|---|---|
| Automated restore tests | done | #21 – database restore weekly (`RELYPER_VERIFY_INTERVAL_DAYS`), file restore test of sample files |
| Recovery readiness | done | #21 – per app: coverage, database test, file test, repository check, off-site copy |
| Recovery report / runbook | done | #21 – Markdown, no passwords; restore commands for the plain restic CLI |
| Notifications on problems | done | #21 – shoutrrr URLs (`RELYPER_NOTIFY_URLS`) |
| Guided production restore of an app | open | Production restore stays a manual, documented step |

## Phase 6 – Product refinement (partial)

| Item | Status | Notes |
|---|---|---|
| Relyper Identity (OIDC) | done | #18, `IDENTITY.md`; roles for the Backrest pages still open |
| Infrastructure page in the web UI | done | #22 |
| Compose variant with Docker socket proxy | done | `docker-compose.apps.yml` |
| Easypanel template | open | Needs a real Easypanel server to verify paths |
| Dashboard redesign | open | |

## Open decisions

| Topic | State |
|---|---|
| Upstream GitHub workflows and dependabot | release-please removed (#16). The other upstream workflows and dependabot are still active; proposal: replace with a Relyper CI (build, test, image). Needs the owner's decision. |
| Registry for the image | Not decided; today the image is built from source (`DEPLOYMENT.md`). |
| Append-only / immutable storage | To be evaluated (rest-server `--append-only`, S3 Object Lock). See `SECURITY.md`. |
| Encrypted secret storage | Planned; today `config.json` holds secrets in plaintext (upstream). |
