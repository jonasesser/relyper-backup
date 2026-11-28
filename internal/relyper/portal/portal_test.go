// Relyper-owned tests: tenant isolation of the customer portal, the
// operator API and the notifier.
package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/auth"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/oplog/sqlitestore"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
)

const secretPW = "repo-password-very-secret"

type fixture struct {
	store   *tenant.Store
	cfg     *v1.Config
	ol      *oplog.OpLog
	h       *Handler
	started []string
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	var err error
	f.store, err = tenant.Open(filepath.Join(t.TempDir(), "tenants.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = &v1.Config{Instance: "host1", Repos: []*v1.Repo{
		{Id: "acme-repo", Guid: "g-acme", Uri: "rest:https://acme:" + secretPW + "@backup.example/acme", Password: secretPW},
		{Id: "beta-repo", Guid: "g-beta", Uri: "s3:https://s3.example/beta", Password: "beta-password-xyz", Env: []string{"AWS_SECRET_ACCESS_KEY=beta-s3-secret"}},
		{Id: "ops-repo", Guid: "g-ops", Uri: "/data/ops"},
	}, Plans: []*v1.Plan{
		{Id: "acme-web", Repo: "acme-repo", Paths: []string{"/srv/acme"}, Schedule: &v1.Schedule{Schedule: &v1.Schedule_MaxFrequencyDays{MaxFrequencyDays: 1}}},
		{Id: "beta-db", Repo: "beta-repo", Paths: []string{"/srv/beta"}, Schedule: &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "0 2 * * *"}}},
		// Assigned to acme, but its repository is not: must stay invisible.
		{Id: "acme-leak", Repo: "ops-repo", Paths: []string{"/srv/ops"}},
	}}
	f.store.Create("acme", "ACME", nil)
	f.store.Create("beta", "Beta", nil)
	f.store.AssignPlan("acme", "acme-web")
	f.store.AssignPlan("acme", "acme-leak")
	f.store.AssignRepo("acme", "acme-repo")
	f.store.AssignPlan("beta", "beta-db")
	f.store.AssignRepo("beta", "beta-repo")

	st, err := sqlitestore.NewMemorySqliteStore(t)
	if err != nil {
		t.Fatal(err)
	}
	f.ol, err = oplog.NewOpLog(st)
	if err != nil {
		t.Fatal(err)
	}
	gate := &tenant.Gate{Store: f.store, Cfg: tenant.PortalConfigFromEnv(func(string) string { return "" })}
	f.h = New(Deps{Store: f.store, Gate: gate, Config: func() (*v1.Config, error) { return f.cfg, nil }, OpLog: f.ol,
		Verify: func(tok string) (*identity.PortalSession, bool) {
			// Test tokens: "<tenant>:<role>"
			parts := strings.SplitN(tok, ":", 2)
			if len(parts) != 2 {
				return nil, false
			}
			return &identity.PortalSession{Subject: "s", Email: "m@" + parts[0] + ".example", Tenant: parts[0], External: parts[0], Roles: []string{parts[1]}}, true
		},
		StartBackup: func(p *v1.Plan) error { f.started = append(f.started, p.GetId()); return nil },
		Now:         func() time.Time { return f.now }})
	return f
}

func (f *fixture) addBackup(t *testing.T, plan, repoGUID, repoID string, status v1.OperationStatus, end time.Time, msg string) *v1.Operation {
	op := &v1.Operation{InstanceId: "host1", PlanId: plan, RepoGuid: repoGUID, RepoId: repoID, Status: status,
		UnixTimeStartMs: end.Add(-time.Minute).UnixMilli(), UnixTimeEndMs: end.UnixMilli(), DisplayMessage: msg,
		Op: &v1.Operation_OperationBackup{OperationBackup: &v1.OperationBackup{}}}
	if err := f.ol.Add(op); err != nil {
		t.Fatal(err)
	}
	return op
}

