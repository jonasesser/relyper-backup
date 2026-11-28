// Package dockerguard is Relyper-owned: a purpose-built gateway between
// Relyper Backup and the Docker daemon.
//
// Access to the Docker socket is root on the host: whoever can create a
// container can mount the host's root filesystem into it. A generic
// filtering proxy that allows "exec" and "create" does not change that. The
// guard runs in its own small container, holds the socket and allows only
// the exact calls Relyper Backup makes:
//
//   - reading containers, volumes, Swarm services and system info, with the
//     values of environment variables removed;
//   - database dumps: exec of the fixed dump and statistics scripts of the
//     dbdump package in running containers of a detected database image,
//     without extra environment variables and as the container's own user;
//   - restore tests: creating containers from the image of a running
//     database, with no network, no mounts, no privileges, only random
//     verification passwords as environment, labelled relyper.verify=true;
//     starting them, copying a dump into them, running the fixed restore
//     scripts in them and removing them;
//   - optionally stopping and restarting containers (MCP, after approval).
//
// Everything else is refused with 403, including every request whose body
// has fields the guard does not know. The guard rebuilds request bodies
// itself instead of forwarding what it received.
package dockerguard

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

// Feature names, also reported on GET /relyper/guard.
const (
	FeatureDumps     = "dumps"
	FeatureVerify    = "restore_tests"
	FeatureLifecycle = "stop_restart"
)

// DumpLabel marks containers that may be dumped when RequireLabel is set.
const DumpLabel = "relyper.dump"

// Config configures a guard.
type Config struct {
	// Upstream is the Docker endpoint the guard talks to, normally
	// unix:///var/run/docker.sock.
	Upstream string
	// Token, when set, must be sent as "Authorization: Bearer <token>".
	Token string
	// Dumps allows the dump scripts in database containers.
	Dumps bool
	// Verify allows restore-test containers.
	Verify bool
	// Lifecycle allows stopping and restarting containers.
	Lifecycle bool
	// RequireLabel limits dumps and restore-test images to containers
	// labelled relyper.dump=true.
	RequireLabel bool
	// Log receives one line per write operation and per refusal. It never
	// receives environment values or request bodies.
	Log *log.Logger
}

// Guard is an http.Handler.
type Guard struct {
	cfg    Config
	docker *docker.Client
	proxy  *httputil.ReverseProxy
	log    *log.Logger

	mu     sync.Mutex
	execs  map[string]execState // execs created by the guard
	verify map[string]string    // verification container ID -> engine
}

type execState struct {
	created time.Time
	started bool
}

// New creates a guard.
func New(cfg Config) (*Guard, error) {
	c, err := docker.New(cfg.Upstream)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}
	target := &url.URL{Scheme: "http", Host: u.Host}
	if u.Scheme == "unix" {
		path := u.Path
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
		target.Host = "docker"
	}
	g := &Guard{cfg: cfg, docker: c, log: cfg.Log, execs: map[string]execState{}, verify: map[string]string{}}
	if g.log == nil {
		g.log = log.New(io.Discard, "", 0)
	}
	g.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			r.Out.Header.Del("Authorization")
		},
		Transport:     tr,
		FlushInterval: -1, // exec output is streamed
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.log.Printf("docker is not reachable at %s: %v", cfg.Upstream, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"message": "blocked by relyper-docker-guard: docker is not reachable (is /var/run/docker.sock mounted into the guard?)"})
		},
	}
	return g, nil
}

// CheckUpstream pings the Docker daemon once.
func (g *Guard) CheckUpstream(ctx context.Context) error {
	_, err := g.docker.Ping(ctx)
	return err
}

// Features lists the enabled write features.
func (g *Guard) Features() []string {
	var f []string
	if g.cfg.Dumps {
		f = append(f, FeatureDumps)
	}
	if g.cfg.Verify {
		f = append(f, FeatureVerify)
	}
	if g.cfg.Lifecycle {
		f = append(f, FeatureLifecycle)
	}
	return f
}

var (
	versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+(/|$)`)
	idRe          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	verifyNameRe  = regexp.MustCompile(`^relyper-verify-[0-9a-f]{12}$`)
	hexPassword   = regexp.MustCompile(fmt.Sprintf(`^[0-9a-f]{%d}$`, dbdump.VerifyPasswordLen))
	dbUserRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`)
)

type route struct {
	method string
	parts  []string // "*" matches one ID segment
}

