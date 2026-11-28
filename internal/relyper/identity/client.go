package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is the user as the identity provider describes them.
type Identity struct {
	Subject     string   `json:"sub"`
	Email       string   `json:"email"`
	DisplayName string   `json:"name"`
	Roles       []string `json:"roles"`
	// Claims are all claims of the verified ID token (relyper: the customer
	// portal reads the tenant claim from them). Never logged.
	Claims map[string]any `json:"-"`
}

// UserName is the name used for this user inside Relyper Backup (audit log,
// "created by"). The prefix keeps it apart from local user names.
func (i *Identity) UserName() string {
	return "relyper:" + i.Email
}

// Error is a sign-in failure. Message is safe to show to the user; Detail is
// for the log and never contains secrets or tokens.
type Error struct {
	Code    string
	Message string
	Detail  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func newErr(code, msg, detail string) *Error { return &Error{Code: code, Message: msg, Detail: detail} }

// transaction is what a sign-in remembers between the redirect to the
// provider and the callback.
type transaction struct {
	State, Nonce, Verifier string
	RedirectURI            string
}

const (
	discoveryTTL   = time.Hour
	requestTimeout = 10 * time.Second
	clockSkew      = time.Minute
)

// client talks to the provider. Discovery and JWKS are fetched lazily and
// cached, so an unreachable provider never blocks startup or backups.
type client struct {
	cfg  *Config
	http *http.Client
	// extraLogin lets customers of the portal past the operator role gate
	// (relyper: multi-tenant portal). The callback decides which session
	// they get.
	extraLogin func(*Identity) bool

	mu        sync.Mutex
	provider  *gooidc.Provider
	fetchedAt time.Time
}

func newClient(cfg *Config, version string) *client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	return &client{cfg: cfg, http: &http.Client{
		Timeout:   requestTimeout,
		Transport: &edgeTransport{next: base, userAgent: "RelyperBackup/" + version + " (OIDC client)"},
		// Never follow redirects from the token or JWKS endpoints silently to
		// another host; discovery documents do not redirect in practice.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return errors.New("redirect to another host refused")
			}
			return nil
		},
	}}
}

func (c *client) ctx(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, c.http)
}

func (c *client) getProvider(ctx context.Context) (*gooidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil && time.Since(c.fetchedAt) < discoveryTTL {
		return c.provider, nil
	}
	p, err := gooidc.NewProvider(c.ctx(ctx), c.cfg.Issuer)
	if err != nil {
		if c.provider != nil {
			// Keep using the last good document while the provider is flaky.
			return c.provider, nil
		}
		return nil, newErr("provider_unavailable", "The sign-in service cannot be reached. Try again later or sign in with your local user.", err.Error())
	}
	c.provider, c.fetchedAt = p, time.Now()
	return p, nil
}

