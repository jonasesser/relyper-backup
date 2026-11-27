# Roadmap

Relyper Backup – Intelligent backup management. Your infrastructure, your
data, your control.

Status markers: **done** (built, tested, on `main`), **open** (not started),
**partial** (some parts done, the rest listed). Issue numbers refer to
`jonasesser/relyper-backup`. Nothing marked open is shown in the UI as if it
existed.

The source specification is the owner's concept in `docs/CONCEPT.md`. The
section "Gaps against the concept" at the end lists what it asks for that is
not built yet.

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

## Phase 7 – Backup as a service for customers (in progress)

Design: `docs/CONCEPT-MULTITENANT.md` (#25).

| Item | Status | Notes |
|---|---|---|
| Design: tenants, isolation, notifications, own storage, recovery kit, escrow, threat model | done | #25 |

## Open decisions

| Topic | State |
|---|---|
| Upstream GitHub workflows and dependabot | release-please removed (#16). The other upstream workflows and dependabot are still active; proposal: replace with a Relyper CI (build, test, image). Needs the owner's decision. |
| Registry for the image | Not decided; today the image is built from source (`DEPLOYMENT.md`). |
| Append-only / immutable storage | To be evaluated (rest-server `--append-only`, S3 Object Lock). See `SECURITY.md`. |
| Encrypted secret storage | Planned; today `config.json` holds secrets in plaintext (upstream). |

## Gaps against the concept

What `docs/CONCEPT.md` asks for and is not (fully) built yet, by section:

| Section | Gap |
|---|---|
| 4 UI | Navigation has Dashboard, Infrastructure (= Applications), Plans, Repositories, MCP Integration, Settings. No separate Snapshots, Restore and Activity pages; snapshots and restores live on the plan and repository pages (upstream). |
| 5 MCP | `discover_apps` covers `list_docker_containers`, `list_docker_services`, `list_docker_volumes`, `list_easypanel_projects` and `list_detected_databases`; `get_recovery_readiness` covers `get_disaster_recovery_status`; `dump_app_databases` covers `run_database_backup`. Tools under the exact concept names do not exist. `stop_container` / `restart_container` are not offered (read-only towards production). |
| 6 Docker | Not reported: networks, restart policies, resource limits, dependencies between services. Data is classified as data, database, live database files; no separate configuration / temporary / cache classes. |
| 7 Easypanel | Detected from Docker metadata and paths only, not verified on a real server; no Easypanel API; domains, routing, environment variables and certificates are not read. |
| 8 Databases | Whole-server dumps only (pg_dumpall, all MySQL/MariaDB databases); no per-database selection; no explicit connection validation step; version compatibility is reported only as the image used for the dump. MongoDB is supported in addition. |
| 9 Coverage | States differ from the concept's names (protected, stale, failing, not in snapshot, unprotected, not reachable, …); "Restore not verified" is part of Readiness. No filtering. |
| 10 Repositories | No guided repository wizard beyond the upstream form and the SFTP diagnostics; Google Drive / Dropbox only via rclone; Host Europe not tested. |
| 11 Setup wizard | One plan per app; no "whole environment / whole project" selection, no storage estimate, defaults fixed at daily 02:30 and 7/4/6 (editable on the plan page). |
| 12 Dashboard | Upstream dashboard plus the Infrastructure summary; no combined health dashboard. Notifications only for coverage problems and failed restore tests (not success, low storage or failed checks). |
| 13 Disaster recovery | Recovery report with images, paths, test results and restic commands; no guided restore workflows, restoration order, network dependencies or bootstrap tool. |
| 15 Security | Roles only for Relyper Identity users on MCP functions; Backrest pages have no roles. Secrets in `config.json` are not encrypted (upstream). |
| 17 Deployment | No Easypanel template yet; MCP endpoint path is fixed at `/mcp`. |
