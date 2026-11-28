package portal

// Customer plans for Relyper Budget. All Budget customers live in one
// server, so their data cannot be backed up as files. Instead a customer's
// plan backs up the export of their Budget user (appexport), fetched by the
// plan's "before backup" hook; a restore adds the budgets of a snapshot as
// new budgets of that user. Design: docs/CONCEPT-MULTITENANT.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/appexport"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
	"google.golang.org/protobuf/proto"
)

// BudgetAPI is the part of appexport.Budget the portal uses.
type BudgetAPI interface {
	Users(ctx context.Context) ([]appexport.BudgetUser, error)
	Import(ctx context.Context, userID string, export io.Reader) ([]appexport.Created, error)
}

// Apps creates and restores customer plans for Relyper Budget.
type Apps struct {
	Store     *tenant.Store
	Config    func() (*v1.Config, error)
	Transform func(fn func(cfg *v1.Config) error) error
	Budget    BudgetAPI
	// ExportBase is the directory below which the exports are kept
	// (<data>/relyper/app-exports).
	ExportBase string
	// Command is the relyper-appexport binary used in hooks.
	Command string
	// Dump writes one file of a snapshot to w.
	Dump func(ctx context.Context, repoID, snapshotID, file string, w io.Writer) error
}

// BudgetPlanRequest creates a Budget plan for a tenant.
type BudgetPlanRequest struct {
	User string `json:"user"`
	Repo string `json:"repo"`
	Name string `json:"name"` // optional plan name, default budget-<user>
}

func defaultSchedule() *v1.Schedule {
	return &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "30 2 * * *"}, Clock: v1.Schedule_CLOCK_LOCAL}
}

func defaultRetention() *v1.RetentionPolicy {
	return &v1.RetentionPolicy{Policy: &v1.RetentionPolicy_PolicyTimeBucketed{PolicyTimeBucketed: &v1.RetentionPolicy_TimeBucketedCounts{Daily: 7, Weekly: 4, Monthly: 6}}}
}

func planNameFor(user string) string {
	n := strings.ToLower(user)
	var b strings.Builder
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() == 8 {
			break
		}
	}
	return "budget-" + b.String()
}

