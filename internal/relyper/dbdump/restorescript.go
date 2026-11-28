// Relyper-owned: a restore script and notes written next to the dumps of an
// app and backed up with them, so a snapshot explains itself: after a plain
// `restic restore`, the databases can be loaded without Relyper Backup.
package dbdump

import (
	"fmt"
	"sort"
	"strings"
)

// Names of the files written next to the dumps.
const (
	RestoreScriptName = "restore-databases.sh"
	RestoreNotesName  = "RESTORE.md"
)

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// RestoreScript returns a POSIX shell script that loads every dump of the
// manifest into a running container of the same image. Usage:
//
//	sh restore-databases.sh [--yes] [service=container ...]
//
// Without a mapping the original container names are used. Credentials are
// read inside the target container from its own environment, as for dumps.
func RestoreScript(m *Manifest) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("#!/bin/sh\n")
	w("# Relyper Backup: loads the database dumps of app %s (dumped %s)\n", m.App, m.CreatedAt.Format("2006-01-02 15:04 MST"))
	w("# into running containers. It REPLACES the content of those databases.\n")
	w("# Usage: sh %s [--yes] [--database=service/name] [service=container ...]\n", RestoreScriptName)
	w("# Without --database every dump replaces its whole database instance\n")
	w("# (all databases in it). --database=service/name loads one database of a\n")
	w("# shared instance and leaves the others alone.\n")
	w("# Start the database containers first (same images as listed below).\n")
	w("set -eu\n")
	w("cd \"$(dirname \"$0\")\"\n")
	w("yes=0\nmap=\"\"\nonly=\"\"\ndone_only=0\n")
	w("for a in \"$@\"; do case \"$a\" in --yes) yes=1 ;; --database=*) only=\"${a#--database=}\" ;; *=*) map=\"$map $a\" ;; *) echo \"unknown argument: $a\" >&2; exit 2 ;; esac; done\n")
	w("target() { for m in $map; do [ \"${m%%%%=*}\" = \"$1\" ] && { echo \"${m#*=}\"; return; }; done; echo \"$2\"; }\n")
	w("confirm() { [ \"$yes\" = 1 ] && return 0; printf '%%s [y/N] ' \"$1\"; read -r r; [ \"$r\" = y ] || [ \"$r\" = Y ]; }\n")
	w("command -v docker >/dev/null || { echo 'docker is required' >&2; exit 1; }\n\n")
	for _, d := range m.Dumps {
		e, ok := engines[d.Engine]
		if !ok {
			continue
		}
		w("# %s: %s, image %s, file %s, sha256 %s\n", d.Service, d.Engine, d.Image, d.File, d.SHA256)
		w("c=$(target %s %s)\n", shQuote(d.Service), shQuote(d.Container))
		if e.importDB != "" && len(d.Databases) > 0 {
			w("if [ -n \"$only\" ]; then case \"$only\" in\n")
			for _, db := range d.Databases {
				w("  %s)\n", shQuote(d.Service+"/"+db.Name))
				w("    if [ -n \"$(command -v sha256sum)\" ]; then echo %s | sha256sum -c - >/dev/null || { echo %s >&2; exit 1; }; fi\n",
					shQuote(db.SHA256+"  "+db.File), shQuote(db.File+": checksum mismatch, the dump is damaged"))
				w("    if confirm %s\"$c\"%s; then\n", shQuote("Replace only database "+db.Name+" of "+d.Service+" with "+db.File+" in container "), shQuote("?"))
				w("      docker exec -i -e RELYPER_DB=%s \"$c\" sh -c %s < %s\n", shQuote(db.Name), shQuote(e.importDB), shQuote(db.File))
				w("      echo %s\"$c\"\n    fi\n    done_only=1 ;;\n", shQuote(d.Service+"/"+db.Name+": loaded into "))
			}
			w("esac; fi\n")
		}
		w("if [ -z \"$only\" ]; then\n")
		w("if [ -n \"$(command -v sha256sum)\" ]; then echo %s | sha256sum -c - >/dev/null || { echo %s >&2; exit 1; }; fi\n",
			shQuote(d.SHA256+"  "+d.File), shQuote(d.File+": checksum mismatch, the dump is damaged"))
		if len(d.Databases) > 0 {
			var names []string
			for _, db := range d.Databases {
				names = append(names, db.Name)
			}
			w("echo %s\n", shQuote(fmt.Sprintf("Note: %s holds %d databases (%s). The next step replaces ALL of them. To load one, use --database=%s/<name>.", d.Service, len(names), strings.Join(names, ", "), d.Service)))
		}
		if e.importStdin != "" {
			w("if confirm \"Replace the %s databases in container $c with %s?\"; then\n", d.Engine, d.File)
			w("  docker exec -i \"$c\" sh -c %s < %s\n", shQuote(e.importStdin), shQuote(d.File))
			w("  echo \"%s: loaded into $c\"\nfi\n", d.Service)
		} else if e.restoreBeforeStart != "" {
			w("if confirm \"Replace the data of %s in container $c with %s (the container is restarted)?\"; then\n", d.Engine, d.File)
			w("  docker stop \"$c\" >/dev/null\n")
			w("  docker cp %s \"$c\":%s/dump.rdb\n", shQuote(d.File), e.restoreBeforeStart)
			w("  docker start \"$c\" >/dev/null\n")
			w("  echo \"%s: copied into $c and restarted (if Redis uses appendonly, disable it for the first start)\"\nfi\n", d.Service)
		}
		w("fi\n\n")
	}
	w("if [ -n \"$only\" ] && [ \"$done_only\" = 0 ]; then echo \"no single-database dump named $only; see %s\" >&2; exit 2; fi\n", RestoreNotesName)
	w("echo 'Done. Compare the content with the expected counts in %s.'\n", RestoreNotesName)
	return b.String()
}

