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

## Phase 2 – Native MCP (open)

Design: `MCP.md`. Order matters – roles come before any write tool.

1. Roles and permissions (`internal/relyper/authz`), enforced in the API
   handlers, also for the web UI.
2. General audit log.
3. MCP server (Streamable HTTP, official Go SDK) with authentication
   (personal access tokens first, then OAuth 2.1).
4. Read-only tools.
5. Administrative tools (plans, repositories, run backup/check).
6. Server-side approval for restricted operations (restore, delete, prune,
   retention changes).
7. MCP configuration and status page in the web UI.

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
