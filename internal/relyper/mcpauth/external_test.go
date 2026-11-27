// Relyper-owned tests for signing in on the consent page through an
// external identity provider.
package mcpauth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garethgeorge/backrest/internal/relyper/audit"
)

func newExternalOAuth(t *testing.T, passwordUsers bool) (*OAuth, *Client, *string) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	started := new(string)
	o := &OAuth{
		Store: store, Audit: log, MCPPath: "/mcp",
		Login:          func(string, string) error { return io.EOF },
		LoginAvailable: func() bool { return passwordUsers },
		External: &External{Name: "Relyper", Start: func(w http.ResponseWriter, r *http.Request, id string) {
			*started = id
			http.Redirect(w, r, "https://idp.example/authorize", http.StatusFound)
		}},
	}
	o.Init()
	c, err := store.RegisterClient("Claude", []string{"https://claude.ai/api/mcp/auth_callback"})
	if err != nil {
		t.Fatal(err)
	}
	return o, c, started
}

func consentForm(o *OAuth, c *Client, decision string, scopes ...string) url.Values {
	p := authzParams{ClientID: c.ID, RedirectURI: c.RedirectURIs[0], State: "st", Scope: "status:read", CodeChallenge: pkceS256("verifier-verifier-verifier-verifier-verifier")}
	v := url.Values{
		"client_id": {p.ClientID}, "redirect_uri": {p.RedirectURI}, "state": {p.State}, "scope": {p.Scope},
		"code_challenge": {p.CodeChallenge}, "form_token": {p.sign(o.formKey, 4102444800)}, "decision": {decision},
	}
	v["scope_choice"] = scopes
	return v
}

func post(o *OAuth, v url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "https://backup.example/oauth/authorize", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	o.authorize(rec, req)
	return rec
}

func TestConsentPageOffersExternal(t *testing.T) {
	o, c, _ := newExternalOAuth(t, false)
	q := url.Values{"client_id": {c.ID}, "redirect_uri": {c.RedirectURIs[0]}, "response_type": {"code"}, "code_challenge": {"x"}, "code_challenge_method": {"S256"}, "state": {"s"}}
	rec := httptest.NewRecorder()
	o.authorize(rec, httptest.NewRequest(http.MethodGet, "https://backup.example/oauth/authorize?"+q.Encode(), nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Sign in with Relyper and allow") {
		t.Fatal("button missing")
	}
	if strings.Contains(body, `name="password"`) {
		t.Fatal("password field shown although no local user exists")
	}
	// Password sign-in is refused when no local user exists.
	if rec := post(o, consentForm(o, c, "approve", "status:read")); rec.Code != http.StatusBadRequest {
		t.Fatalf("password approve: %d", rec.Code)
	}
}

func TestExternalConsentIssuesCode(t *testing.T) {
	o, c, started := newExternalOAuth(t, true)
	rec := post(o, consentForm(o, c, "approve_external", "status:read", "backups:run"))
	if rec.Code != http.StatusFound || *started == "" {
		t.Fatalf("start: %d %q", rec.Code, *started)
	}
	done := httptest.NewRecorder()
	o.CompleteExternal(done, httptest.NewRequest(http.MethodGet, "https://backup.example/auth/oidc/callback", nil), *started, "relyper:jonas@example.com", "")
	loc, _ := url.Parse(done.Header().Get("Location"))
	if done.Code != http.StatusFound || loc.Host != "claude.ai" || loc.Query().Get("code") == "" || loc.Query().Get("state") != "st" {
		t.Fatalf("complete: %d %s", done.Code, loc)
	}
	o.mu.Lock()
	var granted []string
	for _, ac := range o.codes {
		granted = ac.scopes
		if ac.user != "relyper:jonas@example.com" {
			t.Errorf("code user %q", ac.user)
		}
	}
	o.mu.Unlock()
	if strings.Join(granted, " ") != "status:read backups:run" {
		t.Fatalf("granted %v", granted)
	}
	// The pending consent is single use.
	again := httptest.NewRecorder()
	o.CompleteExternal(again, httptest.NewRequest(http.MethodGet, "/", nil), *started, "relyper:jonas@example.com", "")
	if again.Code != http.StatusBadRequest {
		t.Fatalf("reuse: %d", again.Code)
	}
}

func TestExternalConsentFailure(t *testing.T) {
	o, c, started := newExternalOAuth(t, true)
	post(o, consentForm(o, c, "approve_external", "status:read"))
	rec := httptest.NewRecorder()
	o.CompleteExternal(rec, httptest.NewRequest(http.MethodGet, "/", nil), *started, "", "Your account has no access.")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Your account has no access.") || rec.Header().Get("Location") != "" {
		t.Fatalf("failure: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Without a permission, nothing is started.
	*started = ""
	if rec := post(o, consentForm(o, c, "approve_external")); rec.Code != http.StatusBadRequest || *started != "" {
		t.Fatalf("no scopes: %d", rec.Code)
	}
}
