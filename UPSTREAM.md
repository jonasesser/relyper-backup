# Upstream

Relyper Backup is a downstream fork of [Backrest](https://github.com/garethgeorge/backrest).
The fork keeps Backrest's complete Git history so upstream releases can be
merged instead of re-applied by hand.

## Baseline

| | |
|---|---|
| Project | Backrest – https://github.com/garethgeorge/backrest |
| Version | **v1.14.1** (latest stable release at import time) |
| Commit | `875c9cb51e626a75276a64da42c6a9ec0695010a` |
| Imported | 2026-11-27 |
| License | GPL-3.0 (`LICENSE`, unchanged) |
| restic | 0.19.1 (pinned by `internal/resticinstaller`) |

## Remotes

```bash
git remote add upstream https://github.com/garethgeorge/backrest.git
git fetch upstream --tags
```

`origin` is `jonasesser/relyper-backup`. Work happens on `main` (see
`AGENTS.md`).

## Where Relyper code lives

Rule: **add, don't edit.** New functionality goes into Relyper-owned paths.
Upstream files are only touched where a hook point is unavoidable, and every
such line carries a `relyper:` comment so it is easy to find during merges
(`git grep -n "relyper:"`).

| Relyper-owned path | Content |
|---|---|
| `internal/relyper/...` | Go packages (health endpoint, SFTP diagnostics, future MCP, discovery) |
| `cmd/relyper-healthcheck/` | container health probe |
| `proto/relyper/v1/` | Relyper API definitions (`relyper.v1.*` services) |
| `gen/go/relyper/`, `webui/gen/ts/relyper/` | generated code for the above |
| `webui/src/relyper/` | React components and API clients |
| `webui/relyper/` | build-time scripts (branding of message catalogs) |
| `webui/vendor/inlang/` | vendored i18n plugin |
| `webui/assets/relyper-*` | logo, icon, favicon |
| `Dockerfile`, `docker-compose.yml`, `.dockerignore` | source build of the single image |
| `*.md` at the root (except `LICENSE`) | Relyper documentation |

### Upstream files changed (hook points)

Keep this list current; it is the merge checklist.

| File | Change | Issue |
|---|---|---|
| `cmd/backrest/backrest.go` | registers `/healthz` and the `relyper.v1.RelyperSftp` service (2 imports, 2 lines) | #11, #12 |
| `internal/api/backresthandler.go` | `SetupSftp` no longer adds host keys with `ssh-keyscan` (trust on first use) | #12 |
| `webui/src/api/client.ts` | `transport` exported for the Relyper clients | #12 |
| `webui/src/test/setup.tsx` | global mock for `@/relyper/api` | #12 |
| `webui/src/features/repositories/AddRepoModal.tsx` | renders `SftpDiagnostics`; texts no longer promise automatic host key scanning; product name | #12, #14 |
| `webui/src/features/settings/SettingsModal.tsx` | product name in one hint | #14 |
| `webui/src/app/App.tsx` | Relyper logo, version opens the About dialog | #14 |
| `webui/index.html` | title and favicon | #14 |
| `webui/package.json` | `start`, `build`, `build-windows`, `compile-i18n` run `relyper/brand-messages.mjs` first | #14 |
| `webui/project.inlang/settings.json` | vendored message-format plugin; messages read from `.relyper-messages/` | #3, #14 |
| `webui/.gitignore`, `.dockerignore` | generated message catalogs; Docker build context | #11, #14 |
| `README.md`, `CHANGELOG.md` | replaced by Relyper versions; upstream versions are in Git history and upstream | docs |

Not changed on purpose: the Go module path `github.com/garethgeorge/backrest`,
the `BACKREST_*` environment variables, config file format, API package
`v1`, and the `messages/*.json` translation files. Changing any of these would
make every upstream merge conflict and break compatibility with existing
Backrest installations and documentation.

## Updating to a new upstream release

1. Read the upstream release notes and `CHANGELOG.md` of the new tag.
2. Fetch and merge the tag on a temporary branch:
   ```bash
   git fetch upstream --tags
   git switch -c upstream-merge main
   git merge --no-ff v1.X.Y
   ```
3. Resolve conflicts:
   - `README.md`, `CHANGELOG.md`: keep ours.
   - Hook point files from the table above: take upstream's version, then
     re-apply the `relyper:` lines.
   - `webui/pnpm-lock.yaml`: take upstream's, then `pnpm install` (pnpm 9)
     and commit the result.
   - Generated code (`gen/`, `webui/gen/`): take upstream's, then regenerate
     everything with `cd proto && ./update.sh` using the plugin versions
     named in the generated file headers.
4. Run the full checks (see "Regression checks" below).
5. Build and smoke-test the Docker image (`DEPLOYMENT.md`).
6. Fast-forward `main`, update the baseline table above and `CHANGELOG.md`,
   push.

AI-assisted merges follow the same steps. They must not skip tests, code
review or the restore check.

## Regression checks

Run before and after every merge:

```bash
go build ./...
go test ./...                          # upstream + Relyper Go tests
(cd webui && pnpm install --frozen-lockfile && pnpm test && pnpm run check)
docker build -t relyper-backup:test .  # see DEPLOYMENT.md
```

The tests that protect Relyper behaviour:

- `internal/relyper/sshdiag`: SFTP host key trust (no blind trust, no
  replacement of recorded keys), connection diagnostics against an in-process
  SSH/SFTP server, password never in output.
- `internal/relyper/health`, `cmd/relyper-healthcheck`: health endpoint and
  probe.
- `webui/src/relyper/**`: SFTP UI, URI parsing, branding of message catalogs,
  upstream attribution in the About dialog.

Upstream's tests (`pkg/restic`, `internal/orchestrator`, `test/e2e`, web UI)
cover backup, retention (forget), prune, check and restore with disposable
local repositories.

Known environment-dependent failures (not regressions): when run as root,
three `pkg/restic` tests that expect an unreadable file fail because root can
read it; `webui.TestServeIndexGzip` fails unless `webui/dist/index.html.gz`
exists (upstream CI creates a fake one).

## Dependencies

- restic: bumped upstream in `internal/resticinstaller`; arrives with merges.
  Do not bump it independently without running the restic tests.
- Go and npm dependencies: follow upstream where possible. Security updates
  in between get their own issue and a full test run.
- The vendored inlang plugin is updated as described in
  `webui/vendor/inlang/README.md`.

## Upstream CI and release workflows

`.github/` is still upstream's. Release Please, the release workflows, the
docs deployment and the restic update workflow expect upstream's secrets and
infrastructure and fail or do nothing useful in this repository; Dependabot
opens upstream-style update PRs. Whether to disable them is an open decision
(see `ROADMAP.md`).

## Configuration and data migrations

Relyper Backup does not change the config format yet. Upstream migrations in
`internal/config/migrations` run automatically on start. When Relyper needs
its own configuration, it will live in a separate file next to `config.json`
so upstream migrations never see it.
