# Sign-in with Relyper Identity (OIDC)

Relyper Backup can let users sign in through Relyper Identity, the Relyper
OpenID Connect provider. The sign-in works for the web UI and for the consent
page that appears when an AI app (Claude, ChatGPT) connects to the MCP
server. Local users with a password keep working next to it as **emergency
access**. That way backups and restores never depend on the identity provider
being reachable.

The implementation (`internal/relyper/identity`) is the Go counterpart of the
OIDC part of `@relyper/sp-auth` (TypeScript, `jonasesser/relyper-sp-auth`).
It makes the same checks and reads the claims the same way. The TypeScript
package cannot be used directly because the server is written in Go.

## Setup

1. In the Relyper IdP admin UI under **OIDC Settings**, register Relyper
   Backup as a confidential client:
   - generate a client ID and a client secret;
   - set the redirect URI to `https://<your-domain>/auth/oidc/callback`;
   - list the application roles `backup_user` and `backup_admin` (or your
     own names, see below).
2. Keep at least one local user in Relyper Backup (Settings → users). It is
   the emergency access when the provider is down.
3. Set the environment variables and restart:

| Variable | Default | Meaning |
|---|---|---|
| `RELYPER_OIDC_ISSUER` | unset (feature off) | Issuer URL, e.g. `https://api.relyper.de`. Must be https. |
| `RELYPER_OIDC_CLIENT_ID` | – | Client ID from the registration. |
| `RELYPER_OIDC_CLIENT_SECRET` / `RELYPER_OIDC_CLIENT_SECRET_FILE` | – | Client secret, directly or from a file (Docker secrets). |
| `RELYPER_OIDC_REDIRECT_URI` | `<RELYPER_PUBLIC_URL>/auth/oidc/callback` | Must match the registration byte for byte and end in `/auth/oidc/callback`. |
| `RELYPER_OIDC_LOGIN_ROLE` | `backup_user` | Role needed to sign in. |
| `RELYPER_OIDC_ADMIN_ROLE` | `backup_admin` | Role needed to approve MCP requests and to create or revoke MCP tokens. It also allows signing in. |
| `RELYPER_OIDC_SCOPES` | `openid email profile roles` | Requested scopes. |
| `RELYPER_OIDC_DISPLAY_NAME` | `Relyper` | Text on the sign-in buttons. |
| `RELYPER_OIDC_SESSION_HOURS` | `8` | Lifetime of a web UI session (1–168). |

Set `RELYPER_PUBLIC_URL` as well when the server runs behind a reverse proxy.
If the configuration is invalid, the sign-in is switched off with an error in
the log; everything else keeps running.

**Settings → Authentication → Relyper Identity (OIDC)** shows the current
state (not configured, configuration error, not offered, active), the
configuration error if any, the callback URL to register and the variable
names. The client secret is never shown; only whether one is set.

Web UI authentication must be enabled. With authentication disabled in the
settings, nobody needs to sign in and the button is not shown.

## What happens

Web UI:

1. The login dialog shows **Sign in with Relyper** above the user name and
   password fields.
2. `/auth/oidc/login` creates a fresh `state`, `nonce` and PKCE verifier,
   binds them to the browser with an HttpOnly cookie (10 minutes, single use)
   and redirects to the provider.
3. `/auth/oidc/callback` checks that `state` belongs to this browser, then
   exchanges the code with the client secret (`client_secret_basic`, or
   `client_secret_post` if that is all the provider offers) and the PKCE
   verifier.
4. The ID token is verified against the provider's JWKS. Only RS256 is
   accepted, so `alg: none` and HMAC tricks are refused. `iss`, `aud`,
   `exp`, `nonce`, `azp` and `sub` are checked, and an e-mail address is
   required.
5. The `roles` claim must contain the login role or the admin role. Roles may
   be an array or a space- or comma-separated string.
6. The server signs a web UI session token (HS256, its own key in
   `<data>/relyper/oidc-session.key`). The browser picks the token up once
   through `POST /auth/oidc/session`, with an HttpOnly cookie that is valid
   for 2 minutes and a custom header. The token is never put in a URL.
7. Every request checks the session again, including the role, against the
   current settings.

The user appears as `relyper:<e-mail>` in the audit log, so the name is never
confused with a local user.

MCP consent page: next to the local user fields there is **Sign in with
Relyper and allow**. The chosen permissions are fixed before the redirect;
the provider only confirms who the user is, and the login role is required.
If no local user exists, the page offers only the provider.

## Roles

| Who | Web UI | Approve MCP requests, create/revoke MCP tokens |
|---|---|---|
| Provider user with `backup_user` | read-only | no |
| Provider user with `backup_admin` | full | yes |
| Local user (emergency access) | full | yes |
| Provider user without either role | refused | – |

Read-only means: plans, repositories, snapshots, operations, logs, the
dashboard and the Infrastructure page are visible. Changing the
configuration, starting backups, restores, forget/prune/check, downloads,
`RunCommand`, SFTP host key trust and every Infrastructure action are
refused on the server with "read-only access". The configuration a
read-only user receives has its secrets redacted (repository passwords,
environment values, passwords in URIs and flags, password hashes, hook
contents, the multihost key). The header shows a "Read-only" badge. Routes
that are not known to be read-only are refused (deny by default).

## Limits

- There are two roles (view and full). Finer permissions (manage plans,
  run backups, restore, delete) are not split yet.
- Local users always have full rights.
- Signing out removes the session from the browser. A copied session token
  keeps working until it expires (`RELYPER_OIDC_SESSION_HOURS`). Removing a
  role at the provider takes effect at the next sign-in. Raising the login
  role setting takes effect immediately.
- There is no single sign-out at the provider.
- Pending sign-ins live in memory; a restart during a sign-in means starting
  again.
- Tested against a test provider (unit tests and a browser run), not yet
  against the production Relyper IdP.
