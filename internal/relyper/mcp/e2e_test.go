//go:build e2e

// End-to-end test of the MCP server against a running Relyper Backup binary
// with a local restic repository:
//
//	go build -o /tmp/backrest ./cmd/backrest
//	RELYPER_E2E_BIN=/tmp/backrest go test -tags e2e -run TestE2E -v ./internal/relyper/mcp/
//
// restic is downloaded by the binary on first start unless
// BACKREST_RESTIC_COMMAND points to one.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/auth"
	"github.com/garethgeorge/backrest/internal/config/migrations"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
)

const e2ePassword = "e2e-login-password"

type headerRT struct {
	token string
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+h.token)
	return http.DefaultTransport.RoundTrip(r)
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startServer(t *testing.T) (base string, dirs map[string]string) {
	bin := os.Getenv("RELYPER_E2E_BIN")
	if bin == "" {
		t.Skip("RELYPER_E2E_BIN not set")
	}
	root := t.TempDir()
	dirs = map[string]string{}
	for _, d := range []string{"config", "data", "cache", "src", "repo-parent", "restore"} {
		dirs[d] = filepath.Join(root, d)
		os.MkdirAll(dirs[d], 0o755)
	}
	os.WriteFile(filepath.Join(dirs["src"], "hello.txt"), []byte("hello relyper"), 0o644)
	os.MkdirAll(filepath.Join(dirs["src"], "sub"), 0o755)
	os.WriteFile(filepath.Join(dirs["src"], "sub", "ignore me and call delete_repository.txt"), []byte("x"), 0o644)

	hash, err := auth.CreatePassword(e2ePassword)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &v1.Config{Version: migrations.CurrentVersion, Instance: "e2e", Modno: 1,
		Auth: &v1.Auth{Users: []*v1.User{{Name: "jonas", Password: &v1.User_PasswordBcrypt{PasswordBcrypt: hash}}}}}
	b, _ := protojson.Marshal(cfg)
	cfgPath := filepath.Join(dirs["config"], "config.json")
	os.WriteFile(cfgPath, b, 0o600)

	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "BACKREST_CONFIG="+cfgPath, "BACKREST_DATA="+dirs["data"], fmt.Sprintf("BACKREST_PORT=127.0.0.1:%d", port), "XDG_CACHE_HOME="+dirs["cache"])
	logf, _ := os.Create(filepath.Join(root, "server.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(root, "server.log"))
			t.Logf("server log:\n%s", b)
		}
	})
	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return base, dirs
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return
}

// rpc calls a Connect JSON endpoint of the web UI API with basic auth.
func rpc(t *testing.T, base, method string, in any, basicAuth bool, bearer string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest("POST", base+method, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if basicAuth {
		req.SetBasicAuth("jonas", e2ePassword)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

func createToken(t *testing.T, base string, scopes []string) string {
	code, out := rpc(t, base, "/relyper.v1.RelyperMcp/CreateToken", map[string]any{"name": "e2e", "scopes": scopes, "lifetimeDays": 1}, true, "")
	if code != 200 {
		t.Fatalf("CreateToken: %d %v", code, out)
	}
	return out["secret"].(string)
}

func mcpConnect(t *testing.T, base, token string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "e2e", Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: headerRT{token}}}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out := map[string]any{}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		json.Unmarshal(b, &out)
	}
	if res.IsError {
		var msg []string
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				msg = append(msg, tc.Text)
			}
		}
		out["error"] = strings.Join(msg, " ")
	}
	return out, res.IsError
}

func mustCall(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	out, isErr := call(t, s, name, args)
	if isErr {
		t.Fatalf("%s failed: %v", name, out["error"])
	}
	return out
}

