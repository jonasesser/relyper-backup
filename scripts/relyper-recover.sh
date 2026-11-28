#!/bin/sh
# Relyper Backup recovery bootstrap. Relyper-owned.
#
# Restores backups made by Relyper Backup when the original server and the
# Relyper Backup installation are gone. It needs only restic (and docker to
# load database dumps); the repositories are standard restic repositories.
#
#   export RESTIC_REPOSITORY=sftp:user@host:/backups/relyper
#   export RESTIC_PASSWORD=...            # or RESTIC_PASSWORD_FILE
#   sh relyper-recover.sh plans           # plans and their latest snapshots
#   sh relyper-recover.sh report          # the recovery report from the newest snapshot
#   sh relyper-recover.sh restore <plan> <target> [snapshot]
#
# A restore always goes into an empty target directory, never on top of
# live data. The script then points to the restore notes and the database
# restore script stored next to the dumps in the snapshot.
set -eu

RESTIC=${RESTIC:-restic}

die() { echo "relyper-recover: $*" >&2; exit 1; }

need_restic() {
	command -v "$RESTIC" >/dev/null 2>&1 || die "restic not found; install it (https://restic.net) or set RESTIC=/path/to/restic"
	[ -n "${RESTIC_REPOSITORY:-}${RESTIC_REPOSITORY_FILE:-}" ] || die "set RESTIC_REPOSITORY (and the storage variables it needs)"
	[ -n "${RESTIC_PASSWORD:-}${RESTIC_PASSWORD_FILE:-}${RESTIC_PASSWORD_COMMAND:-}" ] || die "set RESTIC_PASSWORD or RESTIC_PASSWORD_FILE"
}

# snapshot ids, newest first
snapshot_ids() {
	"$RESTIC" snapshots --json "$@" | tr ',' '\n' | grep '"short_id"' | cut -d'"' -f4 | awk '{ l[NR] = $0 } END { for (i = NR; i > 0; i--) print l[i] }'
}

cmd_plans() {
	need_restic
	plans=$("$RESTIC" snapshots --json | tr ',[]' '\n\n\n' | grep -o '"plan:[^"]*"' | tr -d '"' | sort -u)
	[ -n "$plans" ] || die "no snapshots with a plan tag found"
	for p in $plans; do
		echo "== ${p#plan:}"
		"$RESTIC" snapshots --tag "$p" --latest 1 --compact | sed -n '3,$p' | grep -v '^-\{3,\}' | grep -v 'snapshots$' || true
	done
}

cmd_report() {
	need_restic
	for id in $(snapshot_ids); do
		f=$("$RESTIC" ls "$id" 2>/dev/null | grep '/_inventory/recovery-report.md$' | head -n 1 || true)
		if [ -n "$f" ]; then
			echo "# from snapshot $id: $f" >&2
			"$RESTIC" dump "$id" "$f"
			return 0
		fi
	done
	die "no recovery report found in any snapshot"
}

cmd_restore() {
	[ $# -ge 2 ] || die "usage: restore <plan> <target> [snapshot]"
	plan=$1
	target=$2
	snap=${3:-latest}
	need_restic
	if [ -e "$target" ] && [ -n "$(ls -A "$target" 2>/dev/null)" ]; then
		die "$target is not empty; restore into an empty directory"
	fi
	mkdir -p "$target"
	if [ "$snap" = latest ]; then
		"$RESTIC" restore latest --tag "plan:$plan" --target "$target"
	else
		"$RESTIC" restore "$snap" --target "$target"
	fi
	echo
	echo "Restored plan $plan ($snap) into $target."
	found=0
	for notes in $(find "$target" -name RESTORE.md -path '*dumps*' 2>/dev/null); do
		found=1
		d=$(dirname "$notes")
		echo
		echo "Database dumps: $d"
		echo "  read:   $notes"
		echo "  load:   sh $d/restore-databases.sh [service=container ...]   (start the database containers first)"
	done
	report=$(find "$target" -path '*/_inventory/recovery-report.md' 2>/dev/null | head -n 1)
	if [ -n "$report" ]; then
		echo
		echo "Recovery report (services, images, host paths, restore order): $report"
	fi
	[ "$found" = 1 ] || echo "No database dumps in this snapshot."
	echo
	echo "Next: recreate the containers, copy the files from $target to their host paths"
	echo "(paths below $target mirror the original ones), load the databases, start the app."
}

case "${1:-}" in
plans) shift; cmd_plans "$@" ;;
report) shift; cmd_report "$@" ;;
restore) shift; cmd_restore "$@" ;;
*)
	sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
