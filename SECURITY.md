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

| Customers (backup as a service) | Optional customer portal (`RELYPER_PORTAL=1`). Portal sessions have their own key and issuer and are refused by every operator route; operator sessions are refused by the portal. The tenant comes only from the signed session and is checked against `tenants.json` on every request; every plan, repository and snapshot ID is checked against the tenant; foreign IDs answer 404. Customer input never becomes flags, hooks or variable names; `$` is refused so the server's environment cannot leak to a customer endpoint; storage endpoints must be https and not internal addresses. Destructive operations stay with the operator. Recovery kits are separate restic keys shown once; escrow copies are sealed to an offline public key. Threat model: `docs/CONCEPT-MULTITENANT.md`. (Relyper, #29, #31, #33) |

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
| Stolen or guessed password, stolen JWT | Full control: read all backups (snapshot browser, downloads), run restores, delete snapshots, change plans and arbitrary restic commands. Hook commands (shell inside the container) only with `RELYPER_HOOK_COMMANDS=all`; the compose files set `dbdump`. | Strong unique password, TLS, keep the UI off the public internet or behind an additional auth layer (VPN, SSO proxy). Give Relyper Identity users who only need to look the read-only role `backup_user`. |
| Compromised Relyper Backup container (e.g. through a vulnerability) | Reads mounted sources; with the repository credentials it can **delete every remote backup it can reach** (restic `forget`/`prune`, or deleting files over SFTP). It does **not** get root on the host: there is no Docker socket in the container, the Relyper Docker guard refuses everything except the fixed dump and restore-test calls, and the container runs read-only with five capabilities. | See "Residual risks" and "Docker access". |
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
- **Two roles only.** Users signed in through Relyper Identity with `backup_user` are read-only (server-side, deny by default, secrets redacted); `backup_admin` and local users have full rights. Finer roles (run backups but not delete, …) do not exist yet. MCP credentials themselves are scoped.
- **MCP exposure.** Connecting claude.ai or ChatGPT requires the instance to be reachable from the internet. The OAuth consent page accepts the web UI password; use a strong password. A repository password given to `create_repository` passes through the AI provider.
- **Docker access.** Mounting the socket gives root on the host, even with
  `:ro`, and so does a generic socket proxy that allows `exec` and
  container creation (the setup before #38). Relyper Backup therefore never
  gets the socket. It talks to the **Relyper Docker guard**
  (`cmd/relyper-docker-guard`), a separate 10 MB container with one static
  binary that allows exactly the calls Relyper Backup makes: reading the
  Docker host (environment values removed), the fixed dump scripts in
  running database containers, and restore-test containers without network,
  mounts or privileges from images that already run as databases. Request
  bodies are checked field by field and rebuilt by the guard; the commands
  are compared with the guard's own compiled-in copy of the scripts, so a
  compromised Relyper Backup cannot change them. Each write feature is off
  unless enabled; stop/restart of containers is off by default. Residual:
  someone who controls Relyper Backup can read database contents through
  the dump scripts (the backups contain them anyway), start isolated
  restore-test containers and, if enabled, stop or restart containers. The
  guard is now the component holding root-equivalent access: keep it on an
  internal network, set `RELYPER_GUARD_TOKEN` and never publish its port.
  Details in `DEPLOYMENT.md`, "Relyper Docker guard".
- **Database credentials** stay inside the database containers: the dump
  runs there and reads the password from the container's own environment or
  `*_FILE` secret. Relyper Backup does not store or log them.
- **Hook commands** are an upstream feature that runs any shell command
  inside the container. With `RELYPER_HOOK_COMMANDS=dbdump` (set in the
  compose files) only the `relyper-dbdump` command of app plans runs; every
  other command hook fails before it starts. Webhook-style hooks (Discord,
  Slack, Gotify, Healthchecks, shoutrrr) are not affected. Even with `all`,
  commands run in the hardened container, not on the host.
- **Container hardening.** The compose files run Relyper Backup with a
  read-only root filesystem, `cap_drop: ALL` plus `CHOWN`, `DAC_OVERRIDE`,
  `DAC_READ_SEARCH`, `FOWNER`, `FSETID` (reading every source file and
  restoring owners needs them) and `no-new-privileges`. The process is root
  inside the container; non-root cannot read other users' volume data.
  Easypanel cannot set these options for App services.

## Patch level

Checked on 2026-11-28 (#38):

| Component | State |
|---|---|
| Backrest (upstream) | v1.14.1 is the latest release; nothing to merge. |
| restic | 0.20.0 (from 0.19.1): fixes two restore security issues (wrong owners with `--ownership-by-name`, duplicate or unsorted node names). |
| Go toolchain | Image builds with the newest `golang:1.26` patch (1.26.11), which fixes all standard-library findings of `govulncheck`. Local builds with an older 1.26.x show them. |
| Go modules | `golang.org/x/net`, `x/crypto`, `x/text`, `grpc`, `go-jose`, `gorilla/websocket` updated. The restic signature check moved from the deprecated `x/crypto/openpgp` to `github.com/ProtonMail/go-crypto` (GO-2026-5932). `govulncheck`: no finding left in Relyper Backup's own modules with Go 1.26.11. |
| Web UI (npm) | `pnpm audit`: 29 findings before, 0 after (lockfile updates within the existing version ranges; overrides for `sha.js` and `rollup`). All were build or test tools except `react-router` (RSC mode, not used). |
| Runtime image | Alpine 3.24 with `apk upgrade`; Trivy: 0 OS findings. The docker CLI was removed from the image. |
| restic binary | restic's own release binary still contains `x/crypto/openpgp` (Trivy GO-2026-5932, severity unknown); restic does not use it for anything Relyper Backup calls. Fixed only by a later restic release. |

How to re-check: `govulncheck ./...`, `pnpm audit` in `webui/`, and an
image scanner (e.g. `trivy image relyper-backup`) on the built images.

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
- [ ] Docker socket mounted only into the Relyper Docker guard, never into
      Relyper Backup; guard on an internal network with `RELYPER_GUARD_TOKEN`
      set; Infrastructure page shows "Docker guard".
- [ ] `RELYPER_HOOK_COMMANDS=dbdump` unless you need own shell hooks.
- [ ] Container hardening of the compose files kept (`read_only`,
      `cap_drop`, `no-new-privileges`).
- [ ] Image rebuilt regularly for Go, Alpine and restic fixes.
- [ ] `/config` backed up offline; repository passwords stored outside the
      host (password manager). See `DISASTER-RECOVERY.md`.
- [ ] At least one backup copy the instance cannot delete.
- [ ] SFTP host key fingerprint compared with the provider's before trusting.