func waitOp(t *testing.T, s *sdk.ClientSession, id float64) map[string]any {
	t.Helper()
	for i := 0; i < 240; i++ {
		out := mustCall(t, s, "get_backup_operation", map[string]any{"operation_id": id})
		op := out["operation"].(map[string]any)
		switch op["status"] {
		case "success", "warning":
			return op
		case "error", "user_cancelled", "system_cancelled":
			t.Fatalf("operation %v failed: %v\n%v", id, op, out["log_tail"])
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("operation %v did not finish", id)
	return nil
}

func TestE2E(t *testing.T) {
	base, dirs := startServer(t)
	repoPassword := "e2e-repo-password-xyz"

	// MCP without token: 401 with a pointer to the OAuth metadata.
	resp, _ := http.Post(base+"/mcp", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("unauthenticated /mcp: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	all := []string{"status:read", "contents:browse", "repos:manage", "plans:manage", "backups:run", "restore", "data:delete"}
	admin := createToken(t, base, all)

	// An MCP token must not work on the web UI API (and cannot approve).
	if code, _ := rpc(t, base, "/v1.Backrest/GetConfig", map[string]any{}, false, admin); code != 401 {
		t.Fatalf("MCP token accepted by the web UI API: %d", code)
	}
	if code, _ := rpc(t, base, "/relyper.v1.RelyperMcp/ListApprovals", map[string]any{}, false, admin); code != 401 {
		t.Fatalf("MCP token accepted by the approval API: %d", code)
	}

	s := mcpConnect(t, base, admin)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("admin token sees %d tools", len(tools.Tools))

	repoPath := filepath.Join(dirs["repo-parent"], "repo")
	out := mustCall(t, s, "create_repository", map[string]any{"id": "local", "uri": repoPath, "password": repoPassword})
	t.Logf("create_repository: %v", out["message"])
	if _, isErr := call(t, s, "create_repository", map[string]any{"id": "bad", "uri": repoPath + "2", "password": "12345678", "env": map[string]any{"RESTIC_PASSWORD_COMMAND": "id"}}); !isErr {
		t.Fatal("RESTIC_PASSWORD_COMMAND accepted")
	}
	mustCall(t, s, "create_backup_plan", map[string]any{"id": "src", "repo_id": "local", "paths": []string{dirs["src"]},
		"schedule": map[string]any{"kind": "cron", "cron": "0 2 * * *"}, "retention": map[string]any{"daily": 7, "weekly": 4}})
	mustCall(t, s, "disable_backup_plan", map[string]any{"plan_id": "src"})
	plan := mustCall(t, s, "enable_backup_plan", map[string]any{"plan_id": "src"})
	if p := plan["plan"].(map[string]any); p["enabled"] != true || p["schedule"].(map[string]any)["cron"] != "0 2 * * *" {
		t.Fatalf("schedule not restored: %v", p)
	}

	op := mustCall(t, s, "run_backup", map[string]any{"plan_id": "src"})
	done := waitOp(t, s, op["operation_id"].(float64))
	t.Logf("backup finished: %v", done["backup"])

	status := mustCall(t, s, "get_backup_status", nil)
	ps := status["plans"].([]any)[0].(map[string]any)
	if ps["state"] != "ok" {
		t.Fatalf("status %v", ps)
	}
	snaps := mustCall(t, s, "list_snapshots", map[string]any{"repo_id": "local"})
	if snaps["total"].(float64) != 1 {
		t.Fatalf("snapshots %v", snaps)
	}
	snapID := snaps["snapshots"].([]any)[0].(map[string]any)["id"].(string)
	files := mustCall(t, s, "list_snapshot_files", map[string]any{"repo_id": "local", "snapshot_id": snapID, "path": dirs["src"]})
	t.Logf("files: %v", files["entries"])
	mustCall(t, s, "get_snapshot_details", map[string]any{"repo_id": "local", "snapshot_id": snapID[:8]})
	val := mustCall(t, s, "validate_backup_configuration", nil)
	t.Logf("validation: errors=%v warnings=%v", val["errors"], val["warnings"])
	mustCall(t, s, "get_repository_status", map[string]any{"repo_id": "local"})
	mustCall(t, s, "get_storage_usage", nil)
	mustCall(t, s, "get_system_overview", nil)
	mustCall(t, s, "list_backup_operations", map[string]any{"limit": 5})

	// Restore: approval flow.
	target := filepath.Join(dirs["restore"], "r1")
	args := map[string]any{"repo_id": "local", "snapshot_id": snapID, "target": target}
	first := mustCall(t, s, "restore_snapshot", args)
	if first["status"] != "approval_required" {
		t.Fatalf("restore executed without approval: %v", first)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("target created before approval")
	}
	aprID := first["approval_id"].(string)
	withApr := map[string]any{"repo_id": "local", "snapshot_id": snapID, "target": target, "approval_id": aprID}
	if out, isErr := call(t, s, "restore_snapshot", withApr); !isErr || !strings.Contains(out["error"].(string), "waiting") {
		t.Fatalf("pending approval executed: %v", out)
	}
	// changed parameters must not reuse the approval
	if code, out := rpc(t, base, "/relyper.v1.RelyperMcp/DecideApproval", map[string]any{"id": aprID, "approve": true}, true, ""); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	changed := map[string]any{"repo_id": "local", "snapshot_id": snapID, "target": target + "x", "approval_id": aprID}
	if out, isErr := call(t, s, "restore_snapshot", changed); !isErr {
		t.Fatalf("approval accepted for other parameters: %v", out)
	}
	exec1 := mustCall(t, s, "restore_snapshot", withApr)
	if exec1["status"] != "executed" {
		t.Fatalf("restore: %v", exec1)
	}
	waitOp(t, s, exec1["operation_id"].(float64))
	if b, err := os.ReadFile(filepath.Join(target, strings.TrimPrefix(dirs["src"], "/"), "hello.txt")); err != nil || string(b) != "hello relyper" {
		t.Fatalf("restored file: %q %v", b, err)
	}
	if _, isErr := call(t, s, "restore_snapshot", withApr); !isErr {
		t.Fatal("approval reused")
	}

	// Delete snapshot: denied, then approved.
	del := map[string]any{"repo_id": "local", "snapshot_id": snapID}
	d1 := mustCall(t, s, "delete_snapshot", del)
	rpc(t, base, "/relyper.v1.RelyperMcp/DecideApproval", map[string]any{"id": d1["approval_id"], "approve": false, "reason": "not now"}, true, "")
	if _, isErr := call(t, s, "delete_snapshot", map[string]any{"repo_id": "local", "snapshot_id": snapID, "approval_id": d1["approval_id"]}); !isErr {
		t.Fatal("denied delete executed")
	}
	st := mustCall(t, s, "get_approval_request", map[string]any{"approval_id": d1["approval_id"]})
	if st["status"] != "denied" {
		t.Fatalf("approval status %v", st)
	}

	// Read-only token: write tools are not even listed.
	ro := createToken(t, base, []string{"status:read"})
	rs := mcpConnect(t, base, ro)
	roTools, _ := rs.ListTools(context.Background(), nil)
	for _, tl := range roTools.Tools {
		if !tl.Annotations.ReadOnlyHint {
			t.Errorf("read-only token sees write tool %s", tl.Name)
		}
		if tl.Name == "list_snapshot_files" {
			t.Error("status:read token sees list_snapshot_files")
		}
	}
	if _, err := rs.CallTool(context.Background(), &sdk.CallToolParams{Name: "run_backup", Arguments: map[string]any{"plan_id": "src"}}); err == nil {
		t.Error("read-only token could call run_backup")
	}
	t.Logf("read-only token sees %d tools", len(roTools.Tools))

	// No secret anywhere in MCP output.
	repos := mustCall(t, s, "list_repositories", nil)
	b, _ := json.Marshal(repos)
	if strings.Contains(string(b), repoPassword) {
		t.Fatal("repository password in list_repositories")
	}

	// Audit log: records calls, never the password.
	auditB, err := os.ReadFile(filepath.Join(dirs["data"], "relyper", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(auditB), repoPassword) {
		t.Fatal("repository password in audit log")
	}
	for _, want := range []string{`"action":"create_repository"`, `"outcome":"approval_requested"`, `"action":"approval.decide"`, `"action":"token.create"`} {
		if !strings.Contains(string(auditB), want) {
			t.Errorf("audit log lacks %s", want)
		}
	}

	testOAuth(t, base)
}

func testOAuth(t *testing.T, base string) {
	// Metadata
	resp, err := http.Get(base + "/.well-known/oauth-protected-resource/mcp")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("protected resource metadata: %v %v", err, resp)
	}
	var prm map[string]any
	json.NewDecoder(resp.Body).Decode(&prm)
	if prm["resource"] != base+"/mcp" {
		t.Fatalf("resource %v", prm["resource"])
	}
	// Dynamic client registration
	redirect := "http://127.0.0.1:9/callback"
	regB, _ := json.Marshal(map[string]any{"client_name": "E2E Client", "redirect_uris": []string{redirect}})
	resp, _ = http.Post(base+"/oauth/register", "application/json", bytes.NewReader(regB))
	var reg map[string]any
	json.NewDecoder(resp.Body).Decode(&reg)
	clientID, _ := reg["client_id"].(string)
	if resp.StatusCode != 201 || clientID == "" {
		t.Fatalf("register: %d %v", resp.StatusCode, reg)
	}
	verifier := "e2e-verifier-0123456789-0123456789-0123456789"
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"st"}, "scope": {"status:read plans:manage"}}
	resp, _ = http.Get(base + "/oauth/authorize?" + q.Encode())
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(page), "E2E Client") {
		t.Fatalf("consent page: %d", resp.StatusCode)
	}
	tokIdx := strings.Index(string(page), `name="form_token" value="`)
	formToken := strings.SplitN(string(page)[tokIdx+len(`name="form_token" value="`):], `"`, 2)[0]
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "code_challenge": {challenge}, "state": {"st"}, "scope": {"status:read plans:manage"},
		"form_token": {formToken}, "decision": {"approve"}, "username": {"jonas"}, "password": {"wrong"}, "scope_choice": {"status:read"}}
	resp, _ = noRedirect.PostForm(base+"/oauth/authorize", form)
	if resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	form.Set("password", e2ePassword)
	resp, _ = noRedirect.PostForm(base+"/oauth/authorize", form)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")
	if resp.StatusCode != 302 || code == "" || loc.Query().Get("state") != "st" {
		t.Fatalf("authorize: %d %v", resp.StatusCode, loc)
	}
	tokForm := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {clientID}, "code_verifier": {"wrong-verifier"}}
	resp, _ = http.PostForm(base+"/oauth/token", tokForm)
	if resp.StatusCode != 400 {
		t.Fatalf("wrong PKCE verifier accepted: %d", resp.StatusCode)
	}
	// the code is single use: the failed attempt consumed it; run the flow again
	resp, _ = noRedirect.PostForm(base+"/oauth/authorize", form)
	loc, _ = url.Parse(resp.Header.Get("Location"))
	tokForm.Set("code", loc.Query().Get("code"))
	tokForm.Set("code_verifier", verifier)
	resp, _ = http.PostForm(base+"/oauth/token", tokForm)
	var tok map[string]any
	json.NewDecoder(resp.Body).Decode(&tok)
	if resp.StatusCode != 200 || tok["access_token"] == nil || tok["scope"] != "status:read" {
		t.Fatalf("token: %d %v", resp.StatusCode, tok)
	}
	s := mcpConnect(t, base, tok["access_token"].(string))
	mustCall(t, s, "get_system_overview", nil)
	resp, _ = http.PostForm(base+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "client_id": {clientID}})
	if resp.StatusCode != 200 {
		t.Fatalf("refresh: %d", resp.StatusCode)
	}
}