func (c *client) oauthConfig(p *gooidc.Provider, redirectURI string) *oauth2.Config {
	ep := p.Endpoint()
	// Pick the client authentication from discovery instead of letting
	// oauth2 guess: a guess retries the exchange, and a code is single use.
	var meta struct {
		Methods []string `json:"token_endpoint_auth_methods_supported"`
	}
	_ = p.Claims(&meta)
	ep.AuthStyle = oauth2.AuthStyleInHeader
	if !slices.Contains(meta.Methods, "client_secret_basic") && slices.Contains(meta.Methods, "client_secret_post") {
		ep.AuthStyle = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{
		ClientID:     c.cfg.ClientID,
		ClientSecret: c.cfg.ClientSecret,
		Endpoint:     ep,
		RedirectURL:  redirectURI,
		Scopes:       c.cfg.Scopes,
	}
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// authorizationURL starts a sign-in.
func (c *client) authorizationURL(ctx context.Context, redirectURI string) (string, *transaction, error) {
	p, err := c.getProvider(ctx)
	if err != nil {
		return "", nil, err
	}
	tx := &transaction{State: randomString(32), Nonce: randomString(32), Verifier: oauth2.GenerateVerifier(), RedirectURI: redirectURI}
	u := c.oauthConfig(p, redirectURI).AuthCodeURL(tx.State, oauth2.S256ChallengeOption(tx.Verifier), gooidc.Nonce(tx.Nonce))
	return u, tx, nil
}

// complete exchanges the code and verifies the ID token. The caller has
// already matched the state against the browser's transaction.
func (c *client) complete(ctx context.Context, tx *transaction, code string) (*Identity, error) {
	if code == "" {
		return nil, newErr("invalid_callback", "The sign-in was not completed.", "callback without code")
	}
	p, err := c.getProvider(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := c.oauthConfig(p, tx.RedirectURI).Exchange(c.ctx(ctx), code, oauth2.VerifierOption(tx.Verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		detail := "token exchange failed"
		if errors.As(err, &re) {
			detail = fmt.Sprintf("token endpoint answered %d %s", re.Response.StatusCode, re.ErrorCode)
		} else {
			detail = "token exchange failed: " + err.Error()
		}
		return nil, newErr("token_exchange_failed", "The sign-in could not be completed. Try again.", detail)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, newErr("missing_id_token", "The sign-in service sent no identity.", "token response without id_token")
	}
	return c.verifyIDToken(ctx, p, raw, tx.Nonce)
}

func (c *client) verifyIDToken(ctx context.Context, p *gooidc.Provider, raw, nonce string) (*Identity, error) {
	v := p.Verifier(&gooidc.Config{
		ClientID:             c.cfg.ClientID,
		SupportedSigningAlgs: []string{gooidc.RS256},
		Now:                  func() time.Time { return time.Now().Add(-clockSkew) },
	})
	idt, err := v.Verify(c.ctx(ctx), raw)
	if err != nil {
		return nil, newErr("invalid_id_token", "The identity could not be verified.", err.Error())
	}
	if idt.Nonce == "" || idt.Nonce != nonce {
		return nil, newErr("invalid_id_token", "The identity could not be verified.", "nonce mismatch")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, newErr("invalid_id_token", "The identity could not be verified.", "claims: "+err.Error())
	}
	azp, _ := claims["azp"].(string)
	if (len(idt.Audience) > 1 || azp != "") && azp != c.cfg.ClientID {
		return nil, newErr("invalid_id_token", "The identity could not be verified.", "azp does not match the client")
	}
	id := claimsToIdentity(claims)
	id.Roles = c.operatorScoped(id.Roles, claims)
	if id.Subject == "" {
		return nil, newErr("invalid_id_token", "The identity could not be verified.", "empty sub")
	}
	if id.Email == "" {
		return nil, newErr("missing_email", "Your account has no e-mail address.", "id token without email")
	}
	if !c.mayLogin(id.Roles) && (c.extraLogin == nil || !c.extraLogin(id)) {
		return nil, newErr("forbidden", "Your account has no access to Relyper Backup. Ask an administrator for the role "+c.cfg.LoginRole+".", "missing role "+c.cfg.LoginRole)
	}
	return id, nil
}

// operatorScoped drops LoginRole and AdminRole unless the token was issued
// for the operator tenant. Relyper Identity puts only the roles of the
// token's own tenant into "roles", so the tenant claim is the one to check.
func (c *client) operatorScoped(roles []string, claims map[string]any) []string {
	if c.cfg.OperatorTenant == AnyTenant {
		return roles
	}
	tenant := asString(claims["tenant"])
	if tenant == "" {
		tenant = asString(claims["tenant_id"])
	}
	if tenant == c.cfg.OperatorTenant {
		return roles
	}
	return slices.DeleteFunc(slices.Clone(roles), func(r string) bool {
		return r == c.cfg.LoginRole || r == c.cfg.AdminRole
	})
}

func (c *client) mayLogin(roles []string) bool {
	return slices.Contains(roles, c.cfg.LoginRole) || slices.Contains(roles, c.cfg.AdminRole)
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// asStringList accepts an array or a space- or comma-separated string, like
// defaultClaimsToIdentity in @relyper/sp-auth.
func asStringList(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if s := asString(e); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ' ' }) {
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func claimsToIdentity(claims map[string]any) *Identity {
	email := asString(claims["email"])
	name := asString(claims["name"])
	if name == "" {
		name = asString(claims["preferred_username"])
	}
	if name == "" {
		name = email
	}
	return &Identity{Subject: asString(claims["sub"]), Email: email, DisplayName: name, Roles: asStringList(claims["roles"]), Claims: claims}
}

// edgeTransport sends a descriptive User-Agent (bot protection in front of
// the provider challenges bare library agents) and retries a GET once when an
// edge layer rejects it. POSTs are never retried: an authorization code is
// single use.
type edgeTransport struct {
	next      http.RoundTripper
	userAgent string
}

func (t *edgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", t.userAgent)
	resp, err := t.next.RoundTrip(req)
	if err != nil || req.Method != http.MethodGet {
		return resp, err
	}
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		resp.Body.Close()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(500 * time.Millisecond):
		}
		return t.next.RoundTrip(req)
	}
	return resp, nil
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
