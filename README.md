<p align="center">
  <img src="./webui/assets/relyper-logo.svg" width="360px" alt="Relyper Backup">
</p>

# Relyper Backup

**Intelligent backup management. Your infrastructure, your data, your
control.**

Relyper Backup is a self-hosted backup platform built on
[restic](https://restic.net). It is a downstream fork of
[Backrest](https://github.com/garethgeorge/backrest) by Gareth George and
keeps everything Backrest does: a web UI for repositories, plans, schedules,
retention, snapshot browsing and restores, hooks and notifications.

Every repository is a standard restic repository. You can always restore with
the plain restic CLI, without Relyper Backup.

## What Relyper Backup adds today

- **One Docker image** with restic, OpenSSH, rclone and a health check,
  started by tini so backups shut down cleanly (`DEPLOYMENT.md`).
- **SFTP done carefully:** the server's host key is shown with its SHA-256
  fingerprint and only trusted after you confirm it; a connection test checks
  DNS, TCP, host key, login, SFTP, the directory, write access and the restic
  repository step by step and tells you what failed.
- **Reproducible builds:** no build-time downloads from CDNs.
- Relyper branding with attribution to Backrest and restic.
- **Native MCP server** at `/mcp`: AI clients (Claude, ChatGPT, Claude Code …)
  read status and set up repositories, SFTP, plans and schedules. Scoped
  tokens or OAuth sign-in; restore, deletion, prune and retention changes
  need your approval in the web UI; audit log (`MCP.md`).

Planned (not built yet): roles in the web UI, Docker/Easypanel discovery,
database dumps, backup coverage and guided disaster recovery. See
`ROADMAP.md`.

## Quick start

```bash
git clone https://github.com/jonasesser/relyper-backup.git
cd relyper-backup
docker compose up -d --build
```

Open `http://<host>:9898` and **create the first user right away** – until
then the instance has no authentication (see `SECURITY.md`). Put it behind a
TLS reverse proxy before exposing it.

## Documentation

| File | Content |
|---|---|
| `DEPLOYMENT.md` | Image, volumes, mounts, Easypanel, upgrades |
| `SECURITY.md` | What is in place, threat model, hardening checklist |
| `DISASTER-RECOVERY.md` | Restore with the UI or the plain restic CLI |
| `ARCHITECTURE.md` | Components and where new features attach |
| `ROADMAP.md` | Phases, status, open decisions |
| `MCP.md` | MCP server: connecting clients, permissions, tools, approvals |
| `UPSTREAM.md` | How the fork tracks Backrest |
| `AGENTS.md` | Rules for contributors and coding agents |
| `CHANGELOG.md` | Relyper changes |

## License and attribution

GPL-3.0, like Backrest (`LICENSE`, `NOTICE`). Backrest © Gareth George and
contributors. restic is BSD-2-Clause. Relyper Backup is not affiliated with
or endorsed by the Backrest or restic projects.
