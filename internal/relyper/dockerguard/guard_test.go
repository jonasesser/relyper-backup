// Relyper-owned tests of the Docker guard against a fake Docker daemon. The
// end-to-end test with real databases is internal/relyper/dbdump's
// docker-tagged test run with RELYPER_GUARD_URL pointing at a guard.
package dockerguard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

type fakeDocker struct {
	mu       sync.Mutex
	requests []string          // "METHOD path?query"
	bodies   map[string][]byte // last body per "METHOD path"
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	key := r.Method + " " + versionPrefix.ReplaceAllString(r.URL.Path, "/")
	f.requests = append(f.requests, key+"?"+r.URL.RawQuery)
	f.bodies[key] = body
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	inspect := func(name, image string, running bool, labels map[string]string) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id": name + "-id", "Name": "/" + name,
			"State":  map[string]any{"Running": running, "Status": "running"},
			"Config": map[string]any{"Image": image, "Env": []string{"POSTGRES_PASSWORD=very-secret", "PATH=/bin"}, "Labels": labels},
		})
	}
	switch key {
	case "GET /containers/pg/json":
		inspect("pg", "postgres:16-alpine", true, map[string]string{})
	case "GET /containers/web/json":
		inspect("web", "nginx:latest", true, map[string]string{})
	case "GET /containers/relyper-verify-0123456789ab/json", "GET /containers/v1/json":
		inspect("relyper-verify-0123456789ab", "postgres:16-alpine", true, map[string]string{"relyper.verify": "true"})
	case "GET /containers/json":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "pg-id", "Image": "postgres:16-alpine", "State": "running", "Labels": map[string]string{}},
			{"Id": "web-id", "Image": "nginx:latest", "State": "running", "Labels": map[string]string{}},
		})
	case "GET /services":
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Spec": map[string]any{"TaskTemplate": map[string]any{"ContainerSpec": map[string]any{"Env": []string{"API_KEY=abc"}}}}}})
	case "POST /containers/pg/exec", "POST /containers/v1/exec":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"exec1"}`))
	case "POST /exec/exec1/start":
		_, _ = w.Write([]byte("stream"))
	case "POST /containers/create":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"v1"}`))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func setup(t *testing.T, cfg Config) (*httptest.Server, *fakeDocker) {
	t.Helper()
	f := &fakeDocker{bodies: map[string][]byte{}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	cfg.Upstream = "tcp://" + strings.TrimPrefix(up.URL, "http://")
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return srv, f
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any, hdr ...string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func pgPolicy(t *testing.T) dbdump.ExecPolicy {
	p, ok := dbdump.Policy(discovery.EnginePostgres)
	if !ok {
		t.Fatal("no postgres policy")
	}
	return p
}

func TestReadsAreRedacted(t *testing.T) {
	srv, _ := setup(t, Config{})
	for _, path := range []string{"/containers/pg/json", "/v1.44/containers/pg/json", "/services"} {
		code, body := do(t, srv, "GET", path, nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		if strings.Contains(body, "very-secret") || strings.Contains(body, "abc") || strings.Contains(body, "=") {
			t.Fatalf("%s: env values not removed: %s", path, body)
		}
	}
	if code, body := do(t, srv, "GET", "/relyper/guard", nil); code != 200 || !strings.Contains(body, "relyper-docker-guard") {
		t.Fatalf("guard info: %d %s", code, body)
	}
}

func TestBlockedEndpoints(t *testing.T) {
	srv, f := setup(t, Config{Dumps: true, Verify: true})
	cases := []struct{ method, path string }{
		{"POST", "/images/create?fromImage=alpine"},
		{"POST", "/build"},
		{"POST", "/containers/pg/attach"},
		{"POST", "/containers/pg/update"},
		{"POST", "/containers/pg/kill"},
		{"POST", "/containers/pg/stop"}, // lifecycle disabled
		{"GET", "/containers/pg/archive?path=/etc"},
		{"PUT", "/containers/pg/archive?path=/tmp/relyper-restore"},
		{"DELETE", "/containers/pg"},
		{"POST", "/containers/pg/start"},
		{"GET", "/secrets"},
		{"POST", "/networks/create"},
		{"POST", "/swarm/init"},
		{"GET", "/containers/x/../../images/json"},
		{"POST", "/v1.44/session"},
		{"POST", "/exec/exec1/start"}, // not created by the guard
		{"GET", "/exec/exec1/json"},
	}
	for _, c := range cases {
		if code, body := do(t, srv, c.method, c.path, nil); code != http.StatusForbidden {
			t.Errorf("%s %s: got %d %s, want 403", c.method, c.path, code, body)
		}
	}
	for _, r := range f.requests {
		if !strings.HasPrefix(r, "GET /containers/") {
			t.Errorf("forwarded to docker: %s", r)
		}
	}
}

func TestToken(t *testing.T) {
	srv, _ := setup(t, Config{Token: "s3cret"})
	if code, _ := do(t, srv, "GET", "/version", nil); code != http.StatusUnauthorized {
		t.Fatalf("without token: %d", code)
	}
	if code, _ := do(t, srv, "GET", "/version", nil, "Authorization", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := do(t, srv, "GET", "/version", nil, "Authorization", "Bearer s3cret"); code >= 300 {
		t.Fatalf("right token: %d", code)
	}
}

func execBody(script string, env ...string) map[string]any {
	b := map[string]any{"AttachStdout": true, "AttachStderr": true, "Cmd": []string{"sh", "-c", script}}
	if env != nil {
		b["Env"] = env
	}
	return b
}

func TestDumpExec(t *testing.T) {
	p := pgPolicy(t)
	t.Run("disabled", func(t *testing.T) {
		srv, _ := setup(t, Config{})
		if code, _ := do(t, srv, "POST", "/containers/pg/exec", execBody(p.Source[1])); code != 403 {
			t.Fatalf("dump with dumps disabled: %d", code)
		}
	})
	srv, f := setup(t, Config{Dumps: true})
	bad := []map[string]any{
		execBody("id"),
		execBody(p.Source[1] + "; id"),
		execBody(p.Source[1], "PGHOST=evil"),
		execBody(p.Verify[0]), // restore-test script in a production container
		{"Cmd": []string{"pg_dumpall"}, "AttachStdout": true},
		{"Cmd": []string{"sh", "-c", p.Source[1]}, "User": "root"},
		{"Cmd": []string{"sh", "-c", p.Source[1]}, "Privileged": true},
		{"Cmd": []string{"sh", "-c", p.Source[1]}, "Tty": true},
	}
	for i, b := range bad {
		if code, body := do(t, srv, "POST", "/containers/pg/exec", b); code != 403 {
			t.Errorf("bad exec %d: %d %s", i, code, body)
		}
	}
	if code, _ := do(t, srv, "POST", "/containers/web/exec", execBody(p.Source[1])); code != 403 {
		t.Errorf("exec in a non-database container: %d", code)
	}
	if code, _ := do(t, srv, "POST", "/containers/relyper-verify-0123456789ab/exec", execBody(p.Source[1])); code != 403 {
		t.Errorf("dump script in a verification container not created by this guard: %d", code)
	}
	for _, s := range p.Source {
		code, body := do(t, srv, "POST", "/containers/pg/exec", execBody(s))
		if code != http.StatusCreated || !strings.Contains(body, "exec1") {
			t.Fatalf("allowed exec: %d %s", code, body)
		}
	}
	var sent map[string]any
	_ = json.Unmarshal(f.bodies["POST /containers/pg/exec"], &sent)
	if _, ok := sent["Env"]; ok || sent["User"] != nil || len(sent) != 3 {
		t.Fatalf("guard forwarded unexpected fields: %v", sent)
	}
	if code, body := do(t, srv, "POST", "/exec/exec1/start", map[string]any{"Detach": false, "Tty": false}); code != 200 || body != "stream" {
		t.Fatalf("exec start: %d %s", code, body)
	}
	if code, _ := do(t, srv, "POST", "/exec/exec1/start", map[string]any{"Detach": false}); code != 403 {
		t.Fatalf("second start of the same exec: %d", code)
	}
	if code, _ := do(t, srv, "GET", "/exec/exec1/json", nil); code == 403 {
		t.Fatal("exec status of a guard exec refused")
	}
}

func TestRequireLabel(t *testing.T) {
	p := pgPolicy(t)
	srv, _ := setup(t, Config{Dumps: true, RequireLabel: true})
	if code, _ := do(t, srv, "POST", "/containers/pg/exec", execBody(p.Source[1])); code != 403 {
		t.Fatalf("dump of an unlabelled container: %d", code)
	}
}

func createBody(image string, env []string, mutate func(map[string]any)) map[string]any {
	b := map[string]any{
		"Image": image, "Env": env,
		"Labels":     map[string]string{"relyper.verify": "true"},
		"HostConfig": map[string]any{"NetworkMode": "none", "AutoRemove": false},
	}
	if mutate != nil {
		mutate(b)
	}
	return b
}

func TestRestoreTestContainers(t *testing.T) {
	pw := strings.Repeat("ab", dbdump.VerifyPasswordLen/2)
	env := []string{"POSTGRES_PASSWORD=" + pw, "POSTGRES_USER=app"}
	name := "/containers/create?name=relyper-verify-0123456789ab"
	t.Run("disabled", func(t *testing.T) {
		srv, _ := setup(t, Config{Dumps: true})
		if code, _ := do(t, srv, "POST", name, createBody("postgres:16-alpine", env, nil)); code != 403 {
			t.Fatalf("create with restore tests disabled: %d", code)
		}
	})
	srv, f := setup(t, Config{Verify: true})
	bad := map[string]struct {
		path string
		body map[string]any
	}{
		"privileged": {name, createBody("postgres:16-alpine", env, func(b map[string]any) {
			b["HostConfig"].(map[string]any)["Privileged"] = true
		})},
		"host mount": {name, createBody("postgres:16-alpine", env, func(b map[string]any) {
			b["HostConfig"].(map[string]any)["Binds"] = []string{"/:/host"}
		})},
		"host network": {name, createBody("postgres:16-alpine", env, func(b map[string]any) {
			b["HostConfig"].(map[string]any)["NetworkMode"] = "host"
		})},
		"command":       {name, createBody("postgres:16-alpine", env, func(b map[string]any) { b["Cmd"] = []string{"sh"} })},
		"entrypoint":    {name, createBody("postgres:16-alpine", env, func(b map[string]any) { b["Entrypoint"] = []string{"sh"} })},
		"other image":   {name, createBody("alpine:latest", nil, nil)},
		"unused image":  {name, createBody("postgres:17", env, nil)},
		"other env":     {name, createBody("postgres:16-alpine", []string{"LD_PRELOAD=/x.so"}, nil)},
		"weak password": {name, createBody("postgres:16-alpine", []string{"POSTGRES_PASSWORD=x"}, nil)},
		"bad user":      {name, createBody("postgres:16-alpine", []string{"POSTGRES_USER=$(id)"}, nil)},
		"other name":    {"/containers/create?name=evil", createBody("postgres:16-alpine", env, nil)},
		"extra label": {name, createBody("postgres:16-alpine", env, func(b map[string]any) {
			b["Labels"] = map[string]string{"relyper.verify": "true", "traefik.enable": "true"}
		})},
	}
	for what, c := range bad {
		if code, body := do(t, srv, "POST", c.path, c.body); code != 403 {
			t.Errorf("%s: %d %s", what, code, body)
		}
	}
	code, body := do(t, srv, "POST", name, createBody("postgres:16-alpine", env, func(b map[string]any) {
		b["HostConfig"].(map[string]any)["Memory"] = 512 << 20
	}))
	if code != http.StatusCreated || !strings.Contains(body, "v1") {
		t.Fatalf("allowed create: %d %s", code, body)
	}
	var sent struct {
		HostConfig map[string]any
		Labels     map[string]string
	}
	_ = json.Unmarshal(f.bodies["POST /containers/create"], &sent)
	if sent.HostConfig["NetworkMode"] != "none" || sent.HostConfig["Privileged"] != false || sent.Labels["relyper.verify"] != "true" {
		t.Fatalf("created with %v", sent)
	}
	p := pgPolicy(t)
	if code, _ := do(t, srv, "POST", "/containers/v1/start", nil); code == 403 {
		t.Fatal("start of the guard's restore-test container refused")
	}
	if code, _ := do(t, srv, "POST", "/containers/v1/exec", execBody("cat /etc/shadow")); code != 403 {
		t.Fatal("arbitrary command in a restore-test container allowed")
	}
	if code, _ := do(t, srv, "POST", "/containers/v1/exec", execBody(p.Verify[0], "MARIADB_ROOT_PASSWORD="+pw)); code != http.StatusCreated {
		t.Fatal("ready script refused")
	}
	if code, _ := do(t, srv, "POST", "/containers/v1/exec", execBody(p.Verify[0], "PATH=/tmp")); code != 403 {
		t.Fatal("arbitrary environment in a restore-test container allowed")
	}
	put := func(dir string) int {
		req, _ := http.NewRequest("PUT", srv.URL+"/containers/v1/archive?path="+dir, strings.NewReader("tar"))
		req.Header.Set("Content-Type", "application/x-tar")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if put("/etc") != 403 || put("/tmp/relyper-restore") == 403 {
		t.Fatal("archive target check")
	}
	if code, _ := do(t, srv, "DELETE", "/containers/web?force=1", nil); code != 403 {
		t.Fatal("removing an app container allowed")
	}
	if code, _ := do(t, srv, "DELETE", "/containers/v1?force=1&v=1", nil); code == 403 {
		t.Fatal("removing the restore-test container refused")
	}
}

func TestLifecycle(t *testing.T) {
	srv, f := setup(t, Config{Lifecycle: true})
	if code, _ := do(t, srv, "POST", "/containers/web/restart?t=30&signal=SIGKILL", nil); code == 403 {
		t.Fatal("restart refused")
	}
	last := f.requests[len(f.requests)-1]
	if last != "POST /containers/web/restart?t=30" {
		t.Fatalf("forwarded %s", last)
	}
}
