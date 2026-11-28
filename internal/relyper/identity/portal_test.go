// Relyper-owned tests for customer portal sessions.
package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

// fakeGate admits identities with role backup_customer and tenant claim "acme".
type fakeGate struct{}

func (fakeGate) Admit(id *Identity) (Membership, bool) {
	if !slices.Contains(id.Roles, "backup_customer") || id.Claims["tenant"] != "acme" {
		return Membership{}, false
	}
	return Membership{Tenant: "acme", External: "acme"}, true
}

func newPortalEnv(t *testing.T) *env {
	e := newEnv(t)
	e.svc.opt.Portal = fakeGate{}
	e.svc.client.extraLogin = func(id *Identity) bool { _, ok := fakeGate{}.Admit(id); return ok }
	return e
}

func TestPortalLogin(t *testing.T) {
	e := newPortalEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_customer"}, "tenant": "acme", "email": "kunde@acme.example"}
	resp := e.get(t, e.idp.authorize(t, e.startLogin(t)))
	if got := resp.Header.Get("Location"); got != "/?relyper_login=1#/portal" {
		t.Fatalf("callback redirects to %q", got)
	}
	code, out := e.handoff(t)
	if code != http.StatusOK || out["kind"] != "portal" || out["token"] == "" {
		t.Fatalf("handoff: %d %v", code, out)
	}
	p, ok := e.svc.VerifyPortal(out["token"])
	if !ok || p.Tenant != "acme" || p.Email != "kunde@acme.example" {
		t.Fatalf("portal session: %v %+v", ok, p)
	}
	// A portal token is never an operator session.
	if _, _, ok := e.svc.VerifySession(context.Background(), out["token"]); ok {
		t.Fatal("portal token accepted as operator session")
	}
}

func TestOperatorTokenIsNoPortalSession(t *testing.T) {
	e := newPortalEnv(t)
	// In the operator tenant, operator roles win over customer roles.
	e.idp.claims = map[string]any{"roles": []string{"backup_user", "backup_customer"}, "tenant": "relyper"}
	resp := e.get(t, e.idp.authorize(t, e.startLogin(t)))
	if got := resp.Header.Get("Location"); got != "/?relyper_login=1" {
		t.Fatalf("callback redirects to %q", got)
	}
	_, out := e.handoff(t)
	if out["kind"] != "operator" {
		t.Fatalf("kind %q", out["kind"])
	}
	if _, ok := e.svc.VerifyPortal(out["token"]); ok {
		t.Fatal("operator token accepted by the portal")
	}
}

func TestOperatorRoleInCustomerTenantIsCustomer(t *testing.T) {
	e := newPortalEnv(t)
	// An operator role in a customer's tenant is ignored: portal only.
	e.idp.claims = map[string]any{"roles": []string{"backup_admin", "backup_customer"}, "tenant": "acme"}
	resp := e.get(t, e.idp.authorize(t, e.startLogin(t)))
	if got := resp.Header.Get("Location"); got != "/?relyper_login=1#/portal" {
		t.Fatalf("callback redirects to %q", got)
	}
	_, out := e.handoff(t)
	if out["kind"] != "portal" {
		t.Fatalf("kind %q", out["kind"])
	}
	if _, _, ok := e.svc.VerifySession(context.Background(), out["token"]); ok {
		t.Fatal("customer became operator")
	}
}

func TestPortalRefusesUnknownTenant(t *testing.T) {
	e := newPortalEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_customer"}, "tenant": "other"}
	resp := e.get(t, e.idp.authorize(t, e.startLogin(t)))
	if got := resp.Header.Get("Location"); got != "/?relyper_login_error=forbidden" {
		t.Fatalf("got %q", got)
	}
	// Started from the portal, the error goes back to the portal.
	pe := newPortalEnv(t)
	pe.idp.claims = map[string]any{"roles": []string{"backup_customer"}, "tenant": "other"}
	r := pe.get(t, pe.app.URL+PathLogin+"?portal=1")
	resp = pe.get(t, pe.idp.authorize(t, r.Header.Get("Location")))
	if got := resp.Header.Get("Location"); got != "/?relyper_login_error=forbidden#/portal" {
		t.Fatalf("got %q", got)
	}
}

func TestPortalTokenTampering(t *testing.T) {
	e := newPortalEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_customer"}, "tenant": "acme"}
	e.get(t, e.idp.authorize(t, e.startLogin(t)))
	_, out := e.handoff(t)
	if _, ok := e.svc.VerifyPortal(out["token"] + "x"); ok {
		t.Fatal("tampered token accepted")
	}
	// A session signed with the operator key and portal claims is refused.
	other := &Service{cfg: e.svc.cfg, portalKey: e.svc.key, now: e.svc.now}
	tok, err := other.issuePortalSession(&Identity{Subject: "s", Email: "a@b.c"}, Membership{Tenant: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.svc.VerifyPortal(tok); ok {
		t.Fatal("portal claims signed with the operator key accepted")
	}
}

func TestConsentRefusesCustomers(t *testing.T) {
	e := newPortalEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_customer"}, "tenant": "acme"}
	var gotErr *Error
	e.svc.SetConsentCompleter(func(w http.ResponseWriter, r *http.Request, ref string, id *Identity, err *Error) {
		gotErr = err
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, e.app.URL+"/oauth/authorize", nil)
	e.svc.Start(rec, req, FlowConsent, "consent-1")
	u, _ := url.Parse(e.app.URL)
	e.browser.Jar.SetCookies(u, rec.Result().Cookies())
	e.get(t, e.idp.authorize(t, rec.Header().Get("Location")))
	if gotErr == nil || gotErr.Code != "forbidden" {
		t.Fatalf("customer passed the MCP consent: %v", gotErr)
	}
}
