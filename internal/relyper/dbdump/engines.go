// Package dbdump is Relyper-owned: consistent database dumps with the
// databases' own tools, run inside the database container through the
// Docker API, and verification of a dump by restoring it into a throwaway
// container.
//
// Credentials never pass through Relyper Backup: the dump commands run in a
// shell inside the database container and read the container's own
// environment variables (and *_FILE secrets) there.
package dbdump

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

// engine describes how to dump, restore and inspect one database engine.
type engine struct {
	ext string
	// dump writes the dump to stdout.
	dump string
	// stats prints "name<TAB>count" lines describing the content.
	stats string
	// ready exits 0 when a fresh verification container accepts connections.
	ready string
	// restore loads /tmp/relyper-restore/<file> into a fresh container.
	restore string
	// verifyEnv returns the environment of a fresh verification container.
	verifyEnv func(password, user string) []string
	// restoreBeforeStart: the dump file is copied before the container
	// starts (Redis loads dump.rdb at startup).
	restoreBeforeStart string
}

// readSecret expands VAR or VAR_FILE inside the container.
func readSecret(names ...string) string {
	// The first non-empty variable wins; the value stays in the container.
	var b strings.Builder
	b.WriteString(`__s=""; `)
	for _, n := range names {
		fmt.Fprintf(&b, `[ -z "$__s" ] && __s="${%s:-}"; [ -z "$__s" ] && [ -n "${%s_FILE:-}" ] && __s="$(cat "${%s_FILE}")"; `, n, n, n)
	}
	return b.String()
}

const pgUser = `__u="${POSTGRES_USER:-}"; [ -z "$__u" ] && [ -n "${POSTGRES_USER_FILE:-}" ] && __u="$(cat "$POSTGRES_USER_FILE")"; [ -z "$__u" ] && __u=postgres; `

var engines = map[string]engine{
	discovery.EnginePostgres: {
		ext:     "sql",
		dump:    readSecret("POSTGRES_PASSWORD") + pgUser + `PGPASSWORD="$__s" exec pg_dumpall -U "$__u" -h 127.0.0.1 2>/dev/null || PGPASSWORD="$__s" exec pg_dumpall -U "$__u"`,
		stats:   readSecret("POSTGRES_PASSWORD") + pgUser + `export PGPASSWORD="$__s"; for d in $(psql -U "$__u" -d postgres -At -c "select datname from pg_database where not datistemplate order by 1"); do printf '%s\t%s\n' "$d" "$(psql -U "$__u" -d "$d" -At -c "select count(*) from information_schema.tables where table_schema not in ('pg_catalog','information_schema')")"; done`,
		ready:   pgUser + `pg_isready -q -h 127.0.0.1 -U "$__u"`,
		restore: pgUser + `psql -q -U "$__u" -d postgres -f /tmp/relyper-restore/dump.sql >/tmp/relyper-restore/restore.log 2>&1; grep -E "^(psql:.*)?FATAL" /tmp/relyper-restore/restore.log && exit 1; exit 0`,
		verifyEnv: func(pw, user string) []string {
			env := []string{"POSTGRES_PASSWORD=" + pw}
			if user != "" {
				env = append(env, "POSTGRES_USER="+user)
			}
			return env
		},
	},
	discovery.EngineMySQL:   mysqlEngine(),
	discovery.EngineMariaDB: mysqlEngine(),
	discovery.EngineRedis: {
		ext:                "rdb",
		dump:               readSecret("REDIS_PASSWORD") + `[ -n "$__s" ] && export REDISCLI_AUTH="$__s"; f=/tmp/relyper-dump-$$.rdb; redis-cli --rdb "$f" >/dev/null 2>&1 || { echo "redis-cli --rdb failed" >&2; exit 1; }; cat "$f"; rm -f "$f"`,
		stats:              readSecret("REDIS_PASSWORD") + `[ -n "$__s" ] && export REDISCLI_AUTH="$__s"; redis-cli info keyspace | tr -d '\r' | grep '^db' | sed 's/:keys=/\t/; s/,.*//'`,
		ready:              `redis-cli ping | grep -q PONG`,
		restoreBeforeStart: "/data",
		verifyEnv:          func(string, string) []string { return nil },
	},
	discovery.EngineMongo: {
		ext:     "archive.gz",
		dump:    readSecret("MONGO_INITDB_ROOT_PASSWORD") + `__u="${MONGO_INITDB_ROOT_USERNAME:-}"; if [ -n "$__u" ]; then exec mongodump --quiet --archive --gzip --username "$__u" --password "$__s" --authenticationDatabase admin; else exec mongodump --quiet --archive --gzip; fi`,
		stats:   readSecret("MONGO_INITDB_ROOT_PASSWORD") + `__u="${MONGO_INITDB_ROOT_USERNAME:-}"; a=""; [ -n "$__u" ] && a="--username $__u --password $__s --authenticationDatabase admin"; mongosh --quiet $a --eval 'db.adminCommand({listDatabases:1}).databases.forEach(function(d){ if(["admin","config","local"].indexOf(d.name)<0){ print(d.name+"\t"+db.getSiblingDB(d.name).getCollectionNames().length) } })'`,
		ready:   `mongosh --quiet --eval 'db.runCommand({ping:1}).ok' | grep -q 1`,
		restore: `mongorestore --quiet --archive=/tmp/relyper-restore/dump.archive.gz --gzip --drop`,
		verifyEnv: func(string, string) []string {
			return nil
		},
	},
}

