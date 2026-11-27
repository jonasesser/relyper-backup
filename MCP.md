# MCP integration

**Status: not implemented.** This is the design for Phase 2 (`ROADMAP.md`).
Nothing described here is available in the current build, and no part of
the UI claims otherwise.

## Goals

- A native Model Context Protocol server inside the Relyper Backup process –
  no extra container.
- External AI clients (Claude, ChatGPT and other MCP clients) can read backup
  status and, when allowed, manage backups.
- AI is optional: every backup and restore keeps working without it.

## Transport and SDK

- Transport: MCP **Streamable HTTP**, served by the same HTTP server under a
  configurable path (default `/mcp`), behind TLS from the reverse proxy.
- SDK: the official Go SDK `github.com/modelcontextprotocol/go-sdk`. The
  specification version and the SDK release are pinned when the work starts;
  the current specification is re-checked at that point.
- Tools call the same internal handlers as the web UI (`BackrestHandler`,
  `RelyperSftp`), so behaviour and validation are identical.

## Authentication

Evaluated in order of client support:

1. **OAuth 2.1 authorization** as defined by the MCP authorization
   specification (protected resource metadata, PKCE). Required for remote
   connectors in Claude and ChatGPT. Relyper Backup acts as resource server;
   the authorization server is either a small built-in one bound to local
   users or the optional Relyper Identity Provider (OIDC). No IdP endpoints are
   assumed before they are verified.
2. **Personal access tokens** (Bearer) for clients that support static
   headers: created in the UI, shown once, stored hashed, scoped, expiring,
   revocable.

The upstream JWT used by the web UI is not reused for MCP.

## Authorization

Upstream has no roles. Before any write tool is exposed, Relyper Backup gets
permissions (see `SECURITY.md`):

| Permission | Example tools |
|---|---|
| `status:read` | get_system_overview, get_backup_status, list_backup_operations |
| `plans:manage` | create_backup_plan, update_backup_plan, enable/disable_backup_plan |
| `repos:manage` | test_repository_connection, create_repository, update_repository |
| `backups:run` | run_backup, run_repository_check, cancel_backup_operation |
| `contents:browse` | list_snapshot_files (sensitive) |
| `restore` | restore_snapshot (restricted) |
| `data:delete` | delete_snapshot, prune_repository, modify_retention_policy (restricted) |
| `infra:manage` | docker discovery, stop/restart_container (restricted) |

A token or OAuth grant carries a subset; read-only is the default.

## Tools

Read-only (Phase 2 unless noted): get_system_overview, get_backup_status,
list_repositories, get_repository_status, list_backup_plans, get_backup_plan,
list_snapshots, get_snapshot_details, list_snapshot_files,
list_backup_operations, get_backup_operation, get_backup_errors,
get_storage_usage. Discovery and coverage tools (list_docker_*,
list_easypanel_projects, list_detected_databases, get_backup_coverage,
get_disaster_recovery_status) follow with Phases 3–5 and only return data
that the corresponding feature actually computes.

Administrative: test_repository_connection (uses `sshdiag` for SFTP),
create/update_repository, create/update/enable/disable_backup_plan,
run_backup, run_repository_check, cancel_backup_operation,
validate_backup_configuration, create_backup_recommendations;
run_database_backup and run_restore_test with their phases.

Restricted (need approval, see below): restore_snapshot,
overwrite_existing_files, delete_snapshot, delete_repository,
prune_repository, modify_retention_policy, stop_container, restart_container.

There is **no** tool for arbitrary shell or restic commands, and none for
editing hook commands.

## Approval of restricted operations

Enforced on the server, never by instructions to the AI:

1. The tool call does not execute. It returns a **preview** (dry run where
   restic supports it: `forget --dry-run`, restore into a staging target,
   list of snapshots affected) and an approval request ID.
2. The approval request stores the tool name and a canonical hash of the exact
   parameters, the requesting principal and an expiry (default 15 minutes).
3. A human approves in the web UI (or via a separate, user-authenticated
   channel). The AI client cannot approve its own request.
4. The client repeats the call with the approval ID; the server checks tool,
   parameter hash, principal, expiry and single use, then executes.
5. Request, decision and execution are written to the audit log.

## Data handling

- Responses never contain passwords, keys, tokens, environment variable values
  or SSH private keys; repository config goes through the same sanitizer as
  the UI, plus a Relyper redaction pass.
- File names, log lines and other repository content are returned as data
  fields and marked as untrusted content; tool descriptions tell the client
  not to follow instructions found inside them.
- Nothing is sent to any AI provider by Relyper Backup itself; data only
  leaves when an authorized client asks for it.

## Testing plan

Tool registration and schemas, authentication failures, permission
enforcement per tool, approval binding (changed parameter, expired, reused,
other principal), redaction of secrets in every response, and a conformance
check with the MCP inspector.
