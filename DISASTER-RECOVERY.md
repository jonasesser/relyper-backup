# Disaster recovery

This document covers recovery with and without Relyper Backup. For Docker
apps, the Infrastructure page (or the MCP tool `get_recovery_report`)
produces a recovery report for your actual setup: repositories, apps, images,
data paths, test results and the restore commands. Download it regularly and
keep it next to the repository password, outside the protected host.

## The one rule

**Keep the repository password and the storage credentials outside the
protected host.** Every Relyper Backup repository is a standard restic
repository encrypted with its password. If the server is lost together with
the only copy of that password, the backups cannot be decrypted by anyone.

Store, for every repository, in a password manager or on paper in a safe
place:

- repository URI (e.g. `sftp:user@host:/path/restic`)
- repository password
- storage credentials (SFTP user and private key, S3 access keys, …)
- for SFTP: the server's host key fingerprint (`SHA256:…`)

An offline copy of `/config/config.json` and `/config/.backrest-ssh/`
contains all of this, but it is a single file with all secrets in plaintext –
protect it accordingly.

## Scenario 1: restore single files with the web UI

1. Repositories → select the repository → Snapshots.
2. Open a snapshot, browse to the file or directory, choose Restore.
3. Restore into `/restore` (the staging mount), check the result, then copy
   it into place on the host deliberately.

Do not restore directly over production paths; production should not be
mounted writable in the first place.

## Scenario 2: the host or the container is gone

Relyper Backup is not needed to restore. Use the plain restic CLI on any
machine (restic 0.19.x or newer, https://restic.net):

```bash
export RESTIC_REPOSITORY='sftp:backup-user@storage.example.com:/restic'
export RESTIC_PASSWORD='…'                  # from your password manager
# SFTP: use the stored private key and verify the host key yourself
export RESTIC_SFTP_ARGS='-i /path/to/id_ed25519 -oUserKnownHostsFile=/path/to/known_hosts'

restic -o sftp.args="$RESTIC_SFTP_ARGS" snapshots
restic -o sftp.args="$RESTIC_SFTP_ARGS" restore latest --target /srv/restore
# only one directory, from a specific snapshot:
restic -o sftp.args="$RESTIC_SFTP_ARGS" restore 1a2b3c4d --target /srv/restore --include /sources/srv/app
```

Before connecting from a new machine, put the server's host key into the
`known_hosts` file you pass, and compare its fingerprint with the one you
stored (`ssh-keygen -lf known_hosts`).

Paths inside snapshots are container paths (e.g. `/sources/srv/...`), not
host paths. Map them back using your compose file.

This was verified during development: a backup made by the Relyper Backup
image to an OpenSSH SFTP server was restored with the plain restic CLI and was
byte-identical to the source (#13).

## Scenario 3: rebuild Relyper Backup on a new host

1. Deploy the image as described in `DEPLOYMENT.md`.
2. Either restore your offline copy of `/config` into the new `/config`
   volume before the first start (plans, repositories, users and SSH keys are
   then back), or start fresh and add the existing repositories again in the
   UI with their existing passwords. The SFTP connection test reports
   "existing restic repository found".
3. Keep the same container hostname if you want new snapshots to continue the
   old plan history (restic records the hostname).
4. Operation history (`/data`) is not needed; snapshots are read from the
   repository again ("Index snapshots").

## Databases

Copying a live database directory does **not** produce a reliable backup.
An app backup plan (Infrastructure page → "Create backup plan", or
`create_app_backup_plan`) dumps PostgreSQL, MySQL, MariaDB, MongoDB and Redis
with their own tools right before every backup; a failed dump fails the
backup. The dumps of an app are in the snapshot under
`/data/relyper/dumps/<app>/`, together with `relyper-dumps.json` (engine,
image, checksum, expected tables/collections/keys).

Restoring a dump by hand, with the image listed in the manifest:

| Engine | File | Restore |
|---|---|---|
| PostgreSQL | `<service>.sql` (pg_dumpall) | `psql -U <user> -d postgres -f <service>.sql` |
| MySQL / MariaDB | `<service>.sql` | `mysql -u root -p < <service>.sql` (or `mariadb`) |
| MongoDB | `<service>.archive.gz` | `mongorestore --archive=<service>.archive.gz --gzip --drop` |
| Redis | `<service>.rdb` | stop Redis, copy the file to `/data/dump.rdb`, start Redis |

Relyper Backup tests this automatically: "Test database restore" (and the
weekly schedule, `RELYPER_VERIFY_INTERVAL_DAYS`) restores the dumps from the
latest snapshot into a throwaway container without network and compares the
counts with the manifest. It never restores into a running production
database.

Databases that are not detected (SQLite inside an app volume, other
engines) are backed up as files only; stop the app or dump them from your own
hook if you need consistency.

## Docker secrets and other non-exportable credentials

Docker/Swarm secrets cannot be read back from a running system in a
recoverable way. Relyper Backup cannot restore them; keep their source values
in your password manager or deployment repository.

## Verifying recoverability

A successful backup is not proof that a restore works. Relyper Backup
checks, per app (Infrastructure page → Readiness):

- coverage: every data item is in a completed snapshot younger than
  `RELYPER_COVERAGE_MAX_AGE_HOURS` (48);
- database restore test (see above), at most 14 days old;
- file restore test: sample files restored from the latest snapshot into a
  temporary directory and compared by size;
- repository check (`restic check`) at most 35 days old;
- an off-site repository (not a local path).

Problems are sent to `RELYPER_NOTIFY_URLS` when they appear. In addition:

- schedule `restic check` for every repository (repository settings → check
  policy); consider "read data subset" from time to time;
- restore a sample regularly into `/restore` and compare it with the source;
- for SFTP storage, also confirm the provider quota is not close to full
  (the connection test shows free space when the server reports it).

The file restore test compares sizes, not contents; `restic check
--read-data-subset` covers the data itself.
