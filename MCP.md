# MCP integration

Relyper Backup has a native [Model Context Protocol](https://modelcontextprotocol.io)
server. AI clients (Claude, ChatGPT, Claude Code and other MCP clients) can
read backup status and set up and manage repositories, plans and schedules.
It runs inside the Relyper Backup process – no extra container. Backups and
restores keep working without any AI client.

## Endpoint and transport

- `https://<your-domain>/mcp`, MCP **Streamable HTTP**, served on the same
  port as the web UI (default `9898`) behind your TLS reverse proxy.
- Implemented with the official Go SDK `github.com/modelcontextprotocol/go-sdk`
  v1.8.0 (protocol versions 2024-11-05 up to 2026-07-28 are negotiated by the
  SDK).
- Sessions time out after 30 minutes without use.
- Set `RELYPER_PUBLIC_URL` (e.g. `https://backup.example.com`) when the server
  runs behind a reverse proxy, so OAuth metadata contains the public address.
  Without it the address is derived from the request (`Host`,
  `X-Forwarded-Proto`, `X-Forwarded-Host`).

## Connecting a client

The web UI page **MCP Integration** shows the endpoint URL, creates tokens,
lists connected apps and is where restricted requests are approved.

| Client | How |
|---|---|
| Claude (claude.ai, Desktop, mobile), ChatGPT and other apps with remote connectors | Add a custom connector with the endpoint URL. The app registers itself (OAuth dynamic client registration), you sign in on Relyper Backup's consent page with your Relyper Backup user and choose the permissions. |
| Claude Code | `claude mcp add --transport http relyper-backup https://<domain>/mcp` (OAuth in the browser), or with a token: `... --header "Authorization: Bearer <token>"` |
| Scripts and other clients | Create an access token on the MCP page and send `Authorization: Bearer <token>`. |

The server must be reachable over HTTPS from wherever the AI app runs. For
claude.ai and ChatGPT that is their cloud, so the instance has to be reachable
from the internet (keep the web UI login strong; see `SECURITY.md`).

## Authentication

Two kinds of credentials, both scoped, both revocable on the MCP page, both
stored only as SHA-256 hashes in `<data>/relyper/mcp-auth.json` (mode 0600):

1. **Personal access tokens** (`rbk_pat_…`): created by a signed-in user,
   shown once, valid 1–366 days.
2. **OAuth 2.1** (built-in authorization server):
   - protected resource metadata (RFC 9728) at
     `/.well-known/oauth-protected-resource[/mcp]`, announced in the
     `WWW-Authenticate` header of a 401 from `/mcp`;
   - authorization server metadata (RFC 8414) at
     `/.well-known/oauth-authorization-server`;
   - dynamic client registration (RFC 7591) at `/oauth/register` for public
     clients (no client secret); redirect URIs must be `https`, `http` on
     loopback, or an app scheme;
   - authorization code flow with mandatory PKCE `S256` at `/oauth/authorize`
     and `/oauth/token`; the `iss` parameter is returned with the code;
   - access tokens (`rbk_oat_…`) live 1 hour; refresh tokens (`rbk_ort_…`)
     30 days and rotate on every use;
   - the consent page asks for the Relyper Backup user name and password,
     or offers sign-in with Relyper Identity (OIDC, see `IDENTITY.md`),
     shows where the user will be sent back to, and lets the user untick
     permissions. Five failed sign-ins per minute and address are allowed.
   - Client ID metadata documents (the newer alternative to dynamic
     registration) are not supported yet.

MCP credentials only work on `/mcp`. They are rejected by the web UI API, so
an AI client can never approve its own requests or create tokens. The web UI
session (JWT) is never accepted on `/mcp`.

## Permissions (scopes)

| Scope | Tools |
|---|---|
| `status:read` | get_system_overview, get_backup_status, list_repositories, get_repository_status, list_backup_plans, get_backup_plan, list_snapshots, get_snapshot_details, list_backup_operations, get_backup_operation, get_backup_errors, get_storage_usage, validate_backup_configuration, create_backup_recommendations, get_approval_request, discover_apps, list_docker_containers, list_docker_services, list_docker_volumes, list_easypanel_projects, list_detected_databases, get_backup_coverage, get_recovery_readiness, get_disaster_recovery_status, get_recovery_report, get_verification_results, propose_app_backup_plan, propose_backup_setup |
| `contents:browse` | list_snapshot_files (names only, never file contents) |
| `repos:manage` | create_repository, update_repository, test_repository_connection, create_sftp_key, scan_sftp_host_keys, test_sftp_connection, trust_sftp_host_key (approval) |
| `plans:manage` | create_app_backup_plan, create_backup_setup, create_backup_plan, update_backup_plan, enable_backup_plan, disable_backup_plan, delete_backup_plan (approval) |
| `backups:run` | run_backup, run_repository_check, run_repository_stats, cancel_backup_operation, dump_app_databases, run_database_backup, verify_app_databases, run_restore_test |
| `restore` | restore_snapshot (approval), overwrite_existing_files (approval) |
| `data:delete` | delete_snapshot, prune_repository, modify_retention_policy, delete_repository (all approval) |
| `infra:manage` | stop_container, restart_container (approval; the Relyper Docker guard must run with `RELYPER_GUARD_LIFECYCLE=1`) |

A client only sees the tools its scopes allow (`tools/list` is filtered), and
every call checks the scope again.

## Setting everything up through MCP

A typical conversation for an SFTP storage box:

1. `create_sftp_key` – generates an ed25519 key in `/config/.backrest-ssh`
   and returns the public key; you install it at the provider.
2. `scan_sftp_host_keys` – shows the server's fingerprints. Compare them with
   the provider's published fingerprints.
3. `trust_sftp_host_key` – needs your approval in the web UI.
4. `test_sftp_connection` – DNS, TCP, host key, login, SFTP, directory, write
   test, repository state.
5. `create_repository` – `sftp:user@host:/path`, password, identity file.
   Initializes the repository if the directory is empty. Default maintenance:
   check every Sunday 03:00, prune on the 1st of the month 04:00.
6. `create_backup_plan` – paths, excludes, schedule (cron, every N days or
   hours), retention.
7. `run_backup`, then `get_backup_status` / `validate_backup_configuration`.

For Docker apps (needs the Docker connection from `DEPLOYMENT.md`):

1. `discover_apps` – containers grouped into apps (Compose project, Swarm
   stack, Easypanel project or single container) with volumes, bind mounts
   and detected databases.
2. `get_backup_coverage` – per app and data item: protected, stale, failing,
   not in a snapshot yet, unprotected, not reachable. It is based on
   completed snapshots, not on the configuration alone.
3. `propose_app_backup_plan`, then `create_app_backup_plan` with a
   repository – one plan per app: a hook dumps the databases right before
   every backup (a failed dump fails the backup), and the plan backs up the
   dumps plus the app's files. For many apps at once use
   `propose_backup_setup` (scope `all`, `easypanel` or `apps`; shows the
   measured data size) and `create_backup_setup` (shared schedule,
   retention, excludes, upload limit, compression; defaults: databases every
   6 hours, files daily, 7 daily / 4 weekly / 12 monthly).
4. `run_backup`, then `verify_app_databases` (restores the dumps from the
   snapshot into a throwaway container and compares tables/collections/keys)
   and `run_restore_test` (restores sample files and checks their sizes).
5. `get_recovery_readiness` and `get_recovery_report` (Markdown runbook
   without passwords).

S3, B2, Azure, Google Cloud Storage, rest-server, Swift, rclone and local
repositories work the same way, with credentials passed as environment
variables (`env`).

## Restricted operations and approval

Restore, restoring over existing files, deleting snapshots, pruning,
retention changes, removing plans or repositories, stopping or restarting
app containers and trusting SFTP host keys are enforced on the server:

1. The first call changes nothing. It validates the call, returns a
   **preview** (what will happen, current vs. new values, the snapshot
   concerned) and an `approval_id`.
2. The request stores the tool, a SHA-256 hash of the exact parameters, the
   token that asked, and expires after 15 minutes.
3. A signed-in user approves or denies it on the MCP page. The page refreshes
   every 5 seconds.
4. The client repeats the call with identical parameters plus `approval_id`.
   The server re-validates the preview, then checks tool, parameter hash,
   token, expiry and single use, and only then executes.
5. Request, decision and execution are written to the audit log.

Pending requests are kept in memory; a restart discards them (fail-safe).

Not offered through MCP:

- Restoring a database dump into a running production database: database
  restores go into throwaway test containers; restoring production is a
  manual step described in the recovery report and `DISASTER-RECOVERY.md`.
- Shell commands, raw restic commands, hook commands, and restic flags or
  environment variables that can run programs (`--password-command`,
  `RESTIC_PASSWORD_COMMAND`, `sftp.command`, `--files-from`, …). Only listed
  backup flags and storage credential variables are accepted.

## Data handling

- Responses never contain repository passwords, environment variable values,
  hook URLs/tokens/commands, password hashes or SSH private keys. Tools build
  their output from explicit fields, and as a second line of defence every
  result is scrubbed of all secret values known from the configuration.
- File names, paths, host names, tags, error messages and logs come from the
  backed-up systems. Tool descriptions and server instructions tell the
  client to treat them as untrusted data.
- The repository password is needed once, to create or change a repository.
  It passes through the AI client and its provider in that case. If you do not
  want that, create the repository in the web UI and manage plans through MCP.
- Relyper Backup itself sends nothing to any AI provider; data only leaves
  when an authorized client asks for it.

## Audit log

`<data>/relyper/audit.jsonl` (JSON Lines, mode 0600, rotated at 10 MB with one
old generation). It records every write tool call, every failed or forbidden
call, approval requests and decisions, executions, token creation and
revocation, OAuth registrations, consents and failed sign-ins. Parameters are
redacted (passwords, tokens, environment values). Read-only calls that succeed
are not recorded. The MCP page shows the latest 200 entries.

## Limits

- Roles exist only for users who sign in with Relyper Identity: only the admin
  role (`backup_admin`) may approve requests and create or revoke tokens.
  Every local user may do both (local users are the emergency access).
- If authentication is disabled in the settings, OAuth sign-in is not
  possible and anyone reaching the web UI can create tokens.
- `modify_retention_policy` shows the current and the new policy but does not
  compute which snapshots will be removed.
- `get_storage_usage` reports the last `restic stats` result; the free space of
  the storage is only known from `test_sftp_connection` for SFTP.

## Testing

- Unit tests: token store (hashing, expiry, revocation, refresh rotation),
  redirect URI rules, approval binding (pending, denied, expired, reused,
  changed parameters, other token, other tool), secret scrubbing and
  redaction, input validation (environment variables, backup flags, URIs,
  SFTP option injection), schedule and retention mapping.
- End-to-end test against the built binary with a real restic repository
  (`internal/relyper/mcp/e2e_test.go`, build tag `e2e`): token checks, tool
  filtering by scope, create repository and plan, disable/enable, backup,
  status, snapshots, file listing, validation, restore with the full approval
  flow, denied delete, secrets absent from results and audit log, and the
  complete OAuth flow including wrong password, wrong PKCE verifier and
  refresh.

```bash
go build -o /tmp/backrest ./cmd/backrest
RELYPER_E2E_BIN=/tmp/backrest go test -tags e2e -run TestE2E -v ./internal/relyper/mcp/
```
