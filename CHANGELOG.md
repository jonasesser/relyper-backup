# Changelog

Relyper Backup changes. Upstream Backrest's changelog up to the version this
fork is based on is in the upstream repository:
https://github.com/garethgeorge/backrest/blob/v1.14.1/CHANGELOG.md

## Unreleased (based on Backrest v1.14.1)

### Added

- Single Docker image built from source: web UI, server, verified restic,
  OpenSSH client, rclone; tini as init so stop signals reach backups;
  `/healthz` endpoint and container HEALTHCHECK; `docker-compose.yml`. (#11)
- SFTP: host keys are scanned per key type and shown with their SHA-256
  fingerprint; a key is only trusted after confirmation. Step-by-step
  connection test (DNS, TCP, host key, authentication, SFTP, directory, write
  test, restic repository, free space) with stable error categories. (#12)
- Branding: name, logo, favicon, About dialog with attribution to Backrest and
  restic; product name applied to the message catalogs at build time. (#14)
- Documentation: README, AGENTS, ARCHITECTURE, ROADMAP, SECURITY, DEPLOYMENT,
  DISASTER-RECOVERY, MCP (design only), UPSTREAM, NOTICE.
- Native MCP server at `/mcp` (Streamable HTTP): status, repository, SFTP,
  plan and schedule tools; scoped personal access tokens and OAuth 2.1 for
  remote connectors; server-side approval for restore, deletion, prune,
  retention changes and host key trust; audit log; MCP page in the web UI.
  (#17)
- Sign-in with Relyper Identity (OIDC) for the web UI and the MCP consent
  page, with roles from the provider (`backup_admin` approves MCP requests
  and manages tokens); local users stay as emergency access. See
  `IDENTITY.md`. (#18)
- Docker discovery (Compose, Swarm, Easypanel, single containers) and
  database dumps for PostgreSQL, MySQL, MariaDB, MongoDB and Redis with
  `relyper-dbdump`, verified by restoring into a throwaway container. (#20)
- Backup coverage based on completed snapshots, app backup plans with a dump
  hook, automatic database and file restore tests, recovery readiness,
  Markdown recovery report, notifications (`RELYPER_NOTIFY_URLS`) and the
  matching MCP tools. (#21)
- Web UI page "Infrastructure"; `docker-compose.apps.yml` with a filtering
  Docker socket proxy. (#22)

### Changed

- The i18n plugin is vendored and checksummed instead of loaded from a CDN at
  build time. (#3)
- Upstream's release-please workflow removed; it requires Conventional
  Commits and failed on every push. (#16)

### Security

- SFTP setup no longer trusts host keys implicitly (upstream ran
  `ssh-keyscan` and wrote whatever it got into `known_hosts`). (#12)

### Fixed

- The SFTP test no longer reports a host key mismatch when the server offers
  several key types and a different one was trusted. (#13)
