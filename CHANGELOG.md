# Changelog

Relyper Backup changes. Upstream Backrest's changelog up to the version this
fork is based on is in the upstream repository:
https://github.com/garethgeorge/backrest/blob/v1.14.1/CHANGELOG.md

## Unreleased (based on Backrest v1.14.1)

### Added

- "Databases only": databases can be put into shared plans (any number,
  named; the apps get the name as tag) next to plans of their own. (#72)
- Set up backups: "Databases only" creates one dump-only plan
  (`<plan>-db`) per app with a database server, selectable one by one; SQLite
  files are listed and stay with the app plans. The plan of Easypanel now
  counts in the coverage: Easypanel's settings, the deployment files and the
  app definition of every Easypanel app are protected by it (Easypanel's own
  backups and logs are not required). (#71)
- Easypanel-only plan: code only of services whose code was uploaded (not
  from Git, an image or a template); the preview lists the source of every
  service. (#69)
- Pin snapshots: a pinned snapshot (restic tag `relyper-pinned`) is kept by
  every retention run and cannot be forgotten until it is unpinned; pin on
  the plan page, list on Infrastructure → Storage, MCP `pin_snapshot`. New
  app plans keep 12 monthly snapshots. (#67)
- Easypanel-only plan: Easypanel's own backups and action logs and the
  data folders of services are no longer in it; clearer labels. (#66)
- Easypanel-only plan: from the service folders only the files needed to
  deploy again; code from Git, `.git`, dependencies and build output are
  left out. (#65)
- Set up backups: "Easypanel configuration only (no app data)" creates one
  small plan with Easypanel's settings and the code of every service,
  without app volumes and databases; the preview shows the size of every
  path. Easypanel's shared settings are no longer copied into every app
  plan, and read-only binds holding other apps' data no longer count as
  data of the app that mounts them. (#64)
- Infrastructure tab "Storage": size of every repository on its storage
  and, per plan, kept snapshots, size of the latest backup and what the
  plan stores; "Measure now" runs restic stats. (#63)
- Infrastructure tab "Disaster recovery": the recovery report rendered in
  the UI, starting with "If the whole server is lost" (steps in order with
  this server's plans and apps), download and print / PDF. (#62)
- Dashboard "Needs attention" shows counts instead of every app; counts
  and tiles open the Infrastructure page with that status filter. (#61)
- Every app card lists its configuration as backup items: the app
  definition in the recovery report, for Easypanel services the service
  folder (`code/`) and Easypanel's settings (environment variables, domains,
  ports, Traefik, certificates). Easypanel itself is listed as its own app.
  SQLite databases inside volumes are found and shown. An app without a
  plan is never "fully protected"; the Docker guard no longer shows up as a
  stopped service. Disaster recovery for a lost Easypanel server. (#60)
- App cards show which databases an app has and which database in another
  app it connects to (from `DATABASE_URL`, `*_HOST` and SQLite variables;
  the Docker guard passes on only engine, host and database name). Old
  Swarm task containers from earlier deployments are no longer listed as
  services or backup targets (collapsed history instead); each volume is
  listed once per service. (#59)
- Every Easypanel service is its own app; the Easypanel project is shown as
  a label. Tags (Relyper Backup only, stored next to `config.json`) group
  apps across Easypanel projects: filter and search by tag, one backup plan
  per tag group with the dumps of all its databases, MCP tool
  `set_app_tags`. Plans for a whole Easypanel project keep working. (#56)
- Database servers with several databases are also dumped per database;
  `restore-databases.sh --database=<service>/<name>` restores one database
  of a shared server without touching the others. (#53)
- App plans back up the whole compose directory (with `.env`) and, for
  Easypanel apps, Easypanel's settings (domains, Traefik, certificates);
  the plan preview lets you choose every volume and bind mount; app
  definitions as JSON in the recovery report. (#52)
- Infrastructure page: summary badges filter the app list; stopped Swarm
  services are no longer reported as running on another node. (#50, #51)
- Health check every 5 s while the container starts, so a zero-downtime
  handover with an idle old container takes seconds. (#45)
- Guided app restore on the Infrastructure page (stage, then a runbook in
  restore order), `RESTORE.md` and `restore-databases.sh` in every app
  snapshot, network dependencies and restore order in the recovery report,
  bootstrap script `relyper-recover`, MCP tool `get_app_recovery_guide`.
  (#43)
- User menu at the top right (operator app and customer portal): who is
  signed in, sign-in method, role, tenant, what the role allows, sign out.
  (#42)
- While Relyper Identity is enabled, the local name/password sign-in (web
  login and MCP consent page) is hidden behind "Emergency access with a
  local user". (#39)
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

### Security

- Relyper Docker guard (`Dockerfile.docker-guard`): the only container with
  the Docker socket. It allows reading the Docker host, the fixed database
  dump scripts and isolated restore-test containers and refuses everything
  else; replaces the generic socket proxy, which allowed any command in any
  container and privileged containers. Container hardening in the compose
  files (read-only, dropped capabilities, no-new-privileges),
  `RELYPER_HOOK_COMMANDS=dbdump`, restic 0.20.0, Alpine 3.24, Go and npm
  dependency updates, docker CLI removed from the image. (#38)

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