// RestoreNotes returns human-readable notes for the dumps.
func RestoreNotes(m *Manifest) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Database dumps of %s\n\n", m.App)
	w("Dumped %s by Relyper Backup. These files are consistent dumps made with the databases' own tools; ", m.CreatedAt.Format("2006-01-02 15:04 MST"))
	w("they contain no passwords of Relyper Backup (database credentials stay in the containers' environment).\n\n")
	w("| Service | Engine | Image | File | Expected content |\n|---|---|---|---|---|\n")
	for _, d := range m.Dumps {
		var parts []string
		for _, k := range sortedKeys(d.Stats) {
			parts = append(parts, fmt.Sprintf("%s: %d", k, d.Stats[k]))
		}
		w("| %s | %s | `%s` | `%s` | %s |\n", d.Service, d.Engine, d.Image, d.File, strings.Join(parts, ", "))
	}
	w("\n## Restore\n\n")
	w("1. Start the database containers with the same images (from the app's compose file or deployment), with the same credentials in their environment.\n")
	w("2. Run `sh %s` in this directory on the Docker host. It asks before replacing each database; `--yes` skips the questions. ", RestoreScriptName)
	w("Use `service=container` arguments when the new containers have other names.\n")
	w("3. Compare the content with the table above, then start the rest of the app.\n\n")
	single := false
	for _, d := range m.Dumps {
		if len(d.Databases) > 0 {
			if !single {
				w("## Single databases of a shared server\n\n")
				w("Without options the script replaces each database server as a whole, with all its databases. Each database is also dumped on its own; load one without touching the others:\n\n")
				single = true
			}
			for _, db := range d.Databases {
				w("- `sh %s --database=%s/%s` (file `%s`)\n", RestoreScriptName, d.Service, db.Name, db.File)
			}
		}
	}
	if single {
		w("\n")
	}
	w("Manual commands per engine: PostgreSQL `psql -U <user> -d postgres < file.sql`; MySQL/MariaDB `mysql -u root -p < file.sql`; ")
	w("MongoDB `mongorestore --archive=file.archive.gz --gzip --drop`; Redis: stop it, copy the file to `/data/dump.rdb`, start it.\n")
	return b.String()
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
