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
| `internal/relyper/mcp` | MCP server (`/mcp`, Streamable HTTP): tools per scope, approval flow for restricted tools, secret scrubbing of every result. Tools call `BackrestHandler`, the config store and the orchestrator like the UI. |
| `internal/relyper/mcpauth` | Personal access tokens and OAuth 2.1 authorization server (metadata, dynamic client registration, PKCE, consent page, refresh rotation); store `<data>/relyper/mcp-auth.json`. |
| `internal/relyper/approval` | In-memory approval requests bound to tool, parameter hash and token; single use, 15 minutes. |
| `internal/relyper/audit` | JSON Lines audit log `<data>/relyper/audit.jsonl`. |
| `internal/relyper/mcpapi` | Wiring into the HTTP server and the `relyper.v1.RelyperMcp` service for the web UI (tokens, approvals, audit). |
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

## Planned components (not built yet)

| Component | Where it will live | Notes |
|---|---|---|
| Roles and permissions | `internal/relyper/authz` | Upstream has no roles: every signed-in user is an administrator (and can create MCP tokens and approve MCP requests). |
| Docker / Easypanel discovery | `internal/relyper/discovery` | Docker Engine API, read-only, optional; Easypanel paths verified on a real host first. |
| Database dumps | `internal/relyper/dbdump` | pg_dump / mysqldump / mariadb-dump / Redis, staged in a dedicated directory, verified. |
| Coverage analysis, DR inventory | `internal/relyper/coverage`, `internal/relyper/dr` | Based on completed operations, not on configuration alone. |
| Relyper settings | separate file next to `config.json` | Keeps upstream config migrations untouched. |

## Build and deployment

One multi-stage `Dockerfile`: web UI (Node 22, pnpm 9) → Go (static,
`CGO_ENABLED=0`) + verified restic → Alpine runtime with tini, OpenSSH client,
rclone, docker-cli. Details in `DEPLOYMENT.md`. Upstream's goreleaser setup
(`.goreleaser.yaml`, `Dockerfile.alpine`, `Dockerfile.scratch`) is kept
unchanged for reference and merges; Relyper does not publish with it.
