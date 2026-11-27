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
| Docker, Easypanel, database and container tools | open | With Phases 3–4; not registered before they exist |

## Phase 3 – Infrastructure discovery (open)

- Docker discovery via the Engine API, read-only, behind a filtering socket
  proxy where possible.
- Docker Swarm services and volumes.
- Easypanel projects – paths and conventions verified on a real host first.
- Application-level backup selection (one app = its volumes + database).
- Coverage analysis based on completed backups, not configuration alone.

## Phase 4 – Database protection (open)

- Database discovery (PostgreSQL, MySQL/MariaDB, Redis, …).
- Consistent dumps with the databases' own tools into a staging directory,
  then restic.
- Application-aware workflows (dump, back up, clean up, report).
- Restore and verification into a throwaway container.

Until then, `DISASTER-RECOVERY.md` explains how to dump databases from a hook.

## Phase 5 – Disaster recovery (open)

- Recovery inventory (what is needed to rebuild, where it is).
- Guided restore workflows.
- Automated restore tests.
- Recovery readiness reporting.

## Phase 6 – Product refinement (open)

- Simpler repository and plan setup.
- Dashboard and UX refinement.
- Optional Relyper Identity (OIDC).
- More notification channels.
- Deployment templates (Easypanel template, compose variants).

## Open decisions

| Topic | State |
|---|---|
| Upstream GitHub workflows and dependabot | release-please removed (#16). The other upstream workflows and dependabot are still active; proposal: replace with a Relyper CI (build, test, image). Needs the owner's decision. |
| Registry for the image | Not decided; today the image is built from source (`DEPLOYMENT.md`). |
| Append-only / immutable storage | To be evaluated (rest-server `--append-only`, S3 Object Lock). See `SECURITY.md`. |
| Encrypted secret storage | Planned; today `config.json` holds secrets in plaintext (upstream). |
