// Relyper-owned tests: Relyper Budget plans and restores from the portal.
package portal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/appexport"
	"github.com/garethgeorge/backrest/internal/relyper/hookpolicy"
	"google.golang.org/protobuf/proto"
)

type fakeBudget struct {
	users    []appexport.BudgetUser
	imported map[string]string // user -> body
	fail     error
}

func (b *fakeBudget) Users(context.Context) ([]appexport.BudgetUser, error) { return b.users, nil }

func (b *fakeBudget) Import(_ context.Context, user string, r io.Reader) ([]appexport.Created, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if b.fail != nil {
		return nil, b.fail
	}
	b.imported[user] = string(data)
	return []appexport.Created{{ID: "new-1", Name: "Haushalt (restored 2026-11-28)", From: "old-1"}}, nil
}

type dumpCall struct{ repo, snap, file string }

func (f *fixture) withApps(t *testing.T) (*Apps, *fakeBudget, *[]dumpCall) {
	fb := &fakeBudget{users: []appexport.BudgetUser{{ID: "u-acme-1234", UserName: "kunde@acme.example"}, {ID: "u-beta-5678", UserName: "x@beta.example"}}, imported: map[string]string{}}
	var calls []dumpCall
	a := &Apps{Store: f.store, Config: func() (*v1.Config, error) { return f.cfg, nil }, Budget: fb,
		ExportBase: "/data/relyper/app-exports", Command: "relyper-appexport",
		Transform: func(fn func(cfg *v1.Config) error) error {
			c := proto.Clone(f.cfg).(*v1.Config)
			if err := fn(c); err != nil {
				return err
			}
			f.cfg = c
			return nil
		},
		Dump: func(_ context.Context, repo, snap, file string, w io.Writer) error {
			calls = append(calls, dumpCall{repo, snap, file})
			_, err := io.WriteString(w, "tar-of-"+snap)
			return err
		}}
	a.ExportBase = t.TempDir()
	f.h.d.Apps = a
	return a, fb, &calls
}

func TestBudgetPlan(t *testing.T) {
	f := newFixture(t)
	a, _, _ := f.withApps(t)
	ctx := context.Background()
	if _, err := a.CreateBudgetPlan(ctx, "acme", BudgetPlanRequest{User: "u-acme-1234", Repo: "beta-repo"}); err == nil {
		t.Fatal("repository of another tenant accepted")
	}
	if _, err := a.CreateBudgetPlan(ctx, "acme", BudgetPlanRequest{User: "unknown", Repo: "acme-repo"}); err == nil {
		t.Fatal("unknown Budget user accepted")
	}
	p, err := a.CreateBudgetPlan(ctx, "acme", BudgetPlanRequest{User: "u-acme-1234", Repo: "acme-repo"})
	if err != nil {
		t.Fatal(err)
	}
	if p.GetId() != "t-acme-budget-uacme123" {
		t.Fatalf("plan id %q", p.GetId())
	}
	hook := p.GetHooks()[0].GetActionCommand().GetCommand()
	t.Setenv("RELYPER_HOOK_COMMANDS", "dbdump")
	if err := hookpolicy.Check(hook); err != nil {
		t.Fatalf("hook %q refused by the strict policy: %v", hook, err)
	}
	if p.GetHooks()[0].GetOnError() != v1.Hook_ON_ERROR_FATAL {
		t.Fatal("a failed export must fail the backup")
	}
	acme, _ := f.store.Get("acme")
	if !acme.HasPlan(p.GetId()) || acme.App(p.GetId()).User != "u-acme-1234" {
		t.Fatal("plan not linked to the tenant")
	}
	// The same Budget user cannot be given to a second customer.
	f.store.AssignRepo("beta", "ops-repo")
	if _, err := a.CreateBudgetPlan(ctx, "beta", BudgetPlanRequest{User: "u-acme-1234", Repo: "ops-repo"}); err == nil || !strings.Contains(err.Error(), "acme") {
		t.Fatalf("user of acme given to beta: %v", err)
	}

	// Overview shows the plan as a Budget plan.
	_, ov, _ := f.call(t, "acme:backup_customer", http.MethodGet, "overview", nil)
	found := false
	for _, x := range ov["plans"].([]any) {
		pv := x.(map[string]any)
		if pv["id"] == p.GetId() {
			found = pv["app"] == "budget" && pv["appUser"] == "kunde@acme.example"
		}
	}
	if !found {
		t.Fatalf("budget plan not shown: %v", ov["plans"])
	}
}

