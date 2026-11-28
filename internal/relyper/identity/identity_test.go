// Relyper-owned tests for sign-in through Relyper Identity against a fake
// OpenID Connect provider.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP is a minimal OpenID provider: discovery, JWKS, token endpoint.
// The authorize step is simulated by the test reading the redirect.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]fakeCode
	claims map[string]any // extra or overriding claims for the next token
	alg    string         // signing alg override for the next token
	nonce  string         // overrides the nonce if set
	// authMethods is announced in discovery; the token endpoint only
	// accepts the first one.
	authMethods []string
}

type fakeCode struct{ challenge, nonce, redirect string }

const (
	testClientID     = "relyper-backup"
	testClientSecret = "s3cret-value-for-tests"
)

func newFakeIdP(t *testing.T) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, codes: map[string]fakeCode{}, authMethods: []string{"client_secret_basic", "client_secret_post"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "RelyperBackup/") {
			http.Error(w, "challenge", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": f.authMethods,
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if f.authMethods[0] == "client_secret_post" {
			if ok {
				id, secret = "", ""
			} else {
				id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
			}
		}
		if id != testClientID || secret != testClientSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		f.mu.Lock()
		c, found := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !found || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge || r.Form.Get("redirect_uri") != c.redirect {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": f.idToken(t, c.nonce)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) idToken(t *testing.T, nonce string) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": f.srv.URL, "aud": testClientID, "sub": "user-123", "email": "jonas@example.com", "name": "Jonas",
		"roles": []string{"backup_user"}, "tenant": "relyper", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": nonce,
	}
	if f.nonce != "" {
		claims["nonce"] = f.nonce
	}
	for k, v := range f.claims {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	if f.alg == "HS256" {
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString([]byte("public-key-as-secret"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// authorize simulates the provider's login page: it takes the authorization
// URL, checks PKCE and returns the callback URL with a code.
func (f *fakeIdP) authorize(t *testing.T, authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization request lacks PKCE, nonce or state: %s", authURL)
	}
	if q.Get("client_id") != testClientID || strings.Contains(authURL, testClientSecret) {
		t.Fatalf("bad client parameters in %s", authURL)
	}
	code := randomString(16)
	f.mu.Lock()
	f.codes[code] = fakeCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri")}
	f.mu.Unlock()
	cb, _ := url.Parse(q.Get("redirect_uri"))
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", q.Get("state"))
	cb.RawQuery = cq.Encode()
	return cb.String()
}

type env struct {
	idp     *fakeIdP
	svc     *Service
	app     *httptest.Server
	browser *http.Client
}

func newEnv(t *testing.T) *env {
	idp := newFakeIdP(t)
	cfg, err := ConfigFromEnv(func(k string) string {
		return map[string]string{
			"RELYPER_OIDC_ISSUER": idp.srv.URL, "RELYPER_OIDC_CLIENT_ID": testClientID, "RELYPER_OIDC_CLIENT_SECRET": testClientSecret,
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{idp: idp}
	mux := http.NewServeMux()
	e.app = httptest.NewServer(mux)
	t.Cleanup(e.app.Close)
	e.svc, err = New(Options{Config: cfg, DataDir: t.TempDir(), Version: "test", BaseURL: func(*http.Request) string { return e.app.URL }})
	if err != nil {
		t.Fatal(err)
	}
	for p, h := range e.svc.Routes() {
		mux.Handle(p, h)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	jar, _ := cookiejar.New(nil)
	e.browser = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

func (e *env) get(t *testing.T, u string) *http.Response {
	resp, err := e.browser.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// startLogin follows /auth/oidc/login and returns the provider URL.
func (e *env) startLogin(t *testing.T) string {
	resp := e.get(t, e.app.URL+PathLogin)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login: status %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, e.idp.srv.URL+"/authorize") {
		t.Fatalf("login redirects to %q", loc)
	}
	return loc
}

func (e *env) handoff(t *testing.T) (int, map[string]string) {
	req, _ := http.NewRequest(http.MethodPost, e.app.URL+PathSession, nil)
	req.Header.Set("X-Relyper-Login", "1")
	resp, err := e.browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestWebUILogin(t *testing.T) {
	e := newEnv(t)
	cb := e.idp.authorize(t, e.startLogin(t))
	resp := e.get(t, cb)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/?relyper_login=1" {
		t.Fatalf("callback: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	code, out := e.handoff(t)
	if code != http.StatusOK || out["token"] == "" || out["user"] != "relyper:jonas@example.com" {
		t.Fatalf("handoff: %d %v", code, out)
	}
	ctx, user, ok := e.svc.VerifySession(context.Background(), out["token"])
	if !ok || user.GetName() != "relyper:jonas@example.com" {
		t.Fatalf("session not accepted: %v %v", ok, user)
	}
	if e.svc.IsAdmin(ctx) {
		t.Fatal("backup_user must not be admin")
	}
	// The handoff is single use.
	if code, _ := e.handoff(t); code != http.StatusUnauthorized {
		t.Fatalf("second handoff: %d", code)
	}
	// The callback cannot be replayed.
	resp = e.get(t, cb)
	if !strings.Contains(resp.Header.Get("Location"), "relyper_login_error=invalid_state") {
		t.Fatalf("replayed callback: %q", resp.Header.Get("Location"))
	}
}

func TestClientSecretPost(t *testing.T) {
	e := newEnv(t)
	e.idp.authMethods = []string{"client_secret_post"}
	if resp := e.get(t, e.idp.authorize(t, e.startLogin(t))); resp.Header.Get("Location") != "/?relyper_login=1" {
		t.Fatalf("callback: %q", resp.Header.Get("Location"))
	}
}

func TestAdminRole(t *testing.T) {
	e := newEnv(t)
	e.idp.claims = map[string]any{"roles": "backup_admin other"}
	e.get(t, e.idp.authorize(t, e.startLogin(t)))
	_, out := e.handoff(t)
	ctx, _, ok := e.svc.VerifySession(context.Background(), out["token"])
	if !ok || !e.svc.IsAdmin(ctx) {
		t.Fatal("backup_admin should be admin")
	}
	if !e.svc.IsAdmin(context.Background()) {
		t.Fatal("local users stay admin")
	}
}

func TestLoginRefused(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *fakeIdP)
		expect string
	}{
		{"missing role", func(f *fakeIdP) { f.claims = map[string]any{"roles": []string{"other"}} }, "forbidden"},
		{"wrong audience", func(f *fakeIdP) { f.claims = map[string]any{"aud": "someone-else"} }, "invalid_id_token"},
		{"wrong issuer", func(f *fakeIdP) { f.claims = map[string]any{"iss": "https://evil.example"} }, "invalid_id_token"},
		{"expired", func(f *fakeIdP) { f.claims = map[string]any{"exp": time.Now().Add(-10 * time.Minute).Unix()} }, "invalid_id_token"},
		{"wrong nonce", func(f *fakeIdP) { f.nonce = "other" }, "invalid_id_token"},
		{"HS256 signature", func(f *fakeIdP) { f.alg = "HS256" }, "invalid_id_token"},
		{"azp other client", func(f *fakeIdP) { f.claims = map[string]any{"azp": "other"} }, "invalid_id_token"},
		{"no email", func(f *fakeIdP) { f.claims = map[string]any{"email": nil} }, "missing_email"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e.idp)
			resp := e.get(t, e.idp.authorize(t, e.startLogin(t)))
			if got := resp.Header.Get("Location"); got != "/?relyper_login_error="+c.expect {
				t.Fatalf("got %q, want error %s", got, c.expect)
			}
			if code, _ := e.handoff(t); code != http.StatusUnauthorized {
				t.Fatalf("handoff after failed login: %d", code)
			}
		})
	}
}

func TestCallbackNeedsSameBrowser(t *testing.T) {
	e := newEnv(t)
	cb := e.idp.authorize(t, e.startLogin(t))
	other := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := other.Get(cb)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Location"), "invalid_state") {
		t.Fatalf("callback from another browser: %q", resp.Header.Get("Location"))
	}
}

func TestSessionRejections(t *testing.T) {
	e := newEnv(t)
	e.get(t, e.idp.authorize(t, e.startLogin(t)))
	_, out := e.handoff(t)
	tok := out["token"]
	if _, _, ok := e.svc.VerifySession(context.Background(), tok+"x"); ok {
		t.Fatal("tampered token accepted")
	}
	// Expired sessions are refused.
	e.svc.now = func() time.Time { return time.Now().Add(9 * time.Hour) }
	if _, _, ok := e.svc.VerifySession(context.Background(), tok); ok {
		t.Fatal("expired session accepted")
	}
	e.svc.now = time.Now
	// A role removed from the settings is enforced on the next request.
	e.svc.cfg.LoginRole = "something_else"
	if _, _, ok := e.svc.VerifySession(context.Background(), tok); ok {
		t.Fatal("session accepted without the login role")
	}
	// Handoff needs the custom header.
	req, _ := http.NewRequest(http.MethodPost, e.app.URL+PathSession, nil)
	resp, err := e.browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("handoff without header: %d", resp.StatusCode)
	}
}

func TestConsentFlow(t *testing.T) {
	e := newEnv(t)
	var gotRef string
	var gotID *Identity
	e.svc.SetConsentCompleter(func(w http.ResponseWriter, r *http.Request, ref string, id *Identity, err *Error) {
		gotRef, gotID = ref, id
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, e.app.URL+"/oauth/authorize", nil)
	e.svc.Start(rec, req, FlowConsent, "consent-1")
	u, _ := url.Parse(e.app.URL)
	e.browser.Jar.SetCookies(u, rec.Result().Cookies())
	resp := e.get(t, e.idp.authorize(t, rec.Header().Get("Location")))
	if resp.StatusCode != http.StatusNoContent || gotRef != "consent-1" || gotID == nil || gotID.Email != "jonas@example.com" {
		t.Fatalf("consent: %d %q %v", resp.StatusCode, gotRef, gotID)
	}
}

func TestConfig(t *testing.T) {
	if c, err := ConfigFromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatal("no issuer must mean off")
	}
	bad := []map[string]string{
		{"RELYPER_OIDC_ISSUER": "http://idp.example", "RELYPER_OIDC_CLIENT_ID": "a", "RELYPER_OIDC_CLIENT_SECRET": "b"},
		{"RELYPER_OIDC_ISSUER": "https://idp.example", "RELYPER_OIDC_CLIENT_SECRET": "b"},
		{"RELYPER_OIDC_ISSUER": "https://idp.example", "RELYPER_OIDC_CLIENT_ID": "a"},
		{"RELYPER_OIDC_ISSUER": "https://idp.example", "RELYPER_OIDC_CLIENT_ID": "a", "RELYPER_OIDC_CLIENT_SECRET": "b", "RELYPER_OIDC_REDIRECT_URI": "https://b.example/other"},
		{"RELYPER_OIDC_ISSUER": "https://idp.example", "RELYPER_OIDC_CLIENT_ID": "a", "RELYPER_OIDC_CLIENT_SECRET": "b", "RELYPER_OIDC_SCOPES": "email"},
		{"RELYPER_OIDC_ISSUER": "https://idp.example", "RELYPER_OIDC_CLIENT_ID": "a", "RELYPER_OIDC_CLIENT_SECRET": "b", "RELYPER_OIDC_SESSION_HOURS": "0"},
	}
	for i, m := range bad {
		if _, err := ConfigFromEnv(func(k string) string { return m[k] }); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
}

func TestOperatorRolesOnlyInOperatorTenant(t *testing.T) {
	// An operator role in a customer's tenant does not make an operator.
	e := newEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_admin"}, "tenant": "acme"}
	e.get(t, e.idp.authorize(t, e.startLogin(t)))
	if code, _ := e.handoff(t); code == http.StatusOK {
		t.Fatal("backup_admin of tenant acme signed in as operator")
	}

	// Without any tenant claim neither.
	e = newEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_admin"}, "tenant": nil}
	e.get(t, e.idp.authorize(t, e.startLogin(t)))
	if code, _ := e.handoff(t); code == http.StatusOK {
		t.Fatal("backup_admin without tenant signed in as operator")
	}

	// The provider's master-tenant flag is accepted.
	e = newEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_admin"}, "tenant": "1", "relyper_is_master_tenant": true}
	e.get(t, e.idp.authorize(t, e.startLogin(t)))
	if code, _ := e.handoff(t); code != http.StatusOK {
		t.Fatal("master tenant flag refused")
	}

	// tenant_id is accepted as well.
	e = newEnv(t)
	e.idp.claims = map[string]any{"roles": []string{"backup_admin"}, "tenant": nil, "tenant_id": "relyper"}
	e.get(t, e.idp.authorize(t, e.startLogin(t)))
	_, out := e.handoff(t)
	if ctx, _, ok := e.svc.VerifySession(context.Background(), out["token"]); !ok || !e.svc.IsAdmin(ctx) {
		t.Fatal("backup_admin of the operator tenant should be admin")
	}
}

func TestOperatorTenantConfig(t *testing.T) {
	base := map[string]string{"RELYPER_OIDC_ISSUER": "https://idp.example", "RELYPER_OIDC_CLIENT_ID": "c", "RELYPER_OIDC_CLIENT_SECRET": "s"}
	c, err := ConfigFromEnv(func(k string) string { return base[k] })
	if err != nil || c.OperatorTenant != "relyper" {
		t.Fatalf("default operator tenant %q, %v", c.OperatorTenant, err)
	}
	base["RELYPER_OIDC_OPERATOR_TENANT"] = "*"
	c, _ = ConfigFromEnv(func(k string) string { return base[k] })
	cl := &client{cfg: c}
	if got, _ := cl.operatorScoped([]string{"backup_admin"}, map[string]any{}); len(got) != 1 {
		t.Fatalf("* should keep operator roles, got %v", got)
	}
}
