// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcpauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/audit"
)

// OAuth implements a small OAuth 2.1 authorization server for MCP clients
// that connect remotely (Claude, ChatGPT and others): authorization server
// and protected resource metadata (RFC 8414, RFC 9728), dynamic client
// registration (RFC 7591) for public clients, the authorization code flow
// with mandatory PKCE S256, and rotating refresh tokens.
//
// The user signs in on the consent page with their Relyper Backup user name
// and password and chooses the scopes. Access tokens are opaque and stored
// hashed in the Store, like personal access tokens.
type OAuth struct {
	Store *Store
	Audit *audit.Log
	// Login checks a user name and password; it returns an error if they are
	// wrong or authentication is not set up.
	Login func(user, password string) error
	// LoginAvailable reports whether users exist that can sign in.
	LoginAvailable func() bool
	// MCPPath is the path of the protected MCP endpoint, e.g. "/mcp".
	MCPPath string

	formKey []byte
	mu      sync.Mutex
	codes   map[string]*authCode
	fails   map[string][]time.Time
}

type authCode struct {
	clientID      string
	redirectURI   string
	codeChallenge string
	scopes        []string
	user          string
	resource      string
	expires       time.Time
}

const (
	codeLifetime = 5 * time.Minute
	formLifetime = 15 * time.Minute
)

// Init must be called before the handlers are used.
func (o *OAuth) Init() {
	o.formKey = make([]byte, 32)
	if _, err := rand.Read(o.formKey); err != nil {
		panic(err)
	}
	o.codes = map[string]*authCode{}
	o.fails = map[string][]time.Time{}
}

// Routes returns the unauthenticated HTTP routes of the authorization server.
func (o *OAuth) Routes() map[string]http.Handler {
	prm := http.HandlerFunc(o.protectedResourceMetadata)
	return map[string]http.Handler{
		"/.well-known/oauth-protected-resource":             prm,
		"/.well-known/oauth-protected-resource" + o.MCPPath: prm,
		"/.well-known/oauth-authorization-server":           http.HandlerFunc(o.serverMetadata),
		"/oauth/register":  http.HandlerFunc(o.register),
		"/oauth/authorize": http.HandlerFunc(o.authorize),
		"/oauth/token":     http.HandlerFunc(o.token),
	}
}

// BaseURL is the externally visible origin of the server. RELYPER_PUBLIC_URL
// wins; otherwise it is derived from the request (and the reverse proxy's
// X-Forwarded-* headers).
func BaseURL(r *http.Request) string {
	if u := strings.TrimRight(os.Getenv("RELYPER_PUBLIC_URL"), "/"); u != "" {
		return u
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return scheme + "://" + host
}

// ResourceMetadataURL is sent in WWW-Authenticate by the MCP endpoint.
func (o *OAuth) ResourceMetadataURL(r *http.Request) string {
	return BaseURL(r) + "/.well-known/oauth-protected-resource" + o.MCPPath
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, code int, errCode, desc string) {
	writeJSON(w, code, map[string]string{"error": errCode, "error_description": desc})
}

func cors(w http.ResponseWriter, r *http.Request) bool {
	// Metadata, registration and token endpoints are called from browser
	// based clients; they carry no cookies, so a wildcard origin is safe.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func (o *OAuth) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	base := BaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base + o.MCPPath,
		"authorization_servers":    []string{base},
		"scopes_supported":         ScopeNames(),
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Relyper Backup",
	})
}