func match(method, path string, r route) ([]string, bool) {
	if method != r.method && !(r.method == http.MethodGet && method == http.MethodHead) {
		return nil, false
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) != len(r.parts) {
		return nil, false
	}
	var ids []string
	for i, p := range r.parts {
		if p == "*" {
			if !idRe.MatchString(segs[i]) {
				return nil, false
			}
			ids = append(ids, segs[i])
		} else if segs[i] != p {
			return nil, false
		}
	}
	return ids, true
}

// ServeHTTP implements http.Handler.
func (g *Guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.cfg.Token != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(g.cfg.Token)) != 1 {
			g.deny(w, r, http.StatusUnauthorized, "missing or wrong token")
			return
		}
	}
	path := r.URL.Path
	if strings.Contains(path, "..") || strings.Contains(path, "//") || r.URL.RawPath != "" {
		g.deny(w, r, http.StatusForbidden, "path not allowed")
		return
	}
	// The Docker API accepts an optional version prefix; the guard checks
	// the path without it and forwards it unchanged.
	bare := versionPrefix.ReplaceAllString(path, "/")
	m := r.Method
	try := func(method string, parts ...string) ([]string, bool) {
		return match(m, bare, route{method, parts})
	}

	if _, ok := try(http.MethodGet, "relyper", "guard"); ok {
		writeJSON(w, http.StatusOK, docker.GuardInfo{Guard: "relyper-docker-guard", Features: g.Features(), RequireLabel: g.cfg.RequireLabel})
		return
	}
	// Read-only discovery.
	for _, p := range [][]string{{"_ping"}, {"version"}, {"info"}, {"containers", "json"}, {"volumes"}} {
		if _, ok := try(http.MethodGet, p...); ok {
			g.forward(w, r, nil)
			return
		}
	}
	for _, p := range [][]string{{"containers", "*", "json"}, {"services"}, {"tasks"}} {
		if _, ok := try(http.MethodGet, p...); ok {
			g.forward(w, r, redactEnv)
			return
		}
	}
	if ids, ok := try(http.MethodGet, "exec", "*", "json"); ok {
		if !g.knownExec(ids[0], false) {
			g.deny(w, r, http.StatusForbidden, "unknown exec")
			return
		}
		g.forward(w, r, nil)
		return
	}
	if ids, ok := try(http.MethodPost, "containers", "*", "exec"); ok {
		g.execCreate(w, r, ids[0])
		return
	}
	if ids, ok := try(http.MethodPost, "exec", "*", "start"); ok {
		g.execStart(w, r, ids[0])
		return
	}
	if _, ok := try(http.MethodPost, "containers", "create"); ok {
		g.create(w, r)
		return
	}
	if ids, ok := try(http.MethodPost, "containers", "*", "start"); ok {
		if _, ok := g.verifyEngine(ids[0]); !ok || !g.cfg.Verify {
			g.deny(w, r, http.StatusForbidden, "only restore-test containers created through the guard can be started")
			return
		}
		g.audit(r, "start restore-test container "+ids[0])
		g.forwardFixed(w, r, url.Values{}, nil)
		return
	}
	if ids, ok := try(http.MethodPut, "containers", "*", "archive"); ok {
		g.putArchive(w, r, ids[0])
		return
	}
	if ids, ok := try(http.MethodDelete, "containers", "*"); ok {
		g.remove(w, r, ids[0])
		return
	}
	for _, action := range []string{"stop", "restart"} {
		if ids, ok := try(http.MethodPost, "containers", "*", action); ok {
			g.lifecycle(w, r, ids[0], action)
			return
		}
	}
	g.deny(w, r, http.StatusForbidden, "endpoint not allowed")
}

// --- reads ---------------------------------------------------------------

func (g *Guard) forward(w http.ResponseWriter, r *http.Request, filter func(any)) {
	if r.Method == http.MethodGet && filter != nil {
		g.forwardFiltered(w, r, filter)
		return
	}
	out := r.Clone(r.Context())
	out.Body = http.NoBody
	out.ContentLength = 0
	g.proxy.ServeHTTP(w, out)
}

// forwardFiltered reads the JSON answer, applies filter and writes it back.
func (g *Guard) forwardFiltered(w http.ResponseWriter, r *http.Request, filter func(any)) {
	rec := &bufferedResponse{header: http.Header{}}
	out := r.Clone(r.Context())
	out.Body = http.NoBody
	out.ContentLength = 0
	out.Header.Del("Accept-Encoding")
	g.proxy.ServeHTTP(rec, out)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if rec.status != http.StatusOK {
		copyHeader(w.Header(), rec.header)
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body)
		return
	}
	var v any
	if err := json.Unmarshal(rec.body, &v); err != nil {
		g.deny(w, r, http.StatusBadGateway, "unexpected answer from docker")
		return
	}
	filter(v)
	writeJSON(w, http.StatusOK, v)
}

