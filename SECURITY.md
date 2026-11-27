# Security

Relyper Backup reads the data it protects, holds the keys to decrypt every
backup and can delete backups. Treat the instance like a root account.

## Reporting a vulnerability

Please do not open a public issue. Contact the maintainer privately via
GitHub (`jonasesser`) with a description and, if possible, a reproduction.
Vulnerabilities in upstream Backrest code should also be reported to
https://github.com/garethgeorge/backrest/security.

## What is in place today

| Area | State |
|---|---|
| Authentication | Users with bcrypt password hashes; HS256 JWT valid for 7 days, signed with a random 64-byte secret in `<data>/jwt-secret`. Basic auth accepted on API calls. (upstream) |
| Authorization | **None beyond "signed in".** Every user is a full administrator. (upstream) |
| First start | Authentication is **disabled until the first user is created** in the setup dialog. Until then anyone who reaches the port has full control. Create the user immediately, before exposing the port. (upstream) |
| Secrets in the UI | `GetConfig` masks user password hashes and the multihost private key. Repository passwords and environment variables **are** sent to the signed-in browser so they can be edited. (upstream) |
| MCP | Separate credentials (scoped personal access tokens or OAuth 2.1 with PKCE), stored as SHA-256 hashes; they only work on `/mcp`. Responses never contain secrets. Restricted operations need a human approval in the web UI bound to the exact parameters. No shell, hook or raw restic access. See `MCP.md`. (Relyper, #17) |
| Secrets at rest | Repository passwords and environment variables (e.g. S3 keys) are stored **in plaintext** in `config.json`; SSH private keys in `/config/.backrest-ssh` (mode 0600). Protect `/config` with file permissions and disk encryption. (upstream) |
| SSH host keys | Never trusted implicitly. Keys are added to `known_hosts` only after the user confirmed the SHA-256 fingerprint, a recorded key is never replaced automatically, revoked keys are honoured, restic runs ssh with `BatchMode=yes` and a fixed `UserKnownHostsFile`. (Relyper, #12/#13; upstream added keys with `ssh-keyscan` without any check) |
| SFTP test password | Used for one test connection only, never stored or logged. (Relyper) |
| Input validation | SFTP host names are checked against option injection (`-oProxyCommand=…`) and shell metacharacters; no Relyper code runs a shell. (Relyper) |
| Health endpoint | `/healthz` is unauthenticated and returns only `{"status":"ok"}`. (Relyper) |
| Supply chain | restic download verified by SHA-256 and signature (upstream). The i18n plugin that upstream loaded unpinned from a CDN at build time is vendored and checksummed. (Relyper, #3) |
| Audit | MCP tool calls, approvals, token and OAuth events go to `<data>/relyper/audit.jsonl` with redacted parameters. Trusting a host key in the UI is logged with user, host and fingerprint. Backup operations are recorded in the operation history. Web UI configuration changes are not audited yet. |
| TLS | Not terminated by the server. Run it behind a TLS reverse proxy (Easypanel/Traefik, Caddy, nginx). |

## Threat model

### Assets

1. Backed-up data (files, databases) – confidentiality and availability.
2. Repository passwords and storage credentials – whoever has both can read
   and delete all backups.
3. The production data on the host – reachable through source mounts and
   restores.
4. The Docker host – reachable if the Docker socket is mounted.

### Attackers and what they can do

| Attacker | Impact today | Mitigation |
|---|---|---|
| Anyone on the network before the first user exists | Full control | Create the user at once; do not publish the port before. |
| Stolen or guessed password, stolen JWT | Full control: read all backups (snapshot browser, downloads), run restores, delete snapshots, change plans, **run hook commands (shell inside the container)** and arbitrary restic commands | Strong unique password, TLS, keep the UI off the public internet or behind an additional auth layer (VPN, SSO proxy). Roles are planned. |
| Compromised Relyper Backup container | Reads mounted sources; with the repository credentials it can **delete every remote backup it can reach** (restic `forget`/`prune`, or deleting files over SFTP) | See "Residual risks". |
| Man-in-the-middle on the SFTP connection | Prevented by host key verification; a changed key stops all connections until a human decides. | Compare fingerprints with the provider. |
| Malicious content in backed-up files, logs, metadata | Shown in the UI as text; passed to AI clients via MCP. | Treat as untrusted input; never execute it; MCP tool descriptions and server instructions mark it as untrusted data. |
| Malicious restore paths | restic restores into the chosen target; a restore over production paths can overwrite data. | Restore into a staging directory (`/restore`); mount production read-only. |

### Residual risks

- **A compromised backup server can destroy its own backups.** The instance
  holds the repository password and the storage credentials, so it can also
  delete or overwrite repository files. Mitigations, depending on the
  storage:
  - Use storage with object lock / immutability (S3 Object Lock, B2 file
    lock) or a `rest-server` in `--append-only` mode, and run `forget`/`prune`
    from a separate, more trusted machine.
  - For SFTP accounts like Host Europe that offer no append-only mode, keep at
    least one additional copy that this instance cannot delete (e.g. a second
    repository pulled by another machine, or provider-side snapshots).
  - Relyper Backup does not implement append-only enforcement itself; this is
    on the roadmap as an evaluation item.
- **Plaintext secrets in `config.json`.** Anyone who can read `/config` can
  read all backups. Encrypted secret storage is planned.
- **No roles in the web UI.** Viewing status and deleting backups need the same login, and every web UI user can create MCP tokens and approve MCP requests. MCP credentials themselves are scoped.
- **MCP exposure.** Connecting claude.ai or ChatGPT requires the instance to be reachable from the internet. The OAuth consent page accepts the web UI password; use a strong password. A repository password given to `create_repository` passes through the AI provider.
- **Docker access.** Mounting the socket gives root on the host, even with
  `:ro`. Application-aware backup needs more than read access: it runs the
  dump tools inside database containers (`exec`) and creates, starts and
  removes throwaway restore-test containers. `docker-compose.apps.yml` puts a
  filtering socket proxy in between that blocks images, networks, secrets,
  configs, plugins, build and system endpoints, but whoever controls Relyper
  Backup can still run commands in your containers. Treat admin access to
  Relyper Backup like root on the Docker host. Relyper Backup itself only
  runs fixed dump/restore commands, never commands from the UI or MCP
  clients; app IDs are validated, and it only removes containers labelled
  `relyper.verify=true`.
- **Database credentials** stay inside the database containers: the dump
  runs there and reads the password from the container's own environment or
  `*_FILE` secret. Relyper Backup does not store or log them.
- **Hook commands** run arbitrary shell commands inside the container by
  design. Anyone who can edit plans can run commands.

## Planned security work

Tracked in `ROADMAP.md`: role-based permissions (view, manage plans, manage
repositories, run backups, browse contents, restore, delete, manage
integrations), MCP authorization with server-side approval of restricted
operations bound to exact parameters and expiry, a general audit log, secret
redaction in logs, encrypted secret storage, evaluation of append-only / immutable repositories.

OIDC sign-in with Relyper Identity is available (#18); see `IDENTITY.md` for
the checks it makes and its limits.

## Deployment hardening checklist

- [ ] First user created before the port is reachable from other machines.
- [ ] UI only via HTTPS; ideally only via VPN or behind SSO.
- [ ] Source mounts read-only; restores go to `/restore`.
- [ ] Docker socket not mounted directly; only through the filtering proxy
      of `docker-compose.apps.yml`, on an internal network.
- [ ] `/config` backed up offline; repository passwords stored outside the
      host (password manager). See `DISASTER-RECOVERY.md`.
- [ ] At least one backup copy the instance cannot delete.
- [ ] SFTP host key fingerprint compared with the provider's before trusting.
