# Backup as a service: customers (multi-tenant design)

Status: design, implemented in increments (see "Increments" at the end).
Relyper-owned document. The product concept in `docs/CONCEPT.md` stays the
source for everything else; this file adds the customer side.

## Goal

The operator (Relyper) runs Relyper Backup for customers. A customer:

- signs in and sees **only their own** backups: plans, last runs, snapshots,
  storage, problems;
- gets **e-mails** about successful and failed backups and about backups that
  stopped running;
- can **connect their own storage** (their S3 bucket, B2, REST server) so a
  copy of their data lives where they control it;
- **holds the key**: they download a recovery kit and can restore with the
  plain restic CLI without Relyper;
- can buy **key escrow** as an extra: the operator keeps a sealed copy of the
  customer key and can help when the customer has lost their kit.

The operator keeps the full Backrest/Relyper UI. Backups and restores keep
working without any AI feature, without the customer portal and without the
identity provider (local operator users stay emergency access).

## Terms

| Term | Meaning |
|---|---|
| Operator | Relyper staff. Local users and Relyper Identity users with `backup_user` / `backup_admin`. Full UI as today. |
| Tenant | One customer organisation. Has an ID (slug), a name, assigned plans and repositories, notification settings, escrow flag. |
| Member | A person of a tenant, signed in through Relyper Identity. |
| Portal | The customer web UI at `/portal` and its API at `/relyper/portal/api/`. |
| Server key | The repository password in `config.json` that Relyper Backup uses for scheduled backups. |
| Customer key | A second restic key of the same repository, created for the customer. Only the customer gets it. |

## Tenant model

Tenants live in a Relyper-owned file `<data>/relyper/tenants.json` (mode
0600), not in `config.json`, so upstream config migrations never see them.

```
Tenant {
  id            slug, 3–40 chars [a-z0-9-], immutable
  name          display name
  external_ids  values of the identity claim that map to this tenant
                (default: the id itself)
  suspended     members cannot sign in; backups keep running
  escrow        key escrow booked (paid add-on)
  plans         plan IDs assigned to the tenant
  repos         repository IDs assigned to the tenant
  own_repos     subset of repos the customer connected themselves
  notify        recipients, events (success, failure, warning, stale)
  kits          per repo: customer key ID, created at/by, escrowed yes/no
}
```

Assignment is explicit: the operator assigns existing plans and
repositories to a tenant. A plan or repository belongs to at most one
tenant. A plan is shown to the tenant only if its repository is assigned to
the same tenant too, so a misassigned plan never exposes a repository of
another customer. Unassigned objects belong to the operator only.

Repositories and plans a customer creates in the portal get IDs with the
prefix `t-<tenant>-` so they are recognisable in the operator UI.

## Who is who: sign-in and roles