// redactEnv replaces every "Env" list of "KEY=value" strings by the keys.
// Environment variables of other containers hold their secrets; Relyper
// Backup only needs to know which variables exist.
func redactEnv(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if k == "Env" {
				if list, ok := x.([]any); ok {
					for i, e := range list {
						if s, ok := e.(string); ok {
							name, _, _ := strings.Cut(s, "=")
							list[i] = name
						}
					}
					continue
				}
			}
			redactEnv(x)
		}
	case []any:
		for _, x := range t {
			redactEnv(x)
		}
	}
}

// --- exec ----------------------------------------------------------------

type execRequest struct {
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env"`
	User         string   `json:"User"`
}

func (g *Guard) execCreate(w http.ResponseWriter, r *http.Request, id string) {
	var req execRequest
	if err := decodeStrict(r, &req); err != nil {
		g.deny(w, r, http.StatusForbidden, "exec body: "+err.Error())
		return
	}
	if req.User != "" {
		g.deny(w, r, http.StatusForbidden, "exec as another user is not allowed")
		return
	}
	if len(req.Cmd) != 3 || req.Cmd[0] != "sh" || req.Cmd[1] != "-c" {
		g.deny(w, r, http.StatusForbidden, "command not allowed")
		return
	}
	script := req.Cmd[2]
	ctx := r.Context()
	var what string
	if engine, ok := g.verifyEngine(id); ok {
		if !g.cfg.Verify {
			g.deny(w, r, http.StatusForbidden, "restore tests are disabled")
			return
		}
		p, _ := dbdump.Policy(engine)
		if !slices.Contains(p.Verify, script) {
			g.deny(w, r, http.StatusForbidden, "command not allowed in a restore-test container")
			return
		}
		for _, e := range req.Env {
			k, v, _ := strings.Cut(e, "=")
			if !slices.Contains(p.VerifyExecEnv, k) || !hexPassword.MatchString(v) {
				g.deny(w, r, http.StatusForbidden, "environment not allowed")
				return
			}
		}
		what = "restore-test script in " + id
	} else {
		if !g.cfg.Dumps {
			g.deny(w, r, http.StatusForbidden, "database dumps are disabled")
			return
		}
		d, err := g.docker.InspectContainer(ctx, id)
		if err != nil {
			g.upstreamError(w, r, err)
			return
		}
		engine := discovery.EngineOf(d.Config.Image)
		if engine == "" || !d.State.Running || d.Config.Labels[docker.VerifyLabel] == "true" {
			g.deny(w, r, http.StatusForbidden, "not a running database container")
			return
		}
		if g.cfg.RequireLabel && d.Config.Labels[DumpLabel] != "true" {
			g.deny(w, r, http.StatusForbidden, "container is not labelled "+DumpLabel+"=true")
			return
		}
		p, _ := dbdump.Policy(engine)
		if !slices.Contains(p.Source, script) {
			g.deny(w, r, http.StatusForbidden, "command not allowed")
			return
		}
		if len(req.Env) > 0 {
			g.deny(w, r, http.StatusForbidden, "environment not allowed")
			return
		}
		what = engine + " dump script in " + d.Name
	}
	body := map[string]any{"AttachStdout": true, "AttachStderr": true, "Cmd": []string{"sh", "-c", script}}
	if len(req.Env) > 0 {
		body["Env"] = req.Env
	}
	resp, err := g.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", body)
	if err != nil {
		g.upstreamError(w, r, err)
		return
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(resp, &created); err != nil || !idRe.MatchString(created.ID) {
		g.deny(w, r, http.StatusBadGateway, "unexpected answer from docker")
		return
	}
	g.mu.Lock()
	g.gcExecs()
	g.execs[created.ID] = execState{created: time.Now()}
	g.mu.Unlock()
	g.audit(r, "exec "+what)
	writeJSON(w, http.StatusCreated, map[string]string{"Id": created.ID})
}

func (g *Guard) gcExecs() {
	for id, e := range g.execs {
		if time.Since(e.created) > 6*time.Hour {
			delete(g.execs, id)
		}
	}
}

// knownExec reports whether the guard created the exec. With start it also
// marks the exec as started: an exec can be started only once.
func (g *Guard) knownExec(id string, start bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.execs[id]
	if !ok || (start && e.started) {
		return false
	}
	if start {
		e.started = true
		g.execs[id] = e
	}
	return true
}

func (g *Guard) execStart(w http.ResponseWriter, r *http.Request, id string) {
	if !g.knownExec(id, true) {
		g.deny(w, r, http.StatusForbidden, "unknown exec")
		return
	}
	g.forwardFixed(w, r, url.Values{}, map[string]any{"Detach": false, "Tty": false})
}

// --- restore-test containers -------------------------------------------

type createRequest struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env"`
	Cmd        []string          `json:"Cmd"`
	Labels     map[string]string `json:"Labels"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
		AutoRemove  bool   `json:"AutoRemove"`
		Memory      int64  `json:"Memory"`
	} `json:"HostConfig"`
}

func (g *Guard) create(w http.ResponseWriter, r *http.Request) {
	if !g.cfg.Verify {
		g.deny(w, r, http.StatusForbidden, "restore tests are disabled")
		return
	}
	name := r.URL.Query().Get("name")
	if !verifyNameRe.MatchString(name) || len(r.URL.Query()) != 1 {
		g.deny(w, r, http.StatusForbidden, "only restore-test containers named relyper-verify-<id> can be created")
		return
	}
	var req createRequest
	if err := decodeStrict(r, &req); err != nil {
		g.deny(w, r, http.StatusForbidden, "create body: "+err.Error())
		return
	}
	if len(req.Cmd) > 0 || req.HostConfig.NetworkMode != "none" || req.HostConfig.AutoRemove || req.HostConfig.Memory < 0 ||
		len(req.Labels) != 1 || req.Labels[docker.VerifyLabel] != "true" {
		g.deny(w, r, http.StatusForbidden, "container settings not allowed")
		return
	}
	engine := discovery.EngineOf(req.Image)
	if engine == "" {
		g.deny(w, r, http.StatusForbidden, "not a database image")
		return
	}
	// Only images that already run as a database on this host: the guard
	// never pulls, and cannot be used to start arbitrary images.
	if ok, err := g.imageInUse(r.Context(), req.Image); err != nil {
		g.upstreamError(w, r, err)
		return
	} else if !ok {
		g.deny(w, r, http.StatusForbidden, "image is not used by a running database container")
		return
	}
	p, _ := dbdump.Policy(engine)
	for _, e := range req.Env {
		k, v, _ := strings.Cut(e, "=")
		switch {
		case k == p.UserEnv && p.UserEnv != "" && dbUserRe.MatchString(v):
		case slices.Contains(p.CreateEnv, k) && hexPassword.MatchString(v):
		default:
			g.deny(w, r, http.StatusForbidden, "environment not allowed")
			return
		}
	}
	host := map[string]any{
		"NetworkMode":    "none",
		"Privileged":     false,
		"SecurityOpt":    []string{"no-new-privileges:true"},
		"PidsLimit":      4096, // mongod alone runs hundreds of threads
		"ReadonlyRootfs": false,
	}
	if req.HostConfig.Memory > 0 {
		host["Memory"] = req.HostConfig.Memory
	}
	body := map[string]any{
		"Image":      req.Image,
		"Env":        req.Env,
		"Labels":     map[string]string{docker.VerifyLabel: "true"},
		"HostConfig": host,
	}
	resp, err := g.callQuery(r.Context(), http.MethodPost, "/containers/create", url.Values{"name": {name}}, body)
	if err != nil {
		g.upstreamError(w, r, err)
		return
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(resp, &created); err != nil || !idRe.MatchString(created.ID) {
		g.deny(w, r, http.StatusBadGateway, "unexpected answer from docker")
		return
	}
	g.mu.Lock()
	g.verify[created.ID] = engine
	g.mu.Unlock()
	g.audit(r, "create restore-test container "+name+" from "+req.Image)
	writeJSON(w, http.StatusCreated, map[string]any{"Id": created.ID, "Warnings": []string{}})
}

func (g *Guard) imageInUse(ctx context.Context, image string) (bool, error) {
	list, err := g.docker.ListContainers(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range list {
		if c.Image != image || c.State != "running" || c.Labels[docker.VerifyLabel] == "true" {
			continue
		}
		if g.cfg.RequireLabel && c.Labels[DumpLabel] != "true" {
			continue
		}
		return true, nil
	}
	return false, nil
}

// verifyEngine returns the engine of a verification container created by
// this guard. Only the ID returned by create is accepted, not the name.
func (g *Guard) verifyEngine(id string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.verify[id]
	return e, ok
}

func (g *Guard) putArchive(w http.ResponseWriter, r *http.Request, id string) {
	engine, ok := g.verifyEngine(id)
	if !ok || !g.cfg.Verify {
		g.deny(w, r, http.StatusForbidden, "only restore-test containers created through the guard accept files")
		return
	}
	p, _ := dbdump.Policy(engine)
	q := r.URL.Query()
	dir := q.Get("path")
	if len(q) != 1 || !slices.Contains(p.ArchiveDirs, dir) {
		g.deny(w, r, http.StatusForbidden, "target directory not allowed")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/x-tar" {
		g.deny(w, r, http.StatusForbidden, "content type not allowed")
		return
	}
	g.audit(r, "copy dump into restore-test container "+id+":"+dir)
	out := r.Clone(r.Context())
	out.URL.RawQuery = url.Values{"path": {dir}}.Encode()
	g.proxy.ServeHTTP(w, out)
}

func (g *Guard) remove(w http.ResponseWriter, r *http.Request, id string) {
	if !g.cfg.Verify {
		g.deny(w, r, http.StatusForbidden, "restore tests are disabled")
		return
	}
	// Leftovers of an earlier guard process are recognised by the label;
	// nothing else is ever removed.
	d, err := g.docker.InspectContainer(r.Context(), id)
	if err != nil {
		g.upstreamError(w, r, err)
		return
	}
	if d.Config.Labels[docker.VerifyLabel] != "true" || !verifyNameRe.MatchString(d.Name) {
		g.deny(w, r, http.StatusForbidden, "only restore-test containers can be removed")
		return
	}
	g.audit(r, "remove restore-test container "+d.Name)
	g.forwardFixed(w, r, url.Values{"force": {"1"}, "v": {"1"}}, nil)
	g.mu.Lock()
	delete(g.verify, id)
	delete(g.verify, d.ID)
	g.mu.Unlock()
}

func (g *Guard) lifecycle(w http.ResponseWriter, r *http.Request, id, action string) {
	if !g.cfg.Lifecycle {
		g.deny(w, r, http.StatusForbidden, "stopping and restarting containers is disabled (RELYPER_GUARD_LIFECYCLE)")
		return
	}
	t, err := strconv.Atoi(r.URL.Query().Get("t"))
	if err != nil || t < 0 || t > 600 {
		t = 30
	}
	d, err := g.docker.InspectContainer(r.Context(), id)
	if err != nil {
		g.upstreamError(w, r, err)
		return
	}
	g.audit(r, action+" container "+d.Name)
	g.forwardFixed(w, r, url.Values{"t": {strconv.Itoa(t)}}, nil)
}

// --- helpers -------------------------------------------------------------

// forwardFixed forwards r with a query and body chosen by the guard.
func (g *Guard) forwardFixed(w http.ResponseWriter, r *http.Request, q url.Values, body any) {
	out := r.Clone(r.Context())
	out.URL.RawQuery = q.Encode()
	out.Header.Del("Content-Type")
	if body == nil {
		out.Body, out.ContentLength = http.NoBody, 0
	} else {
		b, _ := json.Marshal(body)
		out.Body = io.NopCloser(strings.NewReader(string(b)))
		out.ContentLength = int64(len(b))
		out.Header.Set("Content-Type", "application/json")
	}
	g.proxy.ServeHTTP(w, out)
}

func (g *Guard) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	return g.callQuery(ctx, method, path, nil, body)
}

func (g *Guard) callQuery(ctx context.Context, method, path string, q url.Values, body any) ([]byte, error) {
	var out json.RawMessage
	if err := g.docker.Do(ctx, method, path, q, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *Guard) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	var e *docker.APIError
	if errors.As(err, &e) {
		writeJSON(w, e.Status, map[string]string{"message": e.Message})
		return
	}
	g.deny(w, r, http.StatusBadGateway, "docker is not reachable")
}

func (g *Guard) deny(w http.ResponseWriter, r *http.Request, status int, why string) {
	g.log.Printf("refused %s %s: %s", r.Method, r.URL.Path, why)
	writeJSON(w, status, map[string]string{"message": "blocked by relyper-docker-guard: " + why})
}

func (g *Guard) audit(r *http.Request, what string) {
	g.log.Printf("allowed %s", what)
}

const maxBody = 64 << 10

func decodeStrict(r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return errors.New("content type must be application/json")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("unexpected request body")
	}
	if dec.More() {
		return errors.New("unexpected request body")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = v
	}
}

type bufferedResponse struct {
	header http.Header
	status int
	body   []byte
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(s int) {
	if b.status == 0 {
		b.status = s
	}
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	if len(b.body)+len(p) > 64<<20 {
		return 0, errors.New("answer too large")
	}
	b.body = append(b.body, p...)
	return len(p), nil
}
