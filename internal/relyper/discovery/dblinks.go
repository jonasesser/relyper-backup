// Relyper-owned: which databases an app connects to.
//
// Many apps use a database that runs as its own service (often its own
// Easypanel service). The connection is read from the container's
// environment (connection URLs and *_HOST variables); only the engine, the
// host name and the database name are kept, never user names, passwords or
// other values.
package discovery

import (
	"bytes"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DatabaseLink is a database a service connects to.
type DatabaseLink struct {
	Engine   string `json:"engine"`
	Host     string `json:"host,omitempty"`
	Database string `json:"database,omitempty"`
	// Var is the environment variable it was read from (name only).
	Var string `json:"var"`
	// App and Service are the app and service that run the database, when
	// it runs on this host; empty for an external database.
	App     string `json:"app,omitempty"`
	Service string `json:"service,omitempty"`
}

var urlSchemes = map[string]string{
	"postgres": EnginePostgres, "postgresql": EnginePostgres,
	"mysql": EngineMySQL, "mariadb": EngineMariaDB,
	"mongodb": EngineMongo, "mongodb+srv": EngineMongo,
	"redis": EngineRedis, "rediss": EngineRedis,
}

var hostVar = regexp.MustCompile(`^(?:(POSTGRES|POSTGRESQL|PG|MYSQL|MARIADB|MONGO|MONGODB|REDIS|DB|DATABASE)_)?(?:DB_)?HOST(?:NAME)?$`)
var sqliteFile = regexp.MustCompile(`(?i)\.(sqlite3?|db)$`)

var hostVarEngine = map[string]string{
	"POSTGRES": EnginePostgres, "POSTGRESQL": EnginePostgres, "PG": EnginePostgres,
	"MYSQL": EngineMySQL, "MARIADB": EngineMariaDB,
	"MONGO": EngineMongo, "MONGODB": EngineMongo, "REDIS": EngineRedis,
}

// databaseLinks reads the database connections from KEY=value pairs.
func databaseLinks(env []string) []DatabaseLink {
	var out []DatabaseLink
	seen := map[string]bool{}
	add := func(l DatabaseLink) {
		k := l.Engine + "|" + l.Host + "|" + l.Database
		if l.Host == "" && l.Database == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, l)
	}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || v == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, "://"); i > 0 {
			if engine, ok := urlSchemes[strings.ToLower(v[:i])]; ok {
				if u, err := url.Parse(v); err == nil {
					host := u.Hostname()
					if j := strings.IndexByte(host, ','); j >= 0 {
						host = host[:j]
					}
					db := strings.TrimPrefix(u.Path, "/")
					if engine == EngineRedis {
						db = ""
					}
					add(DatabaseLink{Engine: engine, Host: host, Database: db, Var: k})
				}
				continue
			}
		}
		if strings.HasPrefix(strings.ToLower(v), "file:") || (sqliteFile.MatchString(v) && path.IsAbs(strings.TrimPrefix(v, "file:"))) {
			if strings.Contains(strings.ToUpper(k), "DATABASE") || strings.Contains(strings.ToUpper(k), "DB") || strings.Contains(strings.ToUpper(k), "SQLITE") {
				f := strings.TrimPrefix(strings.TrimPrefix(v, "file:"), "//")
				if i := strings.IndexByte(f, '?'); i >= 0 {
					f = f[:i]
				}
				add(DatabaseLink{Engine: "sqlite", Database: f, Var: k})
			}
			continue
		}
		if m := hostVar.FindStringSubmatch(strings.ToUpper(k)); m != nil && (m[1] != "" || strings.Contains(strings.ToUpper(k), "DB")) {
			if strings.ContainsAny(v, " /@") {
				continue
			}
			add(DatabaseLink{Engine: hostVarEngine[m[1]], Host: v, Var: k})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Var < out[j].Var })
	return out
}

// resolveDatabaseLinks finds the app and service that run each linked
// database (by Swarm service name, compose service name or container
// name) and drops links to the app's own database services.
func resolveDatabaseLinks(apps map[string]*App) {
	type target struct{ app, svc, engine string }
	byHost := map[string]target{}
	for _, a := range apps {
		for _, s := range a.Services {
			if s.Database == nil {
				continue
			}
			t := target{a.ID, s.Name, s.Database.Engine}
			for _, h := range []string{s.SwarmService, s.Container, s.Name, s.Labels["com.docker.compose.service"]} {
				if h != "" {
					if _, dup := byHost[strings.ToLower(h)]; !dup {
						byHost[strings.ToLower(h)] = t
					}
				}
			}
		}
	}
	for _, a := range apps {
		for i := range a.Services {
			s := &a.Services[i]
			kept := s.DatabaseLinks[:0]
			for _, l := range s.DatabaseLinks {
				if t, ok := byHost[strings.ToLower(l.Host)]; ok {
					if t.app == a.ID {
						continue // the app's own database, already listed
					}
					l.App, l.Service = t.app, t.svc
					if l.Engine == "" {
						l.Engine = t.engine
					}
				}
				kept = append(kept, l)
			}
			if len(kept) == 0 {
				kept = nil
			}
			s.DatabaseLinks = kept
		}
	}
}

var plainHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// SafeDatabaseEnv returns the part of an environment variable that tells
// which database a container connects to, without anything secret: for a
// connection URL scheme://host[:port]/database (no user, password or
// query), for a *_HOST variable the plain host name, for an SQLite
// variable the file path. ok is false for every other variable. The Docker
// guard uses it so that Relyper Backup sees database connections but no
// secrets.
func SafeDatabaseEnv(name, value string) (string, bool) {
	value = strings.TrimSpace(value)
	links := databaseLinks([]string{name + "=" + value})
	if len(links) != 1 {
		return "", false
	}
	l := links[0]
	switch {
	case l.Engine == "sqlite":
		return l.Database, true
	case strings.Contains(value, "://"):
		scheme, _, _ := strings.Cut(value, "://")
		u, err := url.Parse(value)
		if err != nil || !plainHost.MatchString(l.Host) {
			return "", false
		}
		out := strings.ToLower(scheme) + "://" + l.Host
		if p := u.Port(); p != "" {
			out += ":" + p
		}
		if l.Database != "" && plainHost.MatchString(l.Database) {
			out += "/" + l.Database
		}
		return out, true
	case plainHost.MatchString(l.Host):
		return l.Host, true
	}
	return "", false
}

// findSQLite looks for SQLite database files in the data of every service
// that Relyper Backup can read: at most 4 directory levels and 5000 entries
// per mount, so a large volume costs little.
func findSQLite(apps []App) {
	for ai := range apps {
		for si := range apps[ai].Services {
			for di := range apps[ai].Services[si].Data {
				d := &apps[ai].Services[si].Data[di]
				if d.BackupPath != "" && !d.DatabaseFiles {
					d.SQLite = sqliteFiles(d.BackupPath)
				}
			}
		}
	}
}

var sqliteHeader = []byte("SQLite format 3\x00")

func sqliteFiles(root string) []string {
	var out []string
	n := 0
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n++; n > 5000 || len(out) >= 20 {
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		if e.IsDir() {
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= 3 {
				return fs.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() || !sqliteFile.MatchString(p) && !strings.HasSuffix(p, ".sqlite") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		head := make([]byte, 16)
		if _, err := f.Read(head); err == nil && bytes.Equal(head, sqliteHeader) {
			out = append(out, rel)
		}
		return nil
	})
	return out
}
