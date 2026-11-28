//go:build docker

// Relyper-owned integration test against a real Docker daemon:
//
//	go test -tags docker ./internal/relyper/dbdump/
//
// It starts PostgreSQL, MariaDB and Redis containers with test data as a
// Compose-labeled app, discovers them, dumps them, verifies the dumps in
// throwaway containers and checks that a damaged dump is detected.
package dbdump

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/discovery"
	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

func run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func startDB(t *testing.T, name, image string, env ...string) {
	args := []string{"run", "-d", "--name", name, "--label", "com.docker.compose.project=relyper-it", "--label", "com.docker.compose.service=" + strings.TrimPrefix(name, "relyper-it-")}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, image)
	run(t, args...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", name).Run() })
}

func waitExec(t *testing.T, name string, script string) {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if exec.Command("docker", "exec", name, "sh", "-c", script).Run() == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s not ready", name)
}

func TestDumpAndVerify(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := docker.New(docker.EndpointFromEnv())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ping(ctx); err != nil {
		t.Skip("no docker daemon:", err)
	}
	startDB(t, "relyper-it-pg", "postgres:16-alpine", "POSTGRES_PASSWORD=pg-secret-123", "POSTGRES_USER=app", "POSTGRES_DB=shop")
	startDB(t, "relyper-it-maria", "mariadb:11", "MARIADB_ROOT_PASSWORD=maria-secret-456", "MARIADB_DATABASE=crm")
	startDB(t, "relyper-it-redis", "redis:7-alpine")
	waitExec(t, "relyper-it-pg", `pg_isready -h 127.0.0.1 -U app`)
	run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "shop", "-c", "create table orders(id int primary key, item text); insert into orders select g, 'item '||g from generate_series(1,500) g; create table customers(id int);")
	run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "postgres", "-c", "create database reports")
	run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "reports", "-c", "create table daily(d date)")
	waitExec(t, "relyper-it-maria", `mariadb -uroot -pmaria-secret-456 --protocol=tcp -h 127.0.0.1 -e "select 1"`)
	run(t, "exec", "relyper-it-maria", "mariadb", "-uroot", "-pmaria-secret-456", "crm", "-e", "create table leads(id int primary key, name varchar(50)); insert into leads values (1,'a'),(2,'b'); create table notes(id int);")
	run(t, "exec", "relyper-it-maria", "mariadb", "-uroot", "-pmaria-secret-456", "-e", "create database billing; create table billing.invoices(id int); insert into billing.invoices values (1),(2),(3);")
	waitExec(t, "relyper-it-redis", `redis-cli ping`)
	for i := 0; i < 50; i++ {
		run(t, "exec", "relyper-it-redis", "redis-cli", "set", "key"+string(rune('a'+i%26))+string(rune('a'+i/26)), "v")
	}

	ctrs, err := c.ListContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apps, _ := discovery.Build(ctrs, nil, discovery.Options{})
	inv := &discovery.Inventory{Apps: apps}
	app := inv.Find("compose:relyper-it")
	if app == nil || len(app.Services) != 3 {
		t.Fatalf("app not discovered: %+v", app)
	}
	for _, s := range app.Services {
		if s.Database == nil {
			t.Fatalf("%s: database not detected (%s)", s.Name, s.Image)
		}
	}

	base := t.TempDir()
	var log bytes.Buffer
	m, err := DumpApp(ctx, c, app, base, &log)
	if err != nil {
		t.Fatalf("dump: %v\n%s", err, log.String())
	}
	t.Log(log.String())
	want := map[string]map[string]int64{
		"pg":    {"shop": 2, "reports": 1, "postgres": 0},
		"maria": {"crm": 2, "billing": 1},
		"redis": {"db0": 50},
	}
	for _, d := range m.Dumps {
		for k, v := range want[d.Service] {
			if d.Stats[k] != v {
				t.Errorf("%s stats %s = %d, want %d (%v)", d.Service, k, d.Stats[k], v, d.Stats)
			}
		}
	}
	dir := Dir(base, app.ID)
	manifest, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	// No secret ends up in dumps directory metadata or the log.
	for _, secret := range []string{"pg-secret-123", "maria-secret-456"} {
		if strings.Contains(string(manifest), secret) || strings.Contains(log.String(), secret) {
			t.Fatalf("secret %s leaked into manifest or log", secret)
		}
	}

	for _, d := range m.Dumps {
		r, err := Verify(ctx, c, d, filepath.Join(dir, d.File), &log)
		if err != nil {
			t.Fatalf("verify %s: %v\n%s", d.Service, err, log.String())
		}
		if !r.OK {
			t.Fatalf("verify %s failed: %v (restored %v)", d.Service, r.Problems, r.Restored)
		}
	}

	// A damaged dump is detected (checksum), and a dump with missing data
	// is detected by the content comparison.
	pg := m.Dumps[0]
	for _, d := range m.Dumps {
		if d.Engine == discovery.EnginePostgres {
			pg = d
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, pg.File))
	damaged := filepath.Join(t.TempDir(), "damaged.sql")
	_ = os.WriteFile(damaged, append(data, '\n', '-', '-'), 0o600)
	r, err := Verify(ctx, c, pg, damaged, nil)
	if err != nil || r.OK {
		t.Fatalf("damaged dump not detected: %v %+v", err, r)
	}
	truncated := bytes.Split(data, []byte("CREATE TABLE public.customers"))[0]
	pg.SHA256 = ""
	incomplete := filepath.Join(t.TempDir(), "incomplete.sql")
	_ = os.WriteFile(incomplete, truncated, 0o600)
	r, err = Verify(ctx, c, pg, incomplete, nil)
	if err != nil || r.OK {
		t.Fatalf("incomplete dump not detected: %v %+v", err, r)
	}
	t.Logf("incomplete dump problems: %v", r.Problems)

	left, err := c.ListVerifyContainers(ctx)
	if err != nil || len(left) != 0 {
		t.Fatalf("verification containers left behind: %v %v", len(left), err)
	}

	// The restore script next to the dumps brings lost data back without
	// Relyper Backup. Drop data in all three databases, then run it.
	script, err := os.ReadFile(filepath.Join(dir, RestoreScriptName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"pg-secret-123", "maria-secret-456"} {
		if strings.Contains(string(script), secret) {
			t.Fatal("secret in restore script")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, RestoreNotesName)); err != nil {
		t.Fatal(err)
	}
	run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "shop", "-c", "drop table orders")
	run(t, "exec", "relyper-it-maria", "mariadb", "-uroot", "-pmaria-secret-456", "crm", "-e", "drop table leads")
	run(t, "exec", "relyper-it-redis", "redis-cli", "flushall")
	out, err := exec.Command("sh", filepath.Join(dir, RestoreScriptName), "--yes").CombinedOutput()
	if err != nil {
		t.Fatalf("restore script: %v\n%s", err, out)
	}
	t.Logf("restore script: %s", out)
	if n := run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "shop", "-At", "-c", "select count(*) from orders"); n != "500" {
		t.Fatalf("orders after restore: %q", n)
	}
	if n := run(t, "exec", "relyper-it-maria", "mariadb", "-uroot", "-pmaria-secret-456", "crm", "-N", "-B", "-e", "select count(*) from leads"); n != "2" {
		t.Fatalf("leads after restore: %q", n)
	}
	waitExec(t, "relyper-it-redis", `redis-cli ping`)
	if n := run(t, "exec", "relyper-it-redis", "redis-cli", "dbsize"); n != "50" {
		t.Fatalf("redis keys after restore: %q", n)
	}

	// Shared instances: every database is also dumped on its own, and the
	// script restores one database without touching the others.
	for _, d := range m.Dumps {
		switch d.Service {
		case "pg", "maria":
			if len(d.Databases) != 2 {
				t.Fatalf("%s: per-database dumps %+v", d.Service, d.Databases)
			}
		case "redis":
			if len(d.Databases) != 0 {
				t.Fatalf("redis has no per-database dumps: %+v", d.Databases)
			}
		}
	}
	run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "shop", "-c", "delete from orders where id > 100")
	run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "reports", "-c", "create table kept(id int)")
	run(t, "exec", "relyper-it-maria", "mariadb", "-uroot", "-pmaria-secret-456", "-e", "delete from billing.invoices; create table crm.kept(id int);")
	for _, only := range []string{"pg/shop", "maria/billing"} {
		out, err = exec.Command("sh", filepath.Join(dir, RestoreScriptName), "--yes", "--database="+only).CombinedOutput()
		if err != nil {
			t.Fatalf("restore %s: %v\n%s", only, err, out)
		}
		t.Logf("restore %s: %s", only, out)
	}
	if n := run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "shop", "-At", "-c", "select count(*) from orders"); n != "500" {
		t.Fatalf("orders after single-database restore: %q", n)
	}
	if n := run(t, "exec", "relyper-it-pg", "psql", "-U", "app", "-d", "reports", "-At", "-c", "select count(*) from information_schema.tables where table_name='kept'"); n != "1" {
		t.Fatalf("the other database was touched: %q", n)
	}
	if n := run(t, "exec", "relyper-it-maria", "mariadb", "-uroot", "-pmaria-secret-456", "-N", "-B", "-e", "select count(*) from billing.invoices"); n != "3" {
		t.Fatalf("invoices after single-database restore: %q", n)
	}
	if n := run(t, "exec", "relyper-it-maria", "mariadb", "-uroot", "-pmaria-secret-456", "-N", "-B", "-e", "select count(*) from information_schema.tables where table_name='kept'"); n != "1" {
		t.Fatalf("the other mariadb database was touched: %q", n)
	}
	if out, err := exec.Command("sh", filepath.Join(dir, RestoreScriptName), "--yes", "--database=pg/nope").CombinedOutput(); err == nil {
		t.Fatalf("unknown database accepted: %s", out)
	}
}