// CreateBudgetPlan creates the plan that backs up one Budget user of the
// tenant into one of the tenant's repositories, and links it.
func (a *Apps) CreateBudgetPlan(ctx context.Context, tenantID string, in BudgetPlanRequest) (*v1.Plan, error) {
	t, err := a.Store.Get(tenantID)
	if err != nil {
		return nil, err
	}
	if !appexport.ValidUserID(in.User) {
		return nil, errors.New("invalid Relyper Budget user")
	}
	if !t.HasRepo(in.Repo) {
		return nil, errors.New("assign the repository to this customer first")
	}
	for _, l := range t.Apps {
		if l.App == appexport.AppBudget && l.User == in.User && planRepo(a.Config, l.Plan) == in.Repo {
			return nil, fmt.Errorf("plan %s already backs up this user into that repository", l.Plan)
		}
	}
	users, err := a.Budget.Users(ctx)
	if err != nil {
		return nil, err
	}
	var user *appexport.BudgetUser
	for i := range users {
		if users[i].ID == in.User {
			user = &users[i]
		}
	}
	if user == nil {
		return nil, errors.New("Relyper Budget does not know this user")
	}
	if o, ok := a.Store.OwnerOfApp(appexport.AppBudget, in.User); ok && o.ID != t.ID {
		return nil, fmt.Errorf("this Relyper Budget user belongs to customer %s", o.ID)
	}
	name := in.Name
	if name == "" {
		name = planNameFor(in.User)
	}
	id, err := tenant.ObjectID(t.ID, name)
	if err != nil {
		return nil, err
	}
	dir := appexport.Dir(a.ExportBase, t.ID, in.User)
	hook, err := appexport.HookCommand(a.Command, dir, in.User)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the export directory: %w", err)
	}
	plan := &v1.Plan{
		Id:        id,
		Repo:      in.Repo,
		Paths:     []string{dir},
		Schedule:  defaultSchedule(),
		Retention: defaultRetention(),
		Hooks: []*v1.Hook{{
			Conditions: []v1.Hook_Condition{v1.Hook_CONDITION_SNAPSHOT_START},
			OnError:    v1.Hook_ON_ERROR_FATAL,
			Action:     &v1.Hook_ActionCommand{ActionCommand: &v1.Hook_Command{Command: hook}},
		}},
	}
	err = a.Transform(func(cfg *v1.Config) error {
		for _, p := range cfg.GetPlans() {
			if p.GetId() == id {
				return fmt.Errorf("a plan with ID %q already exists", id)
			}
		}
		found := false
		for _, r := range cfg.GetRepos() {
			if r.GetId() == in.Repo {
				found = true
			}
		}
		if !found {
			return errors.New("unknown repository")
		}
		cfg.Plans = append(cfg.Plans, proto.Clone(plan).(*v1.Plan))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := a.Store.SetApp(t.ID, tenant.AppLink{Plan: id, App: appexport.AppBudget, User: user.ID, UserName: user.UserName, Dir: dir, Created: time.Now().UTC()}); err != nil {
		return nil, err
	}
	return plan, nil
}

func planRepo(cfgf func() (*v1.Config, error), planID string) string {
	cfg, err := cfgf()
	if err != nil {
		return ""
	}
	for _, p := range cfg.GetPlans() {
		if p.GetId() == planID {
			return p.GetRepo()
		}
	}
	return ""
}

// Restore adds the budgets of a snapshot of a Budget plan as new budgets of
// the linked user. The snapshot must belong to the plan.
func (a *Apps) Restore(ctx context.Context, t *tenant.Tenant, p *v1.Plan, snapshotID string) ([]appexport.Created, error) {
	l := t.App(p.GetId())
	if l == nil || l.App != appexport.AppBudget {
		return nil, errors.New("this plan cannot be restored from the portal")
	}
	file := path.Join(strings.ReplaceAll(l.Dir, "\\", "/"), appexport.ExportFile)
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(a.Dump(ctx, p.GetRepo(), snapshotID, file, pw))
	}()
	created, err := a.Budget.Import(ctx, l.User, pr)
	pr.CloseWithError(errors.New("done"))
	return created, err
}

// serveRestore handles POST plans/<id>/restore {snapshot}.
func (h *Handler) serveRestore(w http.ResponseWriter, r *http.Request, m *member, planID string) {
	if h.d.Apps == nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Snapshot string `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Snapshot == "" {
		fail(w, http.StatusBadRequest, "snapshot is required")
		return
	}
	s, err := h.scope(m.tenant)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal error")
		return
	}
	p := s.plan(planID)
	if p == nil || m.tenant.App(planID) == nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	snaps, err := h.snapshots(m.tenant, planID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal error")
		return
	}
	found := false
	for _, sn := range snaps {
		if sn.ID == in.Snapshot {
			found = true
		}
	}
	if !found {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if !h.allowAction(m.tenant.ID) {
		h.record(r, m, "portal.restore", "denied", "rate limit")
		fail(w, http.StatusTooManyRequests, "too many actions in the last hour, try again later")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
	defer cancel()
	created, err := h.d.Apps.Restore(ctx, m.tenant, p, in.Snapshot)
	if err != nil {
		h.record(r, m, "portal.restore", "error", "plan "+planID+" snapshot "+in.Snapshot+": "+err.Error())
		fail(w, http.StatusBadGateway, "the restore failed: "+err.Error())
		return
	}
	names := make([]string, 0, len(created))
	for _, c := range created {
		names = append(names, c.Name)
	}
	h.record(r, m, "portal.restore", "ok", fmt.Sprintf("plan %s snapshot %s: %d budget(s) added", planID, in.Snapshot, len(created)))
	writeJSON(w, http.StatusOK, map[string]any{"budgets": created, "names": names})
}
