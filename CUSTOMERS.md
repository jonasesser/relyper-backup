# Customers: backup as a service

Relyper Backup can run backups for customers. Each customer (tenant) signs
in to a **customer portal** and sees only their own backups, gets e-mails
about them and can manage a few things themselves. The operator keeps the
full web UI. Design, isolation rules and threat model:
`docs/CONCEPT-MULTITENANT.md`.

## What customers can do

| Action | Viewer (`backup_customer`) | Admin (`backup_customer_admin`) |
|---|---|---|
| See plans, last runs, snapshots, state and problems | yes | yes |
| Set e-mail recipients and events, send a test mail | – | yes |
| Start a backup now (at most 6 portal actions per hour) | – | yes |
| Connect own storage (S3, B2, Azure, REST) and copy a plan to it | – | yes |
| Create a recovery kit (own restic key) | – | yes |

Deleting snapshots, prune, retention, paths, hooks and schedules stay with
the operator.

## Setup

1. Relyper Identity sign-in must be configured (`IDENTITY.md`). Customers
   sign in only through it.
2. In Relyper Identity:
   - List the application roles `backup_admin`, `backup_user`,
     `backup_customer` and `backup_customer_admin` in the `allowed_roles`
     of the Relyper Backup client.
   - In the master tenant (`relyper`), put `backup_admin` (or
     `backup_user`) into the team template of your own team.
   - In each customer's tenant, put `backup_customer` and
     `backup_customer_admin` into that customer's team templates. Only
     global admins maintain team templates; a customer's `tenant_admin` can
     only add their own users to existing teams, so customers cannot give
     themselves roles.
   - The ID token's `tenant` claim is the user's first tenant, and `roles`
     holds only the roles of that tenant (plus global roles).
   Operator roles count only in the operator tenant
   (`RELYPER_OIDC_OPERATOR_TENANT`, see `IDENTITY.md`). There they win over
   customer roles; in any other tenant they are ignored, so a mistake in a
   customer's team template does not make that customer an operator.
3. Set the environment and restart:

| Variable | Default | Meaning |
|---|---|---|
| `RELYPER_PORTAL` | off | `1` switches the customer portal on. |
| `RELYPER_TENANT_CLAIM` | `tenant` | Claim in the ID token with the tenant. |
| `RELYPER_CUSTOMER_ROLE` | `backup_customer` | View role. |
| `RELYPER_CUSTOMER_ADMIN_ROLE` | `backup_customer_admin` | Admin role. |
| `RELYPER_TENANT_STALE_HOURS` | `36` | A scheduled plan without a successful backup for this long is "stale". |
| `RELYPER_SMTP_HOST` | unset (no e-mail) | SMTP server for customer e-mails. |
| `RELYPER_SMTP_PORT` | `587` (`465` with `tls`) | |
| `RELYPER_SMTP_TLS` | `starttls` | `starttls`, `tls` or `none` (only for localhost). |
| `RELYPER_SMTP_USERNAME`, `RELYPER_SMTP_PASSWORD` / `RELYPER_SMTP_PASSWORD_FILE` | – | SMTP login. |
| `RELYPER_SMTP_FROM` | – | Sender, e.g. `Relyper Backup <backup@relyper.de>`. |

4. Create the tenant on the operator page **Customers**: ID (slug), name and
   the tenant claim values that belong to it (empty = the ID itself). Then
   assign the customer's plans **and** their repositories. A plan is shown
   to the customer only when its repository is assigned to the same tenant.
5. Send customers the portal link: `https://<your-domain>/#/portal`.

Plans and repositories that are deleted in the configuration are removed
from their tenant automatically, so a later object with the same ID is not
given to the customer by accident.

## E-mails

Customer admins choose up to 10 recipients and the events: failed backup
(default on), backup with warnings (on), successful backup (off), no
successful backup for `RELYPER_TENANT_STALE_HOURS` (on, sent once until it
recovers). Mails are sent from the operation log, so they reflect what
really happened. They contain plan, times, snapshot and sizes, never
passwords or storage credentials. If SMTP is not configured, the portal
says so and nothing is sent. Operator notifications (`RELYPER_NOTIFY_URLS`)
are separate.

## Own storage

Customer admins can connect their own S3 (or S3-compatible), Backblaze B2,
Azure Blob or REST server repository. They enter fixed fields only (no
restic flags, hooks or variable names); values with `$` are refused,
endpoints must be https and must not point to loopback, private or
link-local addresses. A new repository gets a generated key; an existing
one is opened with its password. The repository appears in your
configuration as `t-<tenant>-<name>` with a monthly structure check and a
monthly prune.

"Copy plan" creates a second plan with the same paths, schedule, retention
and your hooks (for example database dumps) that writes to the customer's
storage. Your plan keeps running. Disconnecting the storage removes the
repository entry and its plans in Relyper Backup, never data in the
storage.

SFTP is not offered in the portal yet (it needs the host key flow).

## Recovery kits and key escrow

A recovery kit is a text file with the repository location, a **customer
key** and the restic commands to restore without Relyper Backup. The
customer key is a separate restic key (`restic key add`), so the server's
own key (the repository password in `config.json`) is never handed out.
Relyper Backup shows the kit once and does not store the key; it records
only the key ID. Creating a new kit removes the previous customer key. On
the customer's own storage the kit also says how to remove the server key
to end the service.

For your storage the kit contains no storage credentials: if customers need
to restore without you, give them per-customer read access to their
location (for example a separate S3 key limited to their prefix).

**Key escrow** (paid add-on) keeps a sealed copy of the customer key for
you:

1. On an offline machine: `go build ./cmd/relyper-escrow` and
   `relyper-escrow keygen escrow.key`. Keep `escrow.key` offline (and a
   second copy in a safe place); it is never put on the server.
2. Set the printed `RELYPER_ESCROW_PUBLIC_KEY=…` on the server and restart.
3. Tick "Key escrow booked" for the customer on the Customers page.

From then on each new kit is also sealed to the public key in
`<data>/relyper/escrow/<tenant>/<repo>-<key>.sealed`. The server cannot
open it. To help a customer who lost the kit, copy the file to the offline
machine and run `relyper-escrow open escrow.key <file>`. If escrow is
booked but no public key is set, kit creation is refused, so a customer
never believes in a copy that does not exist. Kits created before escrow
was booked have no sealed copy; the customer creates a new kit.

Escrow does not change what the running server can read: it needs a key for
scheduled backups anyway.

## Audit

Portal actions are written to `<data>/relyper/audit.jsonl` with source
`portal` and the actor `user:relyper:<e-mail> (tenant <id>)`; tenant
changes by operators with source `ui`.

## Limits

- Customers must have an account in Relyper Identity; there are no local
  customer passwords.
- A session stays valid until it expires (`RELYPER_OIDC_SESSION_HOURS`), but
  suspending the tenant, removing its claim value or the customer role in
  the settings takes effect on the next request.
- MCP stays operator-only.
- Restores from the portal (download of files) are not built yet; customers
  restore with their recovery kit or ask you.
- Own storage was checked by validation and unit tests and the kit against a
  real restic repository; real S3/B2/Azure/REST accounts were not tested
  yet.