func (o *OAuth) serverMetadata(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	base := BaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         base,
		"authorization_endpoint":                         base + "/oauth/authorize",
		"token_endpoint":                                 base + "/oauth/token",
		"registration_endpoint":                          base + "/oauth/register",
		"scopes_supported":                               ScopeNames(),
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"code_challenge_methods_supported":               []string{"S256"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// validRedirectURI accepts https URIs, http on loopback addresses (native
// clients such as Claude Code) and private-use schemes of desktop apps.
func validRedirectURI(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Fragment != "" || len(s) > 2000 {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		h := u.Hostname()
		ip := net.ParseIP(h)
		return h == "localhost" || (ip != nil && ip.IsLoopback())
	case "", "javascript", "data", "file", "vbscript", "about", "blob", "ftp", "ws", "wss":
		return false
	default:
		return true
	}
}

func (o *OAuth) register(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	var req struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "1 to 10 redirect_uris required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri must be https, http on localhost, or an app scheme")
			return
		}
	}
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		// Only public clients: PKCE protects the code, and no client secret
		// would have to be stored.
		req.TokenEndpointAuthMethod = "none"
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	c, err := o.Store.RegisterClient(name, req.RedirectURIs)
	if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", err.Error())
		return
	}
	o.Audit.Record(audit.Event{Actor: "client:" + c.ID, Source: "oauth", Action: "oauth.register", Outcome: "ok", Message: name, RemoteAddr: remoteAddr(r)})
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        c.CreatedAt.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

func remoteAddr(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// authzParams are the parameters of an authorization request.
type authzParams struct {
	ClientID, RedirectURI, State, Scope, CodeChallenge, Resource string
}

func (p authzParams) sign(key []byte, exp int64) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", p.ClientID, p.RedirectURI, p.State, p.Scope, p.CodeChallenge, p.Resource, exp)
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(m.Sum(nil))
}

func (p authzParams) verify(key []byte, tok string, now time.Time) bool {
	expS, _, ok := strings.Cut(tok, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(p.sign(key, exp)), []byte(tok))
}