func TestDumpAndVerifyMySQLMongo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := docker.New(docker.EndpointFromEnv())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ping(ctx); err != nil {
		t.Skip("no docker daemon:", err)
	}
	startDB(t, "relyper-it-mysql", "mysql:8.4", "MYSQL_ROOT_PASSWORD=mysql-secret-789", "MYSQL_DATABASE=inventory")
	startDB(t, "relyper-it-mongo", "mongo:7", "MONGO_INITDB_ROOT_USERNAME=root", "MONGO_INITDB_ROOT_PASSWORD=mongo-secret-012")
	waitExec(t, "relyper-it-mysql", `mysql -uroot -pmysql-secret-789 --protocol=tcp -h 127.0.0.1 -e "select 1"`)
	run(t, "exec", "relyper-it-mysql", "mysql", "-uroot", "-pmysql-secret-789", "inventory", "-e", "create table items(id int primary key); insert into items values (1),(2),(3); create table places(id int);")
	waitExec(t, "relyper-it-mongo", `mongosh --quiet -u root -p mongo-secret-012 --eval 'db.runCommand({ping:1}).ok'`)
	run(t, "exec", "relyper-it-mongo", "mongosh", "--quiet", "-u", "root", "-p", "mongo-secret-012", "--eval", `db.getSiblingDB("app").users.insertMany([{a:1},{a:2}]); db.getSiblingDB("app").logs.insertOne({x:1}); db.getSiblingDB("other").things.insertOne({y:1})`)

	ctrs, err := c.ListContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apps, _ := discovery.Build(ctrs, nil, discovery.Options{})
	app := (&discovery.Inventory{Apps: apps}).Find("compose:relyper-it")
	if app == nil {
		t.Fatal("app not found")
	}
	base := t.TempDir()
	var log bytes.Buffer
	m, err := DumpApp(ctx, c, app, base, &log)
	if err != nil {
		t.Fatalf("dump: %v\n%s", err, log.String())
	}
	for _, d := range m.Dumps {
		t.Logf("%s stats %v", d.Service, d.Stats)
		if (d.Engine == discovery.EngineMySQL && d.Stats["inventory"] != 2) || (d.Engine == discovery.EngineMongo && d.Stats["app"] != 2) {
			t.Fatalf("unexpected stats for %s: %v", d.Service, d.Stats)
		}
		r, err := Verify(ctx, c, d, filepath.Join(Dir(base, app.ID), d.File), &log)
		if err != nil || !r.OK {
			t.Fatalf("verify %s: %v %+v\n%s", d.Service, err, r, log.String())
		}
	}
	if strings.Contains(log.String(), "secret") {
		t.Fatal("secret in log")
	}

	// Restore script for MySQL and MongoDB.
	run(t, "exec", "relyper-it-mysql", "mysql", "-uroot", "-pmysql-secret-789", "inventory", "-e", "drop table items")
	run(t, "exec", "relyper-it-mongo", "mongosh", "--quiet", "-u", "root", "-p", "mongo-secret-012", "--eval", `db.getSiblingDB("app").users.drop()`)
	out, err := exec.Command("sh", filepath.Join(Dir(base, app.ID), RestoreScriptName), "--yes").CombinedOutput()
	if err != nil {
		t.Fatalf("restore script: %v\n%s", err, out)
	}
	if n := run(t, "exec", "relyper-it-mysql", "mysql", "-uroot", "-pmysql-secret-789", "inventory", "-N", "-B", "-e", "select count(*) from items"); !strings.HasSuffix(n, "3") {
		t.Fatalf("items after restore: %q", n)
	}
	if n := run(t, "exec", "relyper-it-mongo", "mongosh", "--quiet", "-u", "root", "-p", "mongo-secret-012", "--eval", `db.getSiblingDB("app").users.countDocuments()`); n != "2" {
		t.Fatalf("users after restore: %q", n)
	}
	// One MongoDB database on its own; the other stays untouched.
	run(t, "exec", "relyper-it-mongo", "mongosh", "--quiet", "-u", "root", "-p", "mongo-secret-012", "--eval", `db.getSiblingDB("app").users.drop(); db.getSiblingDB("other").things.insertOne({y:2})`)
	out, err = exec.Command("sh", filepath.Join(Dir(base, app.ID), RestoreScriptName), "--yes", "--database=mongo/app").CombinedOutput()
	if err != nil {
		t.Fatalf("single-database restore: %v\n%s", err, out)
	}
	if n := run(t, "exec", "relyper-it-mongo", "mongosh", "--quiet", "-u", "root", "-p", "mongo-secret-012", "--eval", `db.getSiblingDB("app").users.countDocuments()`); n != "2" {
		t.Fatalf("users after single-database restore: %q", n)
	}
	if n := run(t, "exec", "relyper-it-mongo", "mongosh", "--quiet", "-u", "root", "-p", "mongo-secret-012", "--eval", `db.getSiblingDB("other").things.countDocuments()`); n != "2" {
		t.Fatalf("the other database was touched: %q", n)
	}
}
