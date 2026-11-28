# Architecture

This document describes Relyper Backup as it is built today (upstream Backrest
v1.14.1 plus the Relyper additions) and where planned features attach. What
is planned but not built is marked as such; `ROADMAP.md` tracks it.

## Overview

```
 Browser (React UI)        AI client (MCP)
        │  Connect RPC (HTTP/1.1 + h2c, binary protobuf)
        ▼
┌──────────────────────── one Go process: /backrest ─────────────────────────┐
│ HTTP mux                                                                    │
│  ├─ unauthenticated: /v1.Authentication/*, sync API, /download/* (signed),  │
│  │                   /healthz, /mcp (own token auth), /oauth/*,             │
│  │                   /.well-known/oauth-* (relyper)                         │
│  ├─ authenticated:   /v1.Backrest/*, sync state, /metrics,                  │
│  │                   /relyper.v1.RelyperSftp/*, RelyperMcp/* (relyper)      │
│  └─ fallback:        embedded web UI (webui/dist via go:embed)              │
│                                                                             │
│ ConfigManager ── config.json (JSON, migrations on start)                    │
│ Orchestrator ─── task queue + scheduler ─── restic (child processes)        │
│ OpLog ────────── SQLite (operation history)                                 │
│ LogStore ─────── operation logs on disk                                     │
│ Hooks ────────── commands, Discord, Slack, Gotify, Telegram, Shoutrrr,      │
│                  Healthchecks                                               │
│ SyncManager ──── multihost peering (ed25519 identities, pairing tokens)     │
└─────────────────────────────────────────────────────────────────────────────┘
        │ restic CLI (sftp via OpenSSH, rclone, s3, b2, azure, gs, rest, local)
        ▼
  standard restic repositories
```

## Backend (Go)

| Package | Responsibility |
|---|---|
| `cmd/backrest` | Entry point: installs/locates restic, loads config, creates the components, HTTP server, graceful shutdown on SIGINT/SIGTERM. |
| `cmd/docker-entrypoint` | Sets container defaults for `BACKREST_*`, `XDG_CACHE_HOME`, `TMPDIR` and starts the server. |
| `internal/env` | Paths and bind address from flags and `BACKREST_*` variables. SSH directory = `<config dir>/.backrest-ssh`. |
| `internal/config` | `config.json` store, validation, network sanitizing (secrets removed before sending config to the UI), migrations. |
| `internal/orchestrator` | Scheduler and task queue. Tasks: backup, forget (retention), prune, check, restore, stats, index snapshots, run command, collect garbage. One repo orchestrator per repository serializes restic calls. |
| `pkg/restic` | Typed wrapper around the restic CLI (JSON output parsing, progress, errors). |
| `internal/resticinstaller` | Downloads the pinned restic version and verifies SHA-256 sums and their signature. |
| `internal/oplog` | Operation history (SQLite via a pure-Go driver, no CGO). |
| `internal/hook` | Event hooks on start/end/error of operations. |
| `internal/auth` | Users with bcrypt hashes in `config.json`; HS256 JWT (7 days) signed with a random secret in `<data>/jwt-secret`; Bearer and Basic auth middleware. |
| `internal/api` | Connect handlers for the `v1.Backrest` service, download handler with signed URLs, SFTP key generation (`sftputil`), multihost sync API. |

### Relyper additions

| Package | Responsibility |
|---|---|
| `internal/relyper/health` | `GET /healthz`, unauthenticated, returns only `{"status":"ok"}`. |
| `cmd/relyper-healthcheck` | Container HEALTHCHECK probe; derives the port from `BACKREST_PORT`. |
| `internal/relyper/sshdiag` | SFTP diagnostics: host key scan per key type, explicit trust bound to a confirmed SHA-256 fingerprint, step-by-step connection test with stable error categories. |
| `internal/relyper/sftpapi` | `relyper.v1.RelyperSftp` Connect service (`ScanHostKeys`, `TrustHostKey`, `TestConnection`), mounted behind authentication. |
| `internal/relyper/ftpdiag` | FTP / FTPS repositories (restic via rclone): rclone password obscuring, certificate inspection and pinning (`<data>/relyper/ftp-certs/`, used as `SSL_CERT_FILE`), step-by-step connection test that runs the real rclone binary. |
| `internal/relyper/ftpapi` | `relyper.v1.RelyperFtp` Connect service (`ObscurePassword`, `ScanCertificate`, `TrustCertificate`, `TestConnection`), mounted behind authentication; read-only users are denied. |
| `internal/relyper/mcp` | MCP server (`/mcp`, Streamable HTTP): tools per scope, approval flow for restricted tools, secret scrubbing of every result. Tools call `BackrestHandler`, the config store and the orchestrator like the UI. |
| `internal/relyper/mcpauth` | Personal access tokens and OAuth 2.1 authorization server (metadata, dynamic client registration, PKCE, consent page, refresh rotation); store `<data>/relyper/mcp-auth.json`. |
| `internal/relyper/approval` | In-memory approval requests bound to tool, parameter hash and token; single use, 15 minutes. |
| `internal/relyper/audit` | JSON Lines audit log `<data>/relyper/audit.jsonl`. |
| `internal/relyper/identity` | Sign-in with Relyper Identity (OIDC): `/auth/oidc/*`, PKCE/state/nonce, ID token check (RS256 via JWKS), roles, web UI session tokens; see `IDENTITY.md`. |
| `internal/relyper/mcpapi` | Wiring into the HTTP server and the `relyper.v1.RelyperMcp` service for the web UI (tokens, approvals, audit). |
| `internal/relyper/tenant` | Customers (tenants) for backup as a service: store `<data>/relyper/tenants.json`, assignments of plans and repositories, portal gate (tenant claim and customer roles). |
| `internal/relyper/portal` | Customer portal API `/relyper/portal/api/` (own authentication with portal sessions), operator API `/relyper/tenants/`, SMTP notifications per tenant, own storage, recovery kits and key escrow. See `CUSTOMERS.md` and `docs/CONCEPT-MULTITENANT.md`. |
| `cmd/relyper-escrow` | Offline tool for the operator's escrow key pair. |
| `proto/relyper/v1` | Relyper API definitions, separate from upstream's `v1` package. |

