// Relyper-owned tests for the tenant store and the portal gate.
package tenant

import (
	"path/filepath"
	"testing"

	"github.com/garethgeorge/backrest/internal/relyper/identity"
)

func newStore(t *testing.T) *Store {
	s, err := Open(filepath.Join(t.TempDir(), "tenants.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStorePersistsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenants.json")
	s, _ := Open(path)
	for _, bad := range []string{"", "A", "ab", "-abc", "abc-", "a--b", "has space"} {
		if _, err := s.Create(bad, "Name", nil); err == nil {
			t.Errorf("id %q accepted", bad)
		}
	}
	if _, err := s.Create("acme", "", nil); err == nil {
		t.Error("empty name accepted")
	}
	if _, err := s.Create("acme", "ACME\nGmbH", nil); err == nil {
		t.Error("control character accepted")
	}
	if _, err := s.Create("acme", "ACME GmbH", []string{"tenant-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("acme", "again", nil); err == nil {
		t.Error("duplicate id accepted")
	}
	if _, err := s.Create("beta", "Beta", []string{"tenant-1"}); err == nil {
		t.Error("external id of another tenant accepted")
	}
	if _, err := s.Create("tenant-1", "Clash", nil); err == nil {
		t.Error("id equal to another tenant's external id accepted")
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get("acme")
	if err != nil || got.Name != "ACME GmbH" || !got.Notify.Has(EventFailure) || got.Notify.Has(EventSuccess) {
		t.Fatalf("reloaded: %+v %v", got, err)
	}
}

func TestAssignmentsAreExclusive(t *testing.T) {
	s := newStore(t)
	s.Create("acme", "ACME", nil)
	s.Create("beta", "Beta", nil)
	if _, err := s.AssignPlan("acme", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignPlan("beta", "p1"); err == nil {
		t.Fatal("plan assigned to two tenants")
	}
	if _, err := s.AssignRepo("acme", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignRepo("beta", "r1"); err == nil {
		t.Fatal("repo assigned to two tenants")
	}
	if o, ok := s.OwnerOfPlan("p1"); !ok || o.ID != "acme" {
		t.Fatal("owner of plan")
	}
	n, err := s.Prune(map[string]bool{}, map[string]bool{"r1": true})
	if err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if _, ok := s.OwnerOfPlan("p1"); ok {
		t.Fatal("deleted plan still assigned")
	}
}

func TestCleanNotify(t *testing.T) {
	ok, err := CleanNotify(Notify{Recipients: []string{" a@b.example ", "a@b.example"}, Events: []string{EventSuccess}})
	if err != nil || len(ok.Recipients) != 1 {
		t.Fatalf("%v %v", ok, err)
	}
	for _, bad := range []string{"no-at", "Name <a@b.example>", "a@b.example\r\nBcc: x@y.example", "a@b.example, c@d.example"} {
		if _, err := CleanNotify(Notify{Recipients: []string{bad}}); err == nil {
			t.Errorf("recipient %q accepted", bad)
		}
	}
	if _, err := CleanNotify(Notify{Events: []string{"everything"}}); err == nil {
		t.Error("unknown event accepted")
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = string(rune('a'+i)) + "@x.example"
	}
	if _, err := CleanNotify(Notify{Recipients: many}); err == nil {
		t.Error("11 recipients accepted")
	}
}

func TestGate(t *testing.T) {
	s := newStore(t)
	s.Create("acme", "ACME", []string{"t-100"})
	s.Create("beta", "Beta", nil)
	g := &Gate{Store: s, Cfg: PortalConfigFromEnv(func(string) string { return "" })}
	id := func(roles []string, claim any) *identity.Identity {
		return &identity.Identity{Subject: "s", Email: "a@b.example", Roles: roles, Claims: map[string]any{"tenant": claim}}
	}
	cases := []struct {
		name   string
		id     *identity.Identity
		tenant string
		admin  bool
	}{
		{"string claim", id([]string{"backup_customer"}, "t-100"), "acme", false},
		{"object claim", id([]string{"backup_customer_admin"}, map[string]any{"id": "t-100", "name": "x"}), "acme", true},
		{"array claim", id([]string{"backup_customer"}, []any{"unknown", "beta"}), "beta", false},
		{"internal id is not enough when external ids are set", id([]string{"backup_customer"}, "acme"), "", false},
		{"no role", id([]string{"other"}, "t-100"), "", false},
		{"no claim", id([]string{"backup_customer"}, nil), "", false},
		{"ambiguous", id([]string{"backup_customer"}, []any{"t-100", "beta"}), "", false},
	}
	for _, c := range cases {
		m, ok := g.Admit(c.id)
		if ok != (c.tenant != "") || m.Tenant != c.tenant || m.Admin != c.admin {
			t.Errorf("%s: got %+v %v", c.name, m, ok)
		}
	}
	// Suspension and removed external IDs take effect on the next request.
	m, _ := g.Admit(id([]string{"backup_customer"}, "t-100"))
	sess := &identity.PortalSession{Tenant: m.Tenant, External: m.External, Roles: []string{"backup_customer"}}
	if _, _, ok := g.Check(sess); !ok {
		t.Fatal("valid session refused")
	}
	yes := true
	s.Configure("acme", Settings{Suspended: &yes})
	if _, _, ok := g.Check(sess); ok {
		t.Fatal("suspended tenant accepted")
	}
	no := false
	s.Configure("acme", Settings{Suspended: &no, ExternalIDs: &[]string{"t-200"}})
	if _, _, ok := g.Check(sess); ok {
		t.Fatal("removed external id accepted")
	}
}