func (f *fixture) addSnapshot(t *testing.T, plan, repoGUID, id string, at time.Time) {
	id = id + strings.Repeat("0", 64-len(id))
	op := &v1.Operation{InstanceId: "host1", PlanId: plan, RepoGuid: repoGUID, RepoId: "x", Status: v1.OperationStatus_STATUS_SUCCESS,
		UnixTimeStartMs: at.UnixMilli(), SnapshotId: id,
		Op: &v1.Operation_OperationIndexSnapshot{OperationIndexSnapshot: &v1.OperationIndexSnapshot{Snapshot: &v1.ResticSnapshot{Id: id, UnixTimeMs: at.UnixMilli(), Paths: []string{"/srv"}}}}}
	if err := f.ol.Add(op); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) call(t *testing.T, token, method, path string, body any) (int, map[string]any, string) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, APIPrefix+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestPortalNeedsPortalSession(t *testing.T) {
	f := newFixture(t)
	for _, tok := range []string{"", "garbage", "unknown:backup_customer", "acme:backup_user"} {
		if code, _, _ := f.call(t, tok, http.MethodGet, "overview", nil); code != http.StatusUnauthorized {
			t.Errorf("token %q: %d", tok, code)
		}
	}
	yes := true
	f.store.Configure("acme", tenant.Settings{Suspended: &yes})
	if code, _, _ := f.call(t, "acme:backup_customer", http.MethodGet, "overview", nil); code != http.StatusUnauthorized {
		t.Errorf("suspended tenant: %d", code)
	}
}

func TestPortalIsolation(t *testing.T) {
	f := newFixture(t)
	f.addBackup(t, "acme-web", "g-acme", "acme-repo", v1.OperationStatus_STATUS_SUCCESS, f.now.Add(-time.Hour), "")
	f.addBackup(t, "beta-db", "g-beta", "beta-repo", v1.OperationStatus_STATUS_ERROR, f.now.Add(-time.Hour), "failed: beta-s3-secret leaked? beta-password-xyz")
	f.addSnapshot(t, "acme-web", "g-acme", "snapA", f.now.Add(-time.Hour))
	f.addSnapshot(t, "beta-db", "g-beta", "snapB", f.now.Add(-time.Hour))
	// An operation with acme's plan ID in another repository is not acme's.
	f.addSnapshot(t, "acme-web", "g-ops", "snapForeignRepo", f.now.Add(-time.Hour))

	code, ov, raw := f.call(t, "acme:backup_customer", http.MethodGet, "overview", nil)
	if code != http.StatusOK {
		t.Fatalf("overview: %d %s", code, raw)
	}
	plans := ov["plans"].([]any)
	if len(plans) != 1 || plans[0].(map[string]any)["id"] != "acme-web" {
		t.Fatalf("acme sees plans %v", plans)
	}
	if plans[0].(map[string]any)["state"] != StateOK || plans[0].(map[string]any)["snapshots"].(float64) != 1 {
		t.Fatalf("acme plan: %v", plans[0])
	}
	for _, forbidden := range []string{"beta", "ops-repo", "acme-leak", secretPW, "snapForeignRepo"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("overview of acme contains %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, "rest:https://backup.example/acme") {
		t.Errorf("location not shown without user info: %s", raw)
	}
	// Foreign and hidden plans are 404, like unknown ones.
	for _, p := range []string{"beta-db", "acme-leak", "nope"} {
		for _, sub := range []string{"snapshots", "runs"} {
			if code, _, _ := f.call(t, "acme:backup_customer", http.MethodGet, "plans/"+p+"/"+sub, nil); code != http.StatusNotFound {
				t.Errorf("acme %s/%s: %d", p, sub, code)
			}
		}
		if code, _, _ := f.call(t, "acme:backup_customer_admin", http.MethodPost, "plans/"+p+"/backup", map[string]any{}); code != http.StatusNotFound {
			t.Errorf("acme backup %s: %d", p, code)
		}
	}
	code, _, raw = f.call(t, "acme:backup_customer", http.MethodGet, "plans/acme-web/snapshots", nil)
	if code != http.StatusOK || !strings.Contains(raw, "snapA") || strings.Contains(raw, "snapB") || strings.Contains(raw, "snapForeignRepo") {
		t.Fatalf("snapshots: %d %s", code, raw)
	}
	// Beta sees its failure without secrets.
	code, ov, raw = f.call(t, "beta:backup_customer", http.MethodGet, "overview", nil)
	if code != http.StatusOK || len(ov["problems"].([]any)) != 1 {
		t.Fatalf("beta overview: %d %s", code, raw)
	}
	if strings.Contains(raw, "beta-s3-secret") || strings.Contains(raw, "beta-password-xyz") || strings.Contains(raw, "acme") {
		t.Fatalf("beta overview leaks: %s", raw)
	}
}

func TestPortalRoles(t *testing.T) {
	f := newFixture(t)
	if code, _, _ := f.call(t, "acme:backup_customer", http.MethodPost, "plans/acme-web/backup", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("viewer started a backup: %d", code)
	}
	if code, _, _ := f.call(t, "acme:backup_customer", http.MethodPut, "notifications", map[string]any{"recipients": []string{"a@b.example"}}); code != http.StatusForbidden {
		t.Fatalf("viewer changed notifications: %d", code)
	}
	if code, _, raw := f.call(t, "acme:backup_customer_admin", http.MethodPost, "plans/acme-web/backup", map[string]any{}); code != http.StatusAccepted {
		t.Fatalf("admin backup: %d %s", code, raw)
	}
	if len(f.started) != 1 || f.started[0] != "acme-web" {
		t.Fatalf("started %v", f.started)
	}
	// Form posts (no JSON content type) are refused.
	req := httptest.NewRequest(http.MethodPost, APIPrefix+"plans/acme-web/backup", strings.NewReader("a=b"))
	req.Header.Set("Authorization", "Bearer acme:backup_customer_admin")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", rec.Code)
	}
	// Rate limit: at most 6 actions per hour.
	for i := 0; i < 5; i++ {
		f.call(t, "acme:backup_customer_admin", http.MethodPost, "plans/acme-web/backup", map[string]any{})
	}
	if code, _, _ := f.call(t, "acme:backup_customer_admin", http.MethodPost, "plans/acme-web/backup", map[string]any{}); code != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d", code)
	}
	code, out, _ := f.call(t, "acme:backup_customer_admin", http.MethodPut, "notifications", map[string]any{"recipients": []string{"ops@acme.example"}, "events": []string{"failure", "success"}})
	if code != http.StatusOK {
		t.Fatalf("set notify: %d %v", code, out)
	}
	if code, _, _ := f.call(t, "acme:backup_customer_admin", http.MethodPut, "notifications", map[string]any{"recipients": []string{"x@y.example\r\nBcc: z@z.example"}}); code != http.StatusBadRequest {
		t.Fatalf("header injection accepted: %d", code)
	}
}

