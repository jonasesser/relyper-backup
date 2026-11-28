package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/auth"
	"github.com/garethgeorge/backrest/internal/relyper/audit"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
)

// OperatorPrefix is the path of the operator API for tenants. It is mounted
// behind the web UI authentication.
const OperatorPrefix = "/relyper/tenants/"

// Operator serves tenant management for operators.
type Operator struct {
	Store  *tenant.Store
	Config func() (*v1.Config, error)
	Audit  *audit.Log
	// IsAdmin reports whether the caller may change tenants.
	IsAdmin func(ctx context.Context) bool
	// Status describes the portal setup for the page.
	Status func() map[string]any
}

func operatorName(r *http.Request) string {
	if u, ok := r.Context().Value(auth.UserContextKey).(*v1.User); ok && u != nil {
		return "user:" + u.GetName()
	}
	return "user:anonymous (authentication disabled)"
}

func (o *Operator) record(r *http.Request, action, outcome, msg string) {
	if o.Audit != nil {
		o.Audit.Record(audit.Event{Actor: operatorName(r), Source: "ui", Action: action, Outcome: outcome, Message: msg, RemoteAddr: remoteAddr(r)})
	}
}

type tenantBody struct {
	ID          string         `json:"id"`
	Name        *string        `json:"name"`
	ExternalIDs *[]string      `json:"externalIds"`
	Suspended   *bool          `json:"suspended"`
	Escrow      *bool          `json:"escrow"`
	Plan        string         `json:"plan"`
	Repo        string         `json:"repo"`
	Notify      *tenant.Notify `json:"notify"`
}

// ObjectRef is a plan or repository available for assignment.
type ObjectRef struct {
	ID     string `json:"id"`
	Repo   string `json:"repo,omitempty"`
	Tenant string `json:"tenant,omitempty"`
}

func (o *Operator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := strings.Trim(strings.TrimPrefix(r.URL.Path, OperatorPrefix), "/")
	parts := strings.Split(route, "/")
	if route == "" {
		parts = nil
	}
	if r.Method != http.MethodGet {
		if !o.IsAdmin(r.Context()) {
			o.record(r, "tenant.change", "forbidden", "admin role required")
			fail(w, http.StatusForbidden, "this needs the backup admin role")
			return
		}
		if ct := r.Header.Get("Content-Type"); r.Method != http.MethodDelete && !strings.HasPrefix(ct, "application/json") {
			fail(w, http.StatusUnsupportedMediaType, "content type must be application/json")
			return
		}
	}
	var b tenantBody
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			fail(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 0:
		o.list(w)
	case r.Method == http.MethodPost && len(parts) == 0:
		name := ""
		if b.Name != nil {
			name = *b.Name
		}
		var ext []string
		if b.ExternalIDs != nil {
			ext = *b.ExternalIDs
		}
		t, err := o.Store.Create(b.ID, name, ext)
		o.done(w, r, "tenant.create", b.ID, t, err)
	case r.Method == http.MethodPut && len(parts) == 1:
		t, err := o.Store.Configure(parts[0], tenant.Settings{Name: b.Name, ExternalIDs: b.ExternalIDs, Suspended: b.Suspended, Escrow: b.Escrow})
		o.done(w, r, "tenant.update", parts[0], t, err)
	case r.Method == http.MethodDelete && len(parts) == 1:
		err := o.Store.Delete(parts[0])
		o.done(w, r, "tenant.delete", parts[0], map[string]string{"status": "deleted"}, err)
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "plans":
		if !o.exists(b.Plan, "") {
			fail(w, http.StatusBadRequest, "unknown plan")
			return
		}
		t, err := o.Store.AssignPlan(parts[0], b.Plan)
		o.done(w, r, "tenant.assign_plan", parts[0]+" plan "+b.Plan, t, err)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "plans":
		t, err := o.Store.UnassignPlan(parts[0], parts[2])
		o.done(w, r, "tenant.unassign_plan", parts[0]+" plan "+parts[2], t, err)
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "repos":
		if !o.exists("", b.Repo) {
			fail(w, http.StatusBadRequest, "unknown repository")
			return
		}
		t, err := o.Store.AssignRepo(parts[0], b.Repo)
		o.done(w, r, "tenant.assign_repo", parts[0]+" repo "+b.Repo, t, err)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "repos":
		t, err := o.Store.UnassignRepo(parts[0], parts[2])
		o.done(w, r, "tenant.unassign_repo", parts[0]+" repo "+parts[2], t, err)
	case r.Method == http.MethodPut && len(parts) == 2 && parts[1] == "notifications":
		if b.Notify == nil {
			fail(w, http.StatusBadRequest, "notify is required")
			return
		}
		t, err := o.Store.SetNotify(parts[0], *b.Notify)
		o.done(w, r, "tenant.notify", parts[0], t, err)
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

func (o *Operator) exists(plan, repo string) bool {
	cfg, err := o.Config()
	if err != nil {
		return false
	}
	for _, p := range cfg.GetPlans() {
		if plan != "" && p.GetId() == plan {
			return true
		}
	}
	for _, rp := range cfg.GetRepos() {
		if repo != "" && rp.GetId() == repo {
			return true
		}
	}
	return false
}

func (o *Operator) done(w http.ResponseWriter, r *http.Request, action, subject string, v any, err error) {
	if err != nil {
		o.record(r, action, "error", subject+": "+err.Error())
		code := http.StatusBadRequest
		if errors.Is(err, tenant.ErrNotFound) {
			code = http.StatusNotFound
		}
		fail(w, code, err.Error())
		return
	}
	o.record(r, action, "ok", subject)
	writeJSON(w, http.StatusOK, v)
}

func (o *Operator) list(w http.ResponseWriter) {
	cfg, err := o.Config()
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal error")
		return
	}
	tenants := o.Store.List()
	owner := func(isPlan bool, id string) string {
		for _, t := range tenants {
			if (isPlan && t.HasPlan(id)) || (!isPlan && t.HasRepo(id)) {
				return t.ID
			}
		}
		return ""
	}
	plans, repos := []ObjectRef{}, []ObjectRef{}
	for _, p := range cfg.GetPlans() {
		plans = append(plans, ObjectRef{ID: p.GetId(), Repo: p.GetRepo(), Tenant: owner(true, p.GetId())})
	}
	for _, rp := range cfg.GetRepos() {
		repos = append(repos, ObjectRef{ID: rp.GetId(), Tenant: owner(false, rp.GetId())})
	}
	out := map[string]any{"tenants": tenants, "plans": plans, "repos": repos, "events": tenant.AllEvents}
	if o.Status != nil {
		out["status"] = o.Status()
	}
	writeJSON(w, http.StatusOK, out)
}