func TestBudgetRestore(t *testing.T) {
	f := newFixture(t)
	a, fb, calls := f.withApps(t)
	p, err := a.CreateBudgetPlan(context.Background(), "acme", BudgetPlanRequest{User: "u-acme-1234", Repo: "acme-repo"})
	if err != nil {
		t.Fatal(err)
	}
	f.addSnapshot(t, p.GetId(), "g-acme", "aa11", f.now)
	f.addSnapshot(t, "beta-db", "g-beta", "bb22", f.now)
	snap := "aa11" + strings.Repeat("0", 60)
	foreign := "bb22" + strings.Repeat("0", 60)
	path := "plans/" + p.GetId() + "/restore"

	if code, _, _ := f.call(t, "acme:backup_customer", http.MethodPost, path, map[string]string{"snapshot": snap}); code != http.StatusForbidden {
		t.Fatalf("view role may restore: %d", code)
	}
	if code, _, _ := f.call(t, "acme:backup_customer_admin", http.MethodPost, path, map[string]string{"snapshot": foreign}); code != http.StatusNotFound {
		t.Fatalf("snapshot of another plan: %d", code)
	}
	if code, _, _ := f.call(t, "beta:backup_customer_admin", http.MethodPost, path, map[string]string{"snapshot": snap}); code != http.StatusNotFound {
		t.Fatalf("other tenant: %d", code)
	}
	if code, _, _ := f.call(t, "acme:backup_customer_admin", http.MethodPost, "plans/acme-web/restore", map[string]string{"snapshot": snap}); code != http.StatusNotFound {
		t.Fatalf("plan without app link: %d", code)
	}
	code, out, body := f.call(t, "acme:backup_customer_admin", http.MethodPost, path, map[string]string{"snapshot": snap})
	if code != http.StatusOK {
		t.Fatalf("restore: %d %s", code, body)
	}
	if fb.imported["u-acme-1234"] != "tar-of-"+snap {
		t.Fatalf("import got %q", fb.imported["u-acme-1234"])
	}
	if len(*calls) != 1 || (*calls)[0].repo != "acme-repo" || !strings.HasSuffix((*calls)[0].file, "/budget-u-acme-1234/budget-export.tar") {
		t.Fatalf("dump calls %+v", *calls)
	}
	if names := out["names"].([]any); len(names) != 1 {
		t.Fatalf("names %v", names)
	}

	fb.fail = errors.New("Relyper Budget: checksum mismatch")
	if code, _, body := f.call(t, "acme:backup_customer_admin", http.MethodPost, path, map[string]string{"snapshot": snap}); code != http.StatusBadGateway || !strings.Contains(body, "checksum") {
		t.Fatalf("failed import: %d %s", code, body)
	}
}

func TestCopiedBudgetPlanKeepsLink(t *testing.T) {
	f := newFixture(t)
	a, _, _ := f.withApps(t)
	st := f.withStorage(t)
	p, err := a.CreateBudgetPlan(context.Background(), "acme", BudgetPlanRequest{User: "u-acme-1234", Repo: "acme-repo"})
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Repos = append(f.cfg.Repos, &v1.Repo{Id: "t-acme-own", Guid: "g-own", Uri: "s3:https://s3.example.com/own"})
	f.store.AddOwnRepo("acme", "t-acme-own")
	acme, _ := f.store.Get("acme")
	c, err := st.CopyPlan(acme, p.GetId(), "t-acme-own", "budget-own")
	if err != nil {
		t.Fatal(err)
	}
	acme, _ = f.store.Get("acme")
	if l := acme.App(c.GetId()); l == nil || l.User != "u-acme-1234" || !acme.HasPlan(c.GetId()) {
		t.Fatalf("copy lost its link: %+v", acme.Apps)
	}
	// Removing a plan from the tenant removes its link.
	f.store.UnassignPlan("acme", c.GetId())
	acme, _ = f.store.Get("acme")
	if acme.App(c.GetId()) != nil {
		t.Fatal("link kept after unassigning the plan")
	}
}