func TestPlanStates(t *testing.T) {
	f := newFixture(t)
	f.addBackup(t, "acme-web", "g-acme", "acme-repo", v1.OperationStatus_STATUS_SUCCESS, f.now.Add(-48*time.Hour), "")
	_, ov, _ := f.call(t, "acme:backup_customer", http.MethodGet, "overview", nil)
	if st := ov["plans"].([]any)[0].(map[string]any)["state"]; st != StateStale {
		t.Fatalf("state %v, want stale", st)
	}
	f.addBackup(t, "acme-web", "g-acme", "acme-repo", v1.OperationStatus_STATUS_ERROR, f.now.Add(-time.Hour), "boom")
	_, ov, _ = f.call(t, "acme:backup_customer", http.MethodGet, "overview", nil)
	if st := ov["plans"].([]any)[0].(map[string]any)["state"]; st != StateFailing {
		t.Fatalf("state %v, want failing", st)
	}
}

type fakeSender struct {
	mu   sync.Mutex
	sent []Mail
}

func (s *fakeSender) Send(_ context.Context, m Mail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func TestNotifier(t *testing.T) {
	f := newFixture(t)
	f.store.SetNotify("acme", tenant.Notify{Recipients: []string{"ops@acme.example"}, Events: []string{tenant.EventFailure, tenant.EventStale}})
	f.store.SetNotify("beta", tenant.Notify{Recipients: []string{"ops@beta.example"}, Events: []string{tenant.EventSuccess}})
	snd := &fakeSender{}
	n := &Notifier{Store: f.store, Config: func() (*v1.Config, error) { return f.cfg, nil }, OpLog: f.ol, Sender: snd, Now: func() time.Time { return f.now }}
	n.init()
	drain := func() {
		for {
			select {
			case m := <-n.queue:
				_ = snd.Send(context.Background(), m)
			default:
				return
			}
		}
	}
	fail := f.addBackup(t, "acme-web", "g-acme", "acme-repo", v1.OperationStatus_STATUS_ERROR, f.now, "error with "+secretPW)
	n.HandleOperation(fail)
	n.HandleOperation(fail) // same operation again: no second mail
	ok := f.addBackup(t, "acme-web", "g-acme", "acme-repo", v1.OperationStatus_STATUS_SUCCESS, f.now, "")
	n.HandleOperation(ok) // success not subscribed by acme
	betaOK := f.addBackup(t, "beta-db", "g-beta", "beta-repo", v1.OperationStatus_STATUS_SUCCESS, f.now, "")
	n.HandleOperation(betaOK)
	foreign := f.addBackup(t, "acme-web", "g-ops", "ops-repo", v1.OperationStatus_STATUS_ERROR, f.now, "")
	n.HandleOperation(foreign) // other repository: not acme's
	running := f.addBackup(t, "acme-web", "g-acme", "acme-repo", v1.OperationStatus_STATUS_INPROGRESS, f.now, "")
	n.HandleOperation(running)
	drain()
	if len(snd.sent) != 2 {
		t.Fatalf("sent %d mails: %+v", len(snd.sent), snd.sent)
	}
	a, b := snd.sent[0], snd.sent[1]
	if a.To[0] != "ops@acme.example" || !strings.Contains(a.Subject, "acme-web failed") || strings.Contains(a.Body, secretPW) {
		t.Fatalf("acme mail: %+v", a)
	}
	if b.To[0] != "ops@beta.example" || !strings.Contains(b.Subject, "beta-db succeeded") || strings.Contains(b.Body, "acme") {
		t.Fatalf("beta mail: %+v", b)
	}

	// Stale: once, and again only after recovery.
	f2 := newFixture(t)
	f2.store.SetNotify("acme", tenant.Notify{Recipients: []string{"ops@acme.example"}, Events: []string{tenant.EventStale}})
	f2.addBackup(t, "acme-web", "g-acme", "acme-repo", v1.OperationStatus_STATUS_SUCCESS, f2.now.Add(-40*time.Hour), "")
	snd2 := &fakeSender{}
	n2 := &Notifier{Store: f2.store, Config: func() (*v1.Config, error) { return f2.cfg, nil }, OpLog: f2.ol, Sender: snd2, Now: func() time.Time { return f2.now }}
	n2.init()
	n2.CheckStale()
	n2.CheckStale()
	if len(n2.queue) != 1 {
		t.Fatalf("stale mails queued: %d", len(n2.queue))
	}
}

func TestMailBuild(t *testing.T) {
	m := Mail{To: []string{"a@b.example"}, Subject: "Plan x\r\nBcc: evil@example.com", Body: "line1\nline2"}
	b := string(m.Build("Relyper <noreply@relyper.example>", time.Unix(0, 0)))
	if strings.Contains(b, "\r\nBcc:") {
		t.Fatalf("header injection: %q", b)
	}
	if !strings.Contains(b, "line1\r\nline2\r\n") {
		t.Fatalf("body: %q", b)
	}
	if _, err := SMTPConfigFromEnv(func(k string) string {
		return map[string]string{"RELYPER_SMTP_HOST": "smtp.example", "RELYPER_SMTP_TLS": "none", "RELYPER_SMTP_FROM": "a@b.example"}[k]
	}); err == nil {
		t.Fatal("plaintext SMTP to a remote host accepted")
	}
	c, err := SMTPConfigFromEnv(func(k string) string {
		return map[string]string{"RELYPER_SMTP_HOST": "smtp.example", "RELYPER_SMTP_FROM": "a@b.example"}[k]
	})
	if err != nil || c.Port != 587 || c.TLS != "starttls" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
}

func TestOperatorAPI(t *testing.T) {
	f := newFixture(t)
	admin := false
	op := &Operator{Store: f.store, Config: func() (*v1.Config, error) { return f.cfg, nil }, IsAdmin: func(context.Context) bool { return admin }}
	do := func(method, path string, body any) (int, string) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, OperatorPrefix+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), auth.UserContextKey, &v1.User{Name: "op"}))
		rec := httptest.NewRecorder()
		op.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, raw := do(http.MethodGet, "", nil); code != http.StatusOK || !strings.Contains(raw, `"acme"`) {
		t.Fatalf("list: %d %s", code, raw)
	}
	if code, _ := do(http.MethodPost, "", map[string]any{"id": "gamma", "name": "Gamma"}); code != http.StatusForbidden {
		t.Fatalf("non-admin created a tenant: %d", code)
	}
	admin = true
	if code, raw := do(http.MethodPost, "", map[string]any{"id": "gamma", "name": "Gamma"}); code != http.StatusOK {
		t.Fatalf("create: %d %s", code, raw)
	}
	if code, _ := do(http.MethodPost, "gamma/plans", map[string]any{"plan": "beta-db"}); code != http.StatusBadRequest {
		t.Fatalf("plan of another tenant reassigned: %d", code)
	}
	if code, _ := do(http.MethodPost, "gamma/repos", map[string]any{"repo": "nope"}); code != http.StatusBadRequest {
		t.Fatalf("unknown repo assigned: %d", code)
	}
	if code, raw := do(http.MethodPost, "gamma/repos", map[string]any{"repo": "ops-repo"}); code != http.StatusOK {
		t.Fatalf("assign repo: %d %s", code, raw)
	}
	if code, _ := do(http.MethodDelete, "gamma", nil); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
}
