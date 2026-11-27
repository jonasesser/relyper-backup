# Relyper Backup – rules for agents

Relyper Backup is a downstream fork of Backrest (Go backend, React/Chakra web
UI, restic as the backup engine). Read `UPSTREAM.md` before changing upstream
files and `ARCHITECTURE.md` before adding features. Several agent sessions may
work on this repository at the same time.

## Issues and commits

- **Create the GitHub issue first, then commit.** Every change gets an issue
  in `jonasesser/relyper-backup`, and the commit uses the number GitHub
  returned. Never guess or reserve an issue number.
- Commit message: `#<issue> <short German title>`, e.g.
  `#12 SFTP: Host-Key bewusst bestätigen`. No Claude/AI hints, no
  `Co-Authored-By` or session lines. Details (what, why, how it was tested,
  open points) go into the issue, not the commit.
- Issue texts are German, short, in plain words.
- Commit straight to `main` unless asked otherwise.
- Before committing: `git fetch origin` and rebase onto `origin/main`.

## Upstream

- Add, don't edit: new code goes into `internal/relyper/`, `proto/relyper/`,
  `webui/src/relyper/` and the other Relyper paths listed in `UPSTREAM.md`.
- When an upstream file must change, keep the change minimal, mark it with a
  `relyper:` comment and add it to the hook point table in `UPSTREAM.md`.
- Do not rename the Go module, the `BACKREST_*` variables, the config format
  or edit `webui/messages/*.json` (branding is applied at build time by
  `webui/relyper/brand-messages.mjs`).
- `webui/GEMINI.md` describes upstream's web UI structure and code style;
  that guidance still applies to code under `webui/`. Its process rules are
  upstream's; the rules in this file apply to this repository.

## Before committing

- Go: `go build ./...`, `go vet` on changed packages, `go test` for changed
  and affected packages (`go test ./...` before merges and releases).
- Web UI (from `webui/`, pnpm 9): `pnpm test`, `pnpm run check`,
  `npx prettier --write` on changed files.
- Proto changes: regenerate with `cd proto && buf generate` using the plugin
  versions in the headers of the generated files; commit generated code.
- Docker-related changes: build the image and check `/healthz`.
- UI changes: look at the screen at desktop width and at 390 px.
- Report honestly in the issue what was tested and what was not.

## Product rules

- restic stays the engine; repositories stay standard restic repositories
  that can be restored with the plain restic CLI.
- Backups and restores must work without any AI feature.
- No placeholder features, fake status values or mocked production paths.
  If something cannot be done reliably, document the limitation.
- Security: never return or log secrets (repository passwords, keys, tokens).
  Never trust SSH host keys implicitly. Destructive operations need explicit,
  server-side authorization. See `SECURITY.md`.

## Code

- Source code, comments, docs and UI source strings in English.
- Mark own code in upstream files with a `relyper:` comment; Relyper-owned
  files carry a package or file comment saying so.