func mysqlEngine() engine {
	pw := readSecret("MARIADB_ROOT_PASSWORD", "MYSQL_ROOT_PASSWORD")
	user := `__mu=root; __mp="$__s"; if [ -z "$__mp" ]; then __mu="${MARIADB_USER:-${MYSQL_USER:-root}}"; __mp="${MARIADB_PASSWORD:-${MYSQL_PASSWORD:-}}"; fi; `
	tool := `__d=$(command -v mariadb-dump || command -v mysqldump); __c=$(command -v mariadb || command -v mysql); `
	return engine{
		ext:     "sql",
		dump:    pw + user + tool + `__db="${MARIADB_DATABASE:-${MYSQL_DATABASE:-}}"; if [ "$__mu" = root ]; then MYSQL_PWD="$__mp" exec "$__d" -u root --all-databases --single-transaction --routines --events --triggers --hex-blob; else MYSQL_PWD="$__mp" exec "$__d" -u "$__mu" --databases "$__db" --single-transaction --routines --triggers --hex-blob --no-tablespaces; fi`,
		stats:   pw + user + tool + `MYSQL_PWD="$__mp" "$__c" -u "$__mu" -N -B -e "select s.schema_name, count(t.table_name) from information_schema.schemata s left join information_schema.tables t on t.table_schema=s.schema_name where s.schema_name not in ('mysql','information_schema','performance_schema','sys') group by s.schema_name order by 1"`,
		ready:   pw + tool + `MYSQL_PWD="$__s" "$__c" -u root --protocol=tcp -h 127.0.0.1 -N -B -e "select 1" >/dev/null 2>&1`,
		restore: pw + tool + `MYSQL_PWD="$__s" "$__c" -u root --protocol=tcp -h 127.0.0.1 < /tmp/relyper-restore/dump.sql`,
		verifyEnv: func(pw, _ string) []string {
			return []string{"MARIADB_ROOT_PASSWORD=" + pw, "MYSQL_ROOT_PASSWORD=" + pw}
		},
	}
}

// parseStats reads "name<TAB>count" lines.
func parseStats(out string) map[string]int64 {
	m := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		name, n, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			continue
		}
		m[strings.TrimSpace(name)] = v
	}
	return m
}

// compareStats checks that everything in want exists in got with the same
// count. Extra entries in got (e.g. the default database) are fine.
func compareStats(want, got map[string]int64) []string {
	var problems []string
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		g, ok := got[n]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s missing after restore", n))
		case g != want[n]:
			problems = append(problems, fmt.Sprintf("%s: %d after restore, %d at dump time", n, g, want[n]))
		}
	}
	return problems
}
