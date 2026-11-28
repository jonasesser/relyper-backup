package identity

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/audit"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// Paths served by the Service (all without web UI authentication).
const (
	PathStatus   = "/auth/oidc/status"
	PathLogin    = "/auth/oidc/login"
	PathCallback = "/auth/oidc/callback"
	PathSession  = "/auth/oidc/session"

	txCookie      = "relyper_oidc_tx"
	handoffCookie = "relyper_oidc_handoff"
	sessionIssuer = "relyper-backup/oidc"

	txLifetime      = 10 * time.Minute
	handoffLifetime = 2 * time.Minute
	maxPending      = 1000
)

// Flow says what a sign-in is for.
type Flow string

const (
	// FlowWebUI signs in to the web UI.
	FlowWebUI Flow = "ui"
	// FlowConsent signs in on the MCP OAuth consent page.
	FlowConsent Flow = "mcp"
	// FlowPortal signs in to the customer portal.
	FlowPortal Flow = "portal"
)

// ConsentCompleter finishes an MCP consent after the provider answered.
// Exactly one of id and err is set.
type ConsentCompleter func(w http.ResponseWriter, r *http.Request, consentID string, id *Identity, err *Error)

// Options configure a Service.
type Options struct {
	Config  *Config
	DataDir string // <data>/relyper
	Version string
	Audit   *audit.Log
	// BaseURL returns the public base URL for a request (RELYPER_PUBLIC_URL
	// or derived from the request).
	BaseURL func(r *http.Request) string
	// AuthEnabled reports whether web UI authentication is switched on.
	AuthEnabled func() bool
	// Portal admits customers to the customer portal. Nil: no portal.
	Portal PortalGate
}

// Service serves the sign-in routes and verifies sessions.
type Service struct {
	opt       Options
	cfg       *Config
	client    *client
	key       []byte
	portalKey []byte // signs portal sessions; never accepted as operator session
	consent   ConsentCompleter

	mu       sync.Mutex
	txs      map[string]*pendingTx // by state
	handoffs map[string]*handoff   // by hash of the handoff code
	now      func() time.Time
}

type pendingTx struct {
	tx         *transaction
	browser    string // hash of the tx cookie value
	flow       Flow
	consentRef string
	expires    time.Time
}

type handoff struct {
	token   string
	user    string
	kind    string // "operator" or "portal"
	expires time.Time
}

// New returns nil, nil when no configuration is given.
func New(opt Options) (*Service, error) {
	if opt.Config == nil {
		return nil, nil
	}
	key, err := loadKey(filepath.Join(opt.DataDir, "oidc-session.key"))
	if err != nil {
		return nil, err
	}
	portalKey, err := loadKey(filepath.Join(opt.DataDir, "portal-session.key"))
	if err != nil {
		return nil, err
	}
	s := &Service{
		opt: opt, cfg: opt.Config, client: newClient(opt.Config, opt.Version), key: key, portalKey: portalKey,
		txs: map[string]*pendingTx{}, handoffs: map[string]*handoff{}, now: time.Now,
	}
	if opt.Portal != nil {
		s.client.extraLogin = func(id *Identity) bool {
			_, ok := opt.Portal.Admit(id)
			return ok
		}
	}
	zap.S().Infow("Relyper Identity sign-in enabled", "issuer", opt.Config.Issuer, "client_id", opt.Config.ClientID,
		"login_role", opt.Config.LoginRole, "admin_role", opt.Config.AdminRole)
	return s, nil
}

func loadKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write session key: %w", err)
	}
	return key, nil
}

// SetConsentCompleter connects the MCP OAuth consent page.
func (s *Service) SetConsentCompleter(f ConsentCompleter) { s.consent = f }

// DisplayName is shown on sign-in buttons.
func (s *Service) DisplayName() string {
	if s == nil {
		return ""
	}
	return s.cfg.DisplayName
}

// Enabled reports whether sign-in through the provider can be offered.
func (s *Service) Enabled() bool {
	return s != nil && (s.opt.AuthEnabled == nil || s.opt.AuthEnabled())
}

// Routes are mounted without web UI authentication.
func (s *Service) Routes() map[string]http.Handler {
	return map[string]http.Handler{
		PathStatus:   http.HandlerFunc(s.status),
		PathLogin:    http.HandlerFunc(s.login),
		PathCallback: http.HandlerFunc(s.callback),
		PathSession:  http.HandlerFunc(s.session),
	}
}

func (s *Service) redirectURI(r *http.Request) string {
	if s.cfg.RedirectURI != "" {
		return s.cfg.RedirectURI
	}
	return strings.TrimRight(s.opt.BaseURL(r), "/") + PathCallback
}

