//go:build e2e && docker

// End-to-end test of application-aware backup through MCP, against the
// running binary, a real Docker daemon and a local restic repository:
//
//	go build -o /tmp/backrest ./cmd/backrest
//	go build -o /tmp/relyper-dbdump ./cmd/relyper-dbdump
//	RELYPER_E2E_BIN=/tmp/backrest RELYPER_E2E_DBDUMP_BIN=/tmp/relyper-dbdump \
//	  go test -tags 'e2e docker' -run TestE2EInfra -v ./internal/relyper/mcp/
package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestE2EInfra(t *testing.T) {
	dump := os.Getenv("RELYPER_E2E_DBDUMP_BIN")
	if dump == "" || exec.Command("docker", "version").Run() != nil {
		t.Skip("needs RELYPER_E2E_DBDUMP_BIN and a docker daemon")
	}
	t.Setenv("RELYPER_DUMP_COMMAND", dump)
	t.Setenv("RELYPER_VERIFY_INTERVAL_DAYS", "0")

	uploads := filepath.Join(t.TempDir(), "uploads")
	os.MkdirAll(filepath.Join(uploads, "2026"), 0o755)
	os.WriteFile(filepath.Join(uploads, "2026", "invoice.pdf"), []byte(strings.Repeat("pdf", 1000)), 0o644)
	os.WriteFile(filepath.Join(uploads, "logo.png"), []byte("png"), 0o644)

	const pw = "e2e-db-secret-4711"
	const repoPassword = "e2e-infra-repo-password"
	blog := filepath.Join(t.TempDir(), "blog")
	os.MkdirAll(blog, 0o755)
	os.WriteFile(filepath.Join(blog, "post.md"), []byte(strings.Repeat("x", 5000)), 0o644)
	for _, n := range []string{"relyper-e2e-db", "relyper-e2e-web", "relyper-e2e-blog"} {
		exec.Command("docker", "rm", "-f", "-v", n).Run()
	}
	docker(t, "run", "-d", "--name", "relyper-e2e-db", "--label", "com.docker.compose.project=e2eshop", "--label", "com.docker.compose.service=db",
		"-e", "POSTGRES_PASSWORD="+pw, "-e", "POSTGRES_DB=shop", "postgres:16-alpine")
	docker(t, "run", "-d", "--name", "relyper-e2e-web", "--label", "com.docker.compose.project=e2eshop", "--label", "com.docker.compose.service=web",
		"-v", uploads+":/app/uploads", "redis:7-alpine")
	docker(t, "run", "-d", "--name", "relyper-e2e-blog", "--label", "com.docker.compose.project=e2eblog", "--label", "com.docker.compose.service=web",
		"-v", blog+":/srv/blog", "redis:7-alpine")
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", "-v", "relyper-e2e-db", "relyper-e2e-web", "relyper-e2e-blog").Run()
	})
	deadline := time.Now().Add(2 * time.Minute)
	for exec.Command("docker", "exec", "relyper-e2e-db", "pg_isready", "-h", "127.0.0.1").Run() != nil {
		if time.Now().After(deadline) {
			t.Fatal("postgres not ready")
		}
		time.Sleep(time.Second)
	}
	docker(t, "exec", "relyper-e2e-db", "psql", "-U", "postgres", "-d", "shop", "-c", "create table orders(id int); insert into orders values (1),(2);")

	base, dirs := startServer(t)
	token := createToken(t, base, []string{"status:read", "repos:manage", "plans:manage", "backups:run", "infra:manage"})
	s := mcpConnect(t, base, token)

	inv := mustCall(t, s, "discover_apps", map[string]any{"refresh": true})
	if !strings.Contains(toJSON(inv), `"compose:e2eshop"`) {
		t.Fatalf("app not discovered: %s", toJSON(inv))
	}
	cov := mustCall(t, s, "get_backup_coverage", nil)
	if app := findApp(cov, "compose:e2eshop"); app == nil || app["state"] != "unprotected" {
		t.Fatalf("coverage before plan: %v", app)
	}
	prop := mustCall(t, s, "propose_app_backup_plan", map[string]any{"app_id": "compose:e2eshop"})
	t.Logf("proposal: %s", toJSON(prop))

	mustCall(t, s, "create_repository", map[string]any{"id": "local", "uri": filepath.Join(dirs["repo-parent"], "repo"), "password": repoPassword})
	plan := mustCall(t, s, "create_app_backup_plan", map[string]any{"app_id": "compose:e2eshop", "repo_id": "local"})
	planID := plan["plan_id"].(string)
	if !strings.Contains(toJSON(plan["paths"]), uploads) {
		t.Fatalf("uploads not in plan: %v", plan)
	}
	// Setup wizard: the shop has a plan now, the blog (redis, so database
	// schedule) gets one with shared
	// options.
	setup := mustCall(t, s, "propose_backup_setup", map[string]any{"scope": "apps", "app_ids": []string{"compose:e2eshop", "compose:e2eblog"}})
	t.Logf("setup proposal: %s", toJSON(setup))
	for _, a := range setup["apps"].([]any) {
		a := a.(map[string]any)
		size := a["size"].(map[string]any)
		switch a["app"] {
		case "compose:e2eshop":
			if a["existing_plan"] != true || a["has_databases"] != true {
				t.Fatalf("shop proposal: %v", a)
			}
		case "compose:e2eblog":
			if a["existing_plan"] != false || size["bytes"].(float64) < 5000 || size["complete"] != true {
				t.Fatalf("blog proposal: %v", a)
			}
		}
	}
	created := mustCall(t, s, "create_backup_setup", map[string]any{"app_ids": []string{"compose:e2eshop", "compose:e2eblog"}, "repo_id": "local",
		"options": map[string]any{"files_cron": "0 3 * * *", "database_cron": "15 */6 * * *", "daily": 7, "weekly": 4, "monthly": 12,
			"excludes": []string{"*.tmp"}, "limit_upload_kib": 2048, "compression": "max"}})
	results := toJSON(created)
	if !strings.Contains(results, "already exists") || !strings.Contains(results, `"plan_id":"app-e2eblog"`) {
		t.Fatalf("setup results: %s", results)
	}
	bp := toJSON(mustCall(t, s, "get_backup_plan", map[string]any{"plan_id": "app-e2eblog"}))
	for _, want := range []string{"15 */6 * * *", "*.tmp", "--limit-upload=2048", "--compression=max", blog} {
		if !strings.Contains(bp, want) {
			t.Fatalf("plan from setup lacks %q: %s", want, bp)
		}
	}

	op := mustCall(t, s, "run_backup", map[string]any{"plan_id": planID})
	done := waitOp(t, s, op["operation_id"].(float64))
	if done["status"] != "success" {
		t.Fatalf("backup: %s", toJSON(done))
	}

	// Coverage needs the indexed snapshot.
	var app map[string]any
	for i := 0; i < 30; i++ {
		app = findApp(mustCall(t, s, "get_backup_coverage", nil), "compose:e2eshop")
		if app["state"] == "protected" {
			break
		}
		time.Sleep(time.Second)
	}
	if app["state"] != "protected" {
		t.Fatalf("coverage after backup: %s", toJSON(app))
	}

	mustCall(t, s, "verify_app_databases", map[string]any{"app_id": "compose:e2eshop"})
	mustCall(t, s, "run_restore_test", map[string]any{"plan_id": planID})
	var res map[string]any
	for i := 0; i < 120; i++ {
		res = mustCall(t, s, "get_verification_results", map[string]any{"app_id": "compose:e2eshop", "plan_id": planID})
		if res["database_restore_tests"] != nil && res["file_restore_tests"] != nil {
			break
		}
		time.Sleep(time.Second)
	}
	db := res["database_restore_tests"].([]any)[0].(map[string]any)
	files := res["file_restore_tests"].([]any)[0].(map[string]any)
	if db["ok"] != true || files["ok"] != true {
		t.Fatalf("verification: %s", toJSON(res))
	}
	t.Logf("database test: %s", toJSON(db["results"]))
	t.Logf("restore test: %s", toJSON(files["files"]))

	// Tools under the concept's names.
	ctrs := toJSON(mustCall(t, s, "list_docker_containers", nil))
	if !strings.Contains(ctrs, `"restart_policy"`) && !strings.Contains(ctrs, `"networks"`) {
		t.Fatalf("container details missing: %s", ctrs)
	}
	if !strings.Contains(toJSON(mustCall(t, s, "list_detected_databases", nil)), `"engine":"postgres"`) {
		t.Fatal("postgres not listed")
	}
	mustCall(t, s, "list_docker_volumes", nil)
	mustCall(t, s, "list_easypanel_projects", nil)
	mustCall(t, s, "get_disaster_recovery_status", nil)
	if !strings.Contains(toJSON(findApp(mustCall(t, s, "get_backup_coverage", nil), "compose:e2eshop")), `"protection":"fully_protected"`) {
		t.Fatalf("protection after verification: %s", toJSON(findApp(mustCall(t, s, "get_backup_coverage", nil), "compose:e2eshop")))
	}
	dbop := mustCall(t, s, "run_database_backup", map[string]any{"app_id": "compose:e2eshop"})
	if d := waitOp(t, s, dbop["operation_id"].(float64)); d["status"] != "success" {
		t.Fatalf("database backup: %s", toJSON(d))
	}

	// Restarting a container needs an approval.
	started := docker(t, "inspect", "-f", "{{.State.StartedAt}}", "relyper-e2e-web")
	rq := map[string]any{"app_id": "compose:e2eshop", "service": "web"}
	first := mustCall(t, s, "restart_container", rq)
	if first["status"] != "approval_required" {
		t.Fatalf("restart without approval: %v", first)
	}
	if docker(t, "inspect", "-f", "{{.State.StartedAt}}", "relyper-e2e-web") != started {
		t.Fatal("restarted before approval")
	}
	if code, out := rpc(t, base, "/relyper.v1.RelyperMcp/DecideApproval", map[string]any{"id": first["approval_id"], "approve": true}, true, ""); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	rq["approval_id"] = first["approval_id"]
	mustCall(t, s, "restart_container", rq)
	if docker(t, "inspect", "-f", "{{.State.StartedAt}}", "relyper-e2e-web") == started {
		t.Fatal("container not restarted")
	}

	ready := mustCall(t, s, "get_recovery_readiness", nil)
	t.Logf("readiness: %s", ready["level"])
	rep := mustCall(t, s, "get_recovery_report", nil)
	md := rep["markdown"].(string)
	if !strings.Contains(md, "compose:e2eshop") || strings.Contains(md, pw) || strings.Contains(md, repoPassword) {
		t.Fatalf("report: %s", md)
	}

	// The web UI API: report download needs authentication.
	resp, err := http.Get(base + "/relyper/infra/report")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("report without login: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/relyper/infra/coverage", nil)
	req.SetBasicAuth("jonas", e2ePassword)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "compose:e2eshop") {
		t.Fatalf("coverage API: %d %s", resp.StatusCode, b)
	}

	// No secret in the audit log or the data directory's JSON files.
	for _, f := range []string{"relyper/audit.jsonl", "relyper/infra-state.json"} {
		data, _ := os.ReadFile(filepath.Join(dirs["data"], f))
		if strings.Contains(string(data), pw) {
			t.Fatalf("database password in %s", f)
		}
	}
}

func findApp(cov map[string]any, id string) map[string]any {
	apps, _ := cov["apps"].([]any)
	for _, a := range apps {
		if m := a.(map[string]any); m["app"] == id {
			return m
		}
	}
	return nil
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