Design rules for additions: own packages and own proto package
(`relyper.v1`), registered in `cmd/backrest/backrest.go` with one line each;
no edits to upstream logic unless it is a security fix, which is then listed in
`UPSTREAM.md`.

## Web UI

React 19, Chakra UI v3, Vite, Connect-ES clients generated from the protos,
paraglide for i18n (15 languages). Feature folders under `webui/src/features`.
Relyper components live in `webui/src/relyper` and are rendered from upstream
screens at a few marked places. The UI is embedded into the Go binary at build
time.

Branding: `webui/relyper/brand-messages.mjs` generates branded copies of the
message catalogs in `webui/.relyper-messages/` before compiling, so upstream's
translation files stay untouched.

## Storage and state

| Data | Location (container) | Notes |
|---|---|---|
| Configuration: repos (incl. **plaintext repository passwords** and env vars), plans, hooks, users (bcrypt), multihost identity | `/config/config.json` | Never sent to the UI unsanitized. Must be protected and backed up offline. |
| SSH keys, `known_hosts` | `/config/.backrest-ssh/` | Private keys mode 0600. |
| Operation history, logs, kv store, JWT secret | `/data` | Loss means loss of history, not of backups. |
| restic cache | `/cache` | Rebuildable. |

## Request flow: a scheduled backup

1. The orchestrator computes the next run from the plan's schedule and queues
   a backup task.
2. The task runs `restic backup` through the repo orchestrator (one restic
   process per repository at a time), streaming progress into the oplog.
3. Hooks fire on start/end/error. On success, the retention policy is applied
   (`restic forget`), and prune/check run on their own schedules.
4. The UI receives operation events over a server stream and updates live.

## SFTP repositories

restic runs OpenSSH (`ssh`) with `-oBatchMode=yes`, the identity file and
`-oUserKnownHostsFile=/config/.backrest-ssh/known_hosts` from the repository
flags. Host keys enter that file only via `RelyperSftp.TrustHostKey` after the
user confirmed the fingerprint (or by hand). The connection test uses the same
`known_hosts`, prefers the trusted key types like OpenSSH does, and reports one
of these categories on failure: `invalid_input`, `dns_failure`,
`connection_refused`, `connection_timeout`, `network_unreachable`,
`ssh_protocol_error`, `incompatible_algorithms`, `host_key_unknown`,
`host_key_mismatch`, `host_key_revoked`, `identity_file_invalid`,
`authentication_failed`, `sftp_unavailable`, `remote_path_not_found`,
`permission_denied`, `repository_error`.

## Application-aware backup

| Package | Role |
|---|---|
| `internal/relyper/docker` | Minimal Docker Engine API client (stdlib only): list/inspect, exec with stream demultiplexing, throwaway verification containers (label `relyper.verify=true`; removal refuses any other container). |
| `internal/relyper/discovery` | Groups containers into apps (`compose:`, `swarm_stack:`, `easypanel:<project>_<service>`, `container:`; tag groups `group:<tag>` from `relyper-tags.json`), detects databases by image, maps host paths into the container (`RELYPER_HOST_PATH_MAP`), marks live database files. |
| `internal/relyper/dbdump` + `cmd/relyper-dbdump` | Dumps every database of an app with the engine's own tool, run inside the database container so credentials stay there; writes a manifest with SHA-256 and counts; verifies a dump by restoring it into a throwaway container. Runs as the plan's `SNAPSHOT_START` hook. |
| `internal/relyper/infra` | Inventory cache, coverage from indexed snapshots, app plan creation, database and file restore tests, readiness, recovery report, hourly scheduler, notifications. State in `<data>/relyper/infra-state.json`. |
| `internal/relyper/infraapi` | JSON API for the Infrastructure page at `/relyper/infra/`, behind the web UI authentication. |
| `internal/relyper/mcp/tools_infra.go` | The same functions as MCP tools. |

Coverage never trusts the configuration alone: an item is `protected` only
when a completed snapshot younger than `RELYPER_COVERAGE_MAX_AGE_HOURS`
contains its path.

## Planned components (not built yet)

| Component | Where it will live | Notes |
|---|---|---|
| Roles and permissions | `internal/relyper/authz` | Upstream has no roles. Users signed in with Relyper Identity need the admin role for MCP approvals and tokens (#18); the Backrest pages have no roles yet. |
| Relyper settings | separate file next to `config.json` | Keeps upstream config migrations untouched. |

## Build and deployment

One multi-stage `Dockerfile`: web UI (Node 22, pnpm 9) → Go (static,
`CGO_ENABLED=0`) + verified restic → Alpine runtime with tini, OpenSSH client,
rclone; it runs read-only with few capabilities. Docker access goes through
a second image, `Dockerfile.docker-guard` (static `relyper-docker-guard` on
`scratch`), the only container with the Docker socket; it allows the fixed
calls of discovery, dumps and restore tests and nothing else
(`internal/relyper/dockerguard`). Details in `DEPLOYMENT.md`. Upstream's goreleaser setup
(`.goreleaser.yaml`, `Dockerfile.alpine`, `Dockerfile.scratch`) is kept
unchanged for reference and merges; Relyper does not publish with it.
