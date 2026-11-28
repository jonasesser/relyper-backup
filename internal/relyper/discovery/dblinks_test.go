package discovery

import (
	"strings"
	"testing"
)

func TestDatabaseLinks(t *testing.T) {
	links := databaseLinks([]string{
		"DATABASE_URL=postgres://budget:s3cret@hosting_db:5432/budget?sslmode=disable",
		"REDIS_URL=redis://:pw@hosting_redis:6379/0",
		"MYSQL_HOST=mariadb",
		"SQLITE_PATH=/data/app.sqlite",
		"HOSTNAME=abc123",
		"API_URL=https://example.com",
	})
	got := []string{}
	for _, l := range links {
		got = append(got, l.Var+":"+l.Engine+":"+l.Host+":"+l.Database)
	}
	want := "DATABASE_URL:postgres:hosting_db:budget MYSQL_HOST:mysql:mariadb: REDIS_URL:redis:hosting_redis: SQLITE_PATH:sqlite::/data/app.sqlite"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, l := range links {
		if strings.Contains(l.Host+l.Database, "s3cret") || strings.Contains(l.Host+l.Database, "pw") {
			t.Fatalf("credential leaked: %+v", l)
		}
	}

	apps := map[string]*App{
		"easypanel:hosting_budget": {ID: "easypanel:hosting_budget", Services: []Service{{Name: "budget", DatabaseLinks: links[:1]}}},
		"easypanel:hosting_db":     {ID: "easypanel:hosting_db", Services: []Service{{Name: "db", SwarmService: "hosting_db", Database: &Database{Engine: EnginePostgres}}}},
	}
	resolveDatabaseLinks(apps)
	l := apps["easypanel:hosting_budget"].Services[0].DatabaseLinks[0]
	if l.App != "easypanel:hosting_db" || l.Service != "db" {
		t.Fatalf("not resolved: %+v", l)
	}
}

func TestSafeDatabaseEnv(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"DATABASE_URL", "postgres://budget:s3cret@hosting_db:5432/budget?sslmode=disable&password=x", "postgres://hosting_db:5432/budget"},
		{"REDIS_URL", "redis://:pw@cache:6379/0", "redis://cache:6379"},
		{"DB_HOST", "mariadb", "mariadb"},
		{"SQLITE_PATH", "/data/app.sqlite", "/data/app.sqlite"},
		{"DB_PASSWORD", "s3cret", ""},
		{"SECRET_KEY", "postgres-like", ""},
		{"API_URL", "https://user:pw@example.com", ""},
		{"DB_HOST", "user:pw@host", ""},
	}
	for _, c := range cases {
		got, ok := SafeDatabaseEnv(c.name, c.value)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s=%s: got %q %v, want %q", c.name, c.value, got, ok, c.want)
		}
	}
}