func redirectWith(w http.ResponseWriter, r *http.Request, redirectURI string, q url.Values) {
	u, _ := url.Parse(redirectURI)
	vals := u.Query()
	for k, v := range q {
		vals[k] = v
	}
	u.RawQuery = vals.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (o *OAuth) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p := authzParams{
		ClientID:      r.Form.Get("client_id"),
		RedirectURI:   r.Form.Get("redirect_uri"),
		State:         r.Form.Get("state"),
		Scope:         r.Form.Get("scope"),
		CodeChallenge: r.Form.Get("code_challenge"),
		Resource:      r.Form.Get("resource"),
	}
	client := o.Store.GetClient(p.ClientID)
	if client == nil {
		o.page(w, http.StatusBadRequest, pageData{Error: "Unknown client. Remove the connector in your AI app and add it again."})
		return
	}
	if !slices.Contains(client.RedirectURIs, p.RedirectURI) {
		o.page(w, http.StatusBadRequest, pageData{Error: "The redirect address does not match the registered client."})
		return
	}
	// From here on errors are reported to the client via the redirect.
	fail := func(code, desc string) {
		redirectWith(w, r, p.RedirectURI, url.Values{"error": {code}, "error_description": {desc}, "state": {p.State}, "iss": {BaseURL(r)}})
	}
	if r.Form.Get("response_type") != "code" && r.Method == http.MethodGet {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	if p.CodeChallenge == "" || (r.Method == http.MethodGet && r.Form.Get("code_challenge_method") != "S256") {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	requested := strings.Fields(p.Scope)
	if len(requested) == 0 {
		requested = DefaultScopes
	}
	requested, err := NormalizeScopes(requested)
	if err != nil {
		fail("invalid_scope", err.Error())
		return
	}
	data := pageData{
		ClientName:   client.Name,
		RedirectHost: redirectHost(p.RedirectURI),
		Params:       p,
		FormToken:    p.sign(o.formKey, time.Now().Add(formLifetime).Unix()),
		NoLogin:      o.LoginAvailable != nil && !o.LoginAvailable(),
	}
	for _, s := range AllScopes {
		data.Scopes = append(data.Scopes, scopeChoice{ScopeInfo: s, Checked: slices.Contains(requested, s.Name)})
	}
	if r.Method == http.MethodGet {
		o.page(w, http.StatusOK, data)
		return
	}

	// POST: the consent form was submitted.
	if !p.verify(o.formKey, r.Form.Get("form_token"), time.Now()) {
		o.page(w, http.StatusBadRequest, pageData{Error: "The form expired. Start the connection again from your AI app."})
		return
	}
	if r.Form.Get("decision") != "approve" {
		o.Audit.Record(audit.Event{Actor: "client:" + client.ID, Source: "oauth", Action: "oauth.consent", Outcome: "denied", Message: client.Name, RemoteAddr: remoteAddr(r)})
		fail("access_denied", "the user denied access")
		return
	}
	if data.NoLogin {
		o.page(w, http.StatusForbidden, data)
		return
	}
	ip := remoteAddr(r)
	if o.tooManyFailures(ip) {
		data.Error = "Too many failed sign-ins. Wait a minute and try again."
		o.page(w, http.StatusTooManyRequests, data)
		return
	}
	user := r.Form.Get("username")
	if err := o.Login(user, r.Form.Get("password")); err != nil {
		o.recordFailure(ip)
		o.Audit.Record(audit.Event{Actor: "user:" + user, Source: "oauth", Action: "oauth.login", Outcome: "error", Message: "wrong user name or password", RemoteAddr: ip})
		data.Error = "Wrong user name or password."
		o.page(w, http.StatusUnauthorized, data)
		return
	}
	granted, err := NormalizeScopes(r.Form["scope_choice"])
	if err != nil || len(granted) == 0 {
		data.Error = "Choose at least one permission."
		o.page(w, http.StatusBadRequest, data)
		return
	}
	code := randomSecret("rbk_code_")
	o.mu.Lock()
	now := time.Now()
	for k, c := range o.codes {
		if now.After(c.expires) {
			delete(o.codes, k)
		}
	}
	o.codes[hashSecret(code)] = &authCode{clientID: client.ID, redirectURI: p.RedirectURI, codeChallenge: p.CodeChallenge, scopes: granted, user: user, resource: p.Resource, expires: now.Add(codeLifetime)}
	o.mu.Unlock()
	o.Audit.Record(audit.Event{Actor: "user:" + user, Source: "oauth", Action: "oauth.consent", Outcome: "ok", Message: client.Name + ": " + strings.Join(granted, " "), RemoteAddr: ip})
	redirectWith(w, r, p.RedirectURI, url.Values{"code": {code}, "state": {p.State}, "iss": {BaseURL(r)}})
}

func redirectHost(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	if u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + ":"
}

func (o *OAuth) tooManyFailures(ip string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	cut := time.Now().Add(-time.Minute)
	o.fails[ip] = slices.DeleteFunc(o.fails[ip], func(t time.Time) bool { return t.Before(cut) })
	return len(o.fails[ip]) >= 5
}

func (o *OAuth) recordFailure(ip string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fails[ip] = append(o.fails[ip], time.Now())
}

func pkceS256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (o *OAuth) token(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	clientID := r.PostForm.Get("client_id")
	if clientID == "" {
		if u, _, ok := r.BasicAuth(); ok {
			clientID = u
		}
	}
	client := o.Store.GetClient(clientID)
	if client == nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	var (
		t               *Token
		access, refresh string
		err             error
	)
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code := r.PostForm.Get("code")
		o.mu.Lock()
		ac := o.codes[hashSecret(code)]
		delete(o.codes, hashSecret(code)) // single use, even on failure
		o.mu.Unlock()
		switch {
		case ac == nil || time.Now().After(ac.expires):
			oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code")
			return
		case ac.clientID != client.ID || ac.redirectURI != r.PostForm.Get("redirect_uri"):
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code was issued to another client or redirect_uri")
			return
		case pkceS256(r.PostForm.Get("code_verifier")) != ac.codeChallenge:
			oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
		t, access, refresh, err = o.Store.IssueGrant(client, ac.scopes, ac.user, ac.resource)
	case "refresh_token":
		t, access, refresh, err = o.Store.Refresh(r.PostForm.Get("refresh_token"), client.ID)
		if errors.Is(err, ErrInvalidToken) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired refresh token")
			return
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(time.Until(t.ExpiresAt).Seconds()),
		"refresh_token": refresh,
		"scope":         strings.Join(t.Scopes, " "),
	})
}

type scopeChoice struct {
	ScopeInfo
	Checked bool
}

type pageData struct {
	Error        string
	ClientName   string
	RedirectHost string
	Params       authzParams
	FormToken    string
	Scopes       []scopeChoice
	NoLogin      bool
}

func (o *OAuth) page(w http.ResponseWriter, code int, d pageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(code)
	_ = consentTmpl.Execute(w, d)
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Relyper Backup – Connect</title>
<style>
:root{--bg:#f6f7f9;--panel:#fff;--fg:#1a1d21;--muted:#5b6470;--border:#d9dde3;--accent:#2563eb;--warn:#b45309;--err:#b91c1c}
@media (prefers-color-scheme:dark){:root{--bg:#111316;--panel:#1a1d21;--fg:#e8eaed;--muted:#9aa3ad;--border:#2e333a;--accent:#60a5fa;--warn:#f59e0b;--err:#f87171}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.45 system-ui,-apple-system,Segoe UI,Roboto,sans-serif}
main{max-width:520px;margin:32px auto;padding:0 16px}.card{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:24px}
h1{font-size:20px;margin:0 0 4px}p{margin:8px 0}.muted{color:var(--muted)}.err{color:var(--err);font-weight:600}
label{display:block}.scope{display:flex;gap:10px;align-items:flex-start;padding:8px 0;border-top:1px solid var(--border)}
.scope input{margin-top:4px}.tag{font-size:12px;color:var(--warn);font-weight:600}code{font-size:13px}
.field{margin:12px 0}.field input{width:100%;padding:9px 10px;border:1px solid var(--border);border-radius:8px;background:var(--bg);color:var(--fg);font-size:15px}
.buttons{display:flex;gap:10px;margin-top:16px}button{flex:1;padding:10px;border-radius:8px;border:1px solid var(--border);font-size:15px;cursor:pointer;background:var(--panel);color:var(--fg)}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
</style></head><body><main><div class="card">
<h1>Connect to Relyper Backup</h1>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .ClientName}}
<p><strong>{{.ClientName}}</strong> wants to access this Relyper Backup instance.</p>
<p class="muted">After you approve, you are sent back to <code>{{.RedirectHost}}</code>. Only continue if you started this connection yourself.</p>
{{if .NoLogin}}<p class="err">No user account exists, so nobody can approve this connection. Create a user in Relyper Backup first, or use a personal access token from the MCP page.</p>{{else}}
<form method="post" action="/oauth/authorize">
<input type="hidden" name="client_id" value="{{.Params.ClientID}}"><input type="hidden" name="redirect_uri" value="{{.Params.RedirectURI}}">
<input type="hidden" name="state" value="{{.Params.State}}"><input type="hidden" name="scope" value="{{.Params.Scope}}">
<input type="hidden" name="code_challenge" value="{{.Params.CodeChallenge}}"><input type="hidden" name="resource" value="{{.Params.Resource}}">
<input type="hidden" name="form_token" value="{{.FormToken}}">
<p><strong>Permissions</strong></p>
{{range .Scopes}}<label class="scope"><input type="checkbox" name="scope_choice" value="{{.Name}}"{{if .Checked}} checked{{end}}>
<span><code>{{.Name}}</code>{{if .Restricted}} <span class="tag">each use needs your approval</span>{{end}}<br><span class="muted">{{.Description}}</span></span></label>{{end}}
<div class="field"><label>User name<input name="username" autocomplete="username" required></label></div>
<div class="field"><label>Password<input name="password" type="password" autocomplete="current-password"></label></div>
<div class="buttons"><button type="submit" name="decision" value="deny" formnovalidate>Deny</button><button class="primary" type="submit" name="decision" value="approve">Allow</button></div>
</form>{{end}}{{end}}
</div></main></body></html>`))