Customers sign in through Relyper Identity (OIDC, #18). The provider already
issues roles and a tenant claim.

| Setting | Default | Meaning |
|---|---|---|
| `RELYPER_PORTAL` | off | `1` switches the portal on. |
| `RELYPER_TENANT_CLAIM` | `tenant` | Claim with the tenant. A string, or an object with `id` / `slug`. |
| `RELYPER_CUSTOMER_ROLE` | `backup_customer` | May view the tenant's backups. |
| `RELYPER_CUSTOMER_ADMIN_ROLE` | `backup_customer_admin` | Also manages notifications, own storage, recovery kits, restores. |

Rules at sign-in (callback):

1. A user with an operator role (`backup_user` / `backup_admin`) gets an
   operator session, as today. Operator roles win; an operator never acts as
   a tenant member.
2. Otherwise a user with a customer role **and** a tenant claim that matches
   an active tenant's `external_ids` gets a **portal session**.
3. Everyone else is refused. Unknown or suspended tenants are refused with
   the same message (no tenant enumeration).

Customer roles at a glance:

| Action | Viewer (`backup_customer`) | Admin (`backup_customer_admin`) | Operator |
|---|---|---|---|
| See plans, runs, snapshots, storage use, problems | yes | yes | all tenants |
| Notification recipients and events | – | yes | yes |
| Connect / remove own storage | – | yes | yes |
| Copy a plan to own storage | – | yes | yes |
| Start a backup now | – | yes | yes |
| Restore files as download | – | yes | yes |
| Create recovery kit (customer key) | – | yes | – (operator never sees the kit) |
| Delete snapshots, prune, change paths, hooks, schedules, retention | – | – | yes |
| MCP tokens | – | – | yes |

Destructive operations stay with the operator, server-side. A customer can
remove their own storage from Relyper Backup, which never deletes data in
it.

## Isolation

The portal is a separate surface, not a filtered view of the Backrest API.

1. **Separate session tokens.** Portal sessions are HS256 tokens with their
   own issuer (`relyper-backup/portal`) and audience. The operator verifier
   (`identity.VerifySession`, upstream JWT) refuses them, so a portal token
   cannot call `/v1.Backrest/*`, `/relyper/infra/`, `relyper.v1.RelyperMcp`,
   `/metrics` or `/mcp`. Operator tokens are not accepted by the portal API.
2. **Tenant from the session only.** The tenant comes from the signed
   session, checked against `tenants.json` on every request (suspension and
   role changes take effect immediately). No request parameter can choose
   another tenant.
3. **Every ID is checked.** Plan, repository, snapshot and operation IDs in a
   request must belong to the session's tenant. Snapshots and operations are
   read from the operation log by plan ID **and** repository GUID of an
   assigned pair. Not found and not yours look the same (404).
4. **No secrets leave the server.** Portal answers use own DTOs: no
   repository passwords, environment values, flags, hooks or URI user info
   (`rest:https://user:pass@…` is shown without `user:pass`). The only
   secret a portal user ever receives is the customer key in a recovery kit
   they create themselves.
5. **No commands.** Customer input never becomes a hook, a restic flag, a
   command prefix or an environment variable name. Own storage takes a
   fixed set of fields per storage type.
6. **No environment expansion.** Backrest expands `${VAR}` in repository
   URIs and environment values with the server's environment. Customer
   values containing `$` are rejected, so a customer cannot send the
   server's secrets to their storage endpoint.
7. **MCP.** MCP stays operator-only in this design: MCP tokens are created
   by operators and see everything; portal sessions cannot reach `/mcp` or
   create MCP tokens. Tenant-scoped MCP access (read-only tools filtered to
   one tenant, own token store per tenant) is a later increment and must not
   reuse the operator tools.
8. **Restores.** A customer restore writes into
   `<restore dir>/portal/<tenant>/<operation>` and is offered as a download
   for the members of that tenant only. Restores to arbitrary paths stay
   with the operator.
9. **Noisy neighbours.** restic calls are serialized per repository (one
   repo orchestrator each). "Backup now" and restores from the portal are
   limited per tenant (one running at a time, a few per hour).

The operator UI marks tenant objects and has a "Customers" page to create
tenants and assign plans and repositories.

## Notifications per tenant

E-mail through SMTP, configured by the operator:

| Variable | Meaning |
|---|---|
| `RELYPER_SMTP_HOST`, `RELYPER_SMTP_PORT` | Server (port 587 = STARTTLS, 465 = TLS). |
| `RELYPER_SMTP_USERNAME`, `RELYPER_SMTP_PASSWORD` / `RELYPER_SMTP_PASSWORD_FILE` | Login. |
| `RELYPER_SMTP_FROM` | Sender address. |
| `RELYPER_SMTP_TLS` | `starttls` (default), `tls`, or `none` (only for localhost). |

Per tenant, customer admins choose up to 10 recipients and the events:

| Event | Default | When |
|---|---|---|
| failure | on | A backup of an assigned plan ended with an error. |
| warning | on | A backup finished with warnings (unreadable files). |
| success | off | A backup finished successfully. |
| stale | on | An assigned plan had no successful backup for `RELYPER_TENANT_STALE_HOURS` (default 36). Sent once until it recovers. |

The source of truth is the operation log: the notifier subscribes to it and
sends only for final states of backup operations of assigned plans. Mails
contain plan name, time, status, size and error summary (with secrets
scrubbed), never paths of other tenants, passwords or URIs with user info.
Header injection is impossible by construction (addresses validated, no
user text in headers except the plan name, which is stripped of control
characters). Failures to send are logged without the SMTP password and do
not affect backups. Operator notifications (`RELYPER_NOTIFY_URLS`) stay as
they are.

## Own storage (bring your own repository)

A customer admin connects a repository of their own:

| Type | Fields |
|---|---|
| S3 / S3-compatible | endpoint (https), bucket, path, region, access key ID, secret key |
| Backblaze B2 | bucket, path, account ID, application key |
| Azure Blob | account name, account key, container, path |
| REST server | https URL, user, password |

SFTP (Host Europe) needs the SSH key and host key flow of #12/#13 and comes
in a later increment.

Steps: validate fields (no `$`, no control characters, https only, endpoint
host must not resolve to loopback, link-local or private addresses) →
generate a strong server key → `restic init` (or open the existing
repository with a password the customer enters) → store as Backrest
repository `t-<tenant>-<name>` with fixed flags only → assign to the tenant
as own storage.

To use it, the customer copies one of their plans to it: Relyper Backup
creates a second plan with the same paths, schedule, retention and the
operator's hooks (for example database dumps), pointed at the customer's
repository. The operator's copy keeps running; the customer gets a second,
independent copy (3-2-1). The customer cannot change paths or hooks.

Removing own storage deletes the plans that use it and the repository entry
in Relyper Backup, never data in the bucket.

Residual risk: the storage endpoint is customer-controlled. Relyper Backup
talks to it from the server, so a hostile endpoint sees the server's IP and
can send slow or large answers. DNS can change after validation (DNS
rebinding); the address check is repeated before each connection test, not
before every restic request.

## Keys: recovery kit and optional escrow

restic repositories can have several keys (passwords) for the same data.
Relyper Backup uses that:

- **Server key**: the repository password in `config.json`. Scheduled
  backups need it, so the running service can always read the data. That is
  the nature of a backup service that runs backups for you; this design
  does not pretend otherwise.
- **Customer key**: a separate key added with `restic key add`, created when
  a customer admin creates a recovery kit. The password is generated in
  memory, shown once as a download (the recovery kit) and **not stored** by
  Relyper Backup. Only its restic key ID is recorded.

The recovery kit (text file) contains: tenant, repository location without
operator credentials, the customer key, how to provide storage access
(credentials are included only for the customer's own storage, which they
typed themselves; for operator storage the kit says to request per-tenant
access), and the restic commands to list snapshots and restore. Creating a
new kit removes the previous customer key from the repository, so only the
latest kit works.

What the customer can do with it: restore without Relyper (plain restic),
and end the service on their own storage by removing the server key
(`restic key remove <id>`) after which Relyper Backup can no longer read or
write that repository.

**Without escrow (default):** nobody but the customer has the customer key.
If the customer loses the kit and the Relyper Backup instance and its
configuration backup are lost too, the data is unreadable. The operator
promises no recovery in that case.

**With escrow (paid add-on):** when the kit is created, Relyper Backup also
seals the customer key to the operator's escrow public key
(`RELYPER_ESCROW_PUBLIC_KEY`, X25519, NaCl sealed box) and stores the
sealed copy in `<data>/relyper/escrow/<tenant>/<repo>-<key id>.sealed`. The
running server cannot open it; the private key stays offline with the
operator. `relyper-escrow open` decrypts a sealed copy on the operator's
offline machine. Each open is a manual, logged support action. Escrow is
switched on per tenant by the operator; the kit states whether a sealed copy
exists. If escrow is booked but no public key is configured, kit creation
refuses instead of silently skipping escrow.

Escrow does not change what the running service can read (it has the server
key anyway); it protects against loss of the customer's kit.

## Threat model

| Threat | Mitigation |
|---|---|
| Customer A reads customer B's backups (IDOR) | Tenant only from the signed session; every ID checked against the tenant's assignments; snapshots read per assigned plan+repo pair; 404 for foreign IDs; tests for cross-tenant access. |
| Portal token used on the operator API | Different issuer/audience and key purpose; operator verifier refuses it; tests. |
| Operator token or cookie used in the portal | Portal accepts only portal tokens. |
| Forged tenant claim | Claims come only from an ID token verified against the provider's JWKS (RS256 pinned). Tenant must exist and be active locally; the operator controls `external_ids`. |
| Member removed at the provider | Session lifetime (`RELYPER_OIDC_SESSION_HOURS`). Suspending a tenant or removing an `external_id` takes effect on the next request. |
| Customer runs commands on the server | No hooks, flags, command prefixes or env names from customers; `$` rejected; fixed field set per storage type. |
| Server secrets sent to a customer endpoint | No env expansion of customer values (`$` rejected). |
| SSRF through a storage endpoint | https only; loopback, link-local, private and metadata addresses refused; DNS rebinding remains a residual risk (see above). |
| E-mail to the wrong tenant | Recipients are stored per tenant; the notifier resolves the tenant from the plan's assignment at send time. |
| E-mail header injection | Addresses parsed with `net/mail`, CR/LF stripped from the subject. |
| Recovery kit leaked | Only customer admins can create one; creating one is audited; a new kit revokes the previous customer key. The kit is never stored on the server. |
| Escrow private key stolen | It never lives on the server; sealed copies are useless without it. Keep it offline, ideally split between two people. |
| Malicious operator / compromised server | Can read everything (server key). Residual risk inherent to a managed service; customers who need more keep their own storage and can revoke the server key. Audit log records operator actions on tenants. |
| A customer floods the server (backup now, restores) | One running portal action per tenant, rate limits. |
| Enumeration of tenants or plans | Same error for unknown, suspended and foreign; no tenant list in the portal. |
| Customer deletes their backups | Not possible from the portal; deletion, prune and retention changes stay with the operator. |

## API surface

Operator (behind the web UI authentication, admin role for changes):
`/relyper/tenants/` – list, create, update (name, external IDs, suspended,
escrow), assign/unassign plans and repositories, view kits and escrow
entries (no secrets).

Portal (own authentication, portal tokens only): `/relyper/portal/api/` –
`me`, `overview`, `plans/<id>/snapshots`, `plans/<id>/operations`,
`notifications` (get/set, test mail), `storage` (list/add/remove own
storage, copy plan), `kits` (create), `backup` (start), `restores`.

The portal web UI is a separate React entry at `/portal` with its own token
in local storage; it never loads the operator shell.

## Increments

1. This design document.
2. Tenant store, operator API, portal sessions, read-only portal (overview,
   snapshots, runs), isolation tests.
3. E-mail notifications per tenant (SMTP, events, stale check).
4. Own storage (S3, B2, Azure, REST), plan copy to own storage.
5. Recovery kit with customer key, optional escrow, `relyper-escrow` tool.
6. Portal web UI and operator "Customers" page.
7. Later: restores as download, SFTP own storage, tenant-scoped MCP,
   billing hooks for escrow.