func (s *Service) secure(r *http.Request) bool {
	return strings.HasPrefix(s.opt.BaseURL(r), "https://")
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// DisabledStatus answers the status route when no provider is configured.
func DisabledStatus(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false})
}

func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"enabled": s.Enabled(), "name": s.cfg.DisplayName, "portal": s.PortalEnabled()})
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Query().Get("portal") == "1" && s.opt.Portal != nil {
		s.Start(w, r, FlowPortal, "")
		return
	}
	s.Start(w, r, FlowWebUI, "")
}

// Start redirects the browser to the provider.
func (s *Service) Start(w http.ResponseWriter, r *http.Request, flow Flow, consentRef string) {
	noStore(w)
	if !s.Enabled() {
		s.fail(w, r, flow, consentRef, newErr("disabled", "Sign-in through Relyper is not available.", "authentication disabled"))
		return
	}
	u, tx, err := s.client.authorizationURL(r.Context(), s.redirectURI(r))
	if err != nil {
		s.fail(w, r, flow, consentRef, asErr(err))
		return
	}
	browser := randomString(32)
	s.mu.Lock()
	s.gcLocked()
	if len(s.txs) >= maxPending {
		s.mu.Unlock()
		s.fail(w, r, flow, consentRef, newErr("busy", "Too many sign-ins are in progress. Try again in a few minutes.", "pending sign-in limit reached"))
		return
	}
	s.txs[tx.State] = &pendingTx{tx: tx, browser: hashString(browser), flow: flow, consentRef: consentRef, expires: s.now().Add(txLifetime)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: txCookie, Value: browser, Path: "/auth/oidc", MaxAge: int(txLifetime.Seconds()),
		HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Service) gcLocked() {
	now := s.now()
	for k, t := range s.txs {
		if now.After(t.expires) {
			delete(s.txs, k)
		}
	}
	for k, h := range s.handoffs {
		if now.After(h.expires) {
			delete(s.handoffs, k)
		}
	}
}

func asErr(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return newErr("internal", "The sign-in failed.", err.Error())
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	q := r.URL.Query()
	state := q.Get("state")
	s.mu.Lock()
	p := s.txs[state]
	delete(s.txs, state) // single use, whatever happens next
	s.mu.Unlock()
	// Clear the transaction cookie in every case.
	http.SetCookie(w, &http.Cookie{Name: txCookie, Value: "", Path: "/auth/oidc", MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	if p == nil || s.now().After(p.expires) {
		s.fail(w, r, FlowWebUI, "", newErr("invalid_state", "The sign-in expired or was started in another browser. Start again.", "unknown or expired state"))
		return
	}
	c, err := r.Cookie(txCookie)
	if err != nil || hashString(c.Value) != p.browser {
		s.fail(w, r, p.flow, p.consentRef, newErr("invalid_state", "The sign-in was started in another browser. Start again.", "transaction cookie missing or different"))
		return
	}
	if e := q.Get("error"); e != "" {
		s.fail(w, r, p.flow, p.consentRef, newErr("provider_error", "The sign-in was cancelled or refused.", "provider error "+e))
		return
	}
	id, err := s.client.complete(r.Context(), p.tx, q.Get("code"))
	if err != nil {
		s.fail(w, r, p.flow, p.consentRef, asErr(err))
		return
	}
	operator := s.client.mayLogin(id.Roles)
	if p.flow == FlowConsent {
		// MCP access is for operators only; customers never get MCP tokens.
		if !operator {
			s.fail(w, r, p.flow, p.consentRef, s.forbidden())
			return
		}
		s.record(r, id.UserName(), "oidc.login", "ok", string(p.flow))
		if s.consent == nil {
			http.Error(w, "consent not available", http.StatusInternalServerError)
			return
		}
		s.consent(w, r, p.consentRef, id, nil)
		return
	}
	var token, kind, target string
	switch {
	case operator:
		// Operator roles win: an operator never acts as a tenant member.
		token, err = s.issueSession(id)
		kind, target = "operator", "/?relyper_login=1"
	case s.opt.Portal != nil:
		m, ok := s.opt.Portal.Admit(id)
		if !ok {
			s.fail(w, r, FlowPortal, "", s.forbidden())
			return
		}
		token, err = s.issuePortalSession(id, m)
		kind, target = "portal", "/?relyper_login=1#/portal"
	default:
		s.fail(w, r, p.flow, "", s.forbidden())
		return
	}
	if err != nil {
		s.fail(w, r, p.flow, "", asErr(err))
		return
	}
	s.record(r, id.UserName(), "oidc.login", "ok", kind)
	code := randomString(32)
	s.mu.Lock()
	s.handoffs[hashString(code)] = &handoff{token: token, user: id.UserName(), kind: kind, expires: s.now().Add(handoffLifetime)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: handoffCookie, Value: code, Path: PathSession, MaxAge: int(handoffLifetime.Seconds()),
		HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Service) forbidden() *Error {
	return newErr("forbidden", "Your account has no access to Relyper Backup. Ask an administrator for the role "+s.cfg.LoginRole+".", "no operator role and not admitted to the portal")
}

// session hands the web UI its token once, after the callback. The custom
// header makes a cross-site request need a CORS preflight, which is refused.
func (s *Service) session(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Method != http.MethodPost || r.Header.Get("X-Relyper-Login") != "1" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: handoffCookie, Value: "", Path: PathSession, MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	c, err := r.Cookie(handoffCookie)
	if err != nil {
		http.Error(w, "no sign-in to complete", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	h := s.handoffs[hashString(c.Value)]
	delete(s.handoffs, hashString(c.Value))
	s.mu.Unlock()
	if h == nil || s.now().After(h.expires) {
		http.Error(w, "sign-in expired", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": h.token, "user": h.user, "kind": h.kind})
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, flow Flow, consentRef string, e *Error) {
	zap.S().Warnw("Relyper Identity sign-in failed", "code", e.Code, "detail", e.Detail)
	s.record(r, "", "oidc.login", "error", e.Code+": "+e.Detail)
	if flow == FlowConsent && s.consent != nil {
		s.consent(w, r, consentRef, nil, e)
		return
	}
	if flow == FlowPortal {
		http.Redirect(w, r, "/?relyper_login_error="+e.Code+"#/portal", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/?relyper_login_error="+e.Code, http.StatusFound)
}

func (s *Service) record(r *http.Request, user, action, outcome, msg string) {
	if s.opt.Audit == nil {
		return
	}
	actor := "anonymous"
	if user != "" {
		actor = "user:" + user
	}
	s.opt.Audit.Record(audit.Event{Actor: actor, Source: "oidc", Action: action, Outcome: outcome, Message: msg, RemoteAddr: remoteAddr(r)})
}

func remoteAddr(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return strings.Trim(r.RemoteAddr[:i], "[]")
	}
	return r.RemoteAddr
}

type sessionClaims struct {
	jwt.RegisteredClaims
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

func (s *Service) issueSession(id *Identity) (string, error) {
	now := s.now()
	c := sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: sessionIssuer, Subject: id.Subject, ID: randomString(16),
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.SessionTTL)),
		},
		Email: id.Email, Name: id.DisplayName, Roles: id.Roles,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.key)
}

type ctxKey struct{}

// FromContext returns the identity of a request signed in through the
// provider, or nil for local users and disabled authentication.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(ctxKey{}).(*Identity)
	return id
}

// WithIdentity is used by tests and the session verifier.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// VerifySession checks a web UI bearer token issued by this service. ok is
// false for any other token, so upstream verification can take over. The
// role gate is applied again on every request against the current settings.
func (s *Service) VerifySession(ctx context.Context, token string) (context.Context, *v1.User, bool) {
	if s == nil {
		return ctx, nil, false
	}
	var c sessionClaims
	t, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(sessionIssuer),
		jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.now))
	if err != nil || !t.Valid || c.Subject == "" || c.Email == "" {
		return ctx, nil, false
	}
	if !s.client.mayLogin(c.Roles) {
		return ctx, nil, false
	}
	id := &Identity{Subject: c.Subject, Email: c.Email, DisplayName: c.Name, Roles: c.Roles}
	return WithIdentity(ctx, id), &v1.User{Name: id.UserName()}, true
}

// IsAdmin reports whether the request may approve MCP requests and manage
// MCP tokens. Local users (emergency access) and disabled authentication
// keep full rights, as before.
func (s *Service) IsAdmin(ctx context.Context) bool {
	id := FromContext(ctx)
	if id == nil {
		return true
	}
	if s == nil {
		return false
	}
	return slices.Contains(id.Roles, s.cfg.AdminRole)
}

// AdminRole is the configured admin role name.
func (s *Service) AdminRole() string {
	if s == nil {
		return ""
	}
	return s.cfg.AdminRole
}

// OperatorTenant is the tenant operator users belong to ("" when the
// tenant check is off). Every operator session comes from this tenant.
func (s *Service) OperatorTenant() string {
	if s == nil || s.cfg.OperatorTenant == AnyTenant {
		return ""
	}
	return s.cfg.OperatorTenant
}
