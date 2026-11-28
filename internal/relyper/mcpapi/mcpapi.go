// Package mcpapi wires Relyper Backup's MCP server into the Backrest HTTP
// server and exposes the relyper.v1.RelyperMcp management service (tokens,
// approvals, audit log) to signed-in web UI users.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	relyperv1 "github.com/garethgeorge/backrest/gen/go/relyper/v1"
	"github.com/garethgeorge/backrest/gen/go/relyper/v1/v1connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	backrestconnect "github.com/garethgeorge/backrest/gen/go/v1/v1connect"
	"github.com/garethgeorge/backrest/internal/auth"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/logstore"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/orchestrator"
	"github.com/garethgeorge/backrest/internal/relyper/approval"
	"github.com/garethgeorge/backrest/internal/relyper/audit"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"github.com/garethgeorge/backrest/internal/relyper/infra"
	"github.com/garethgeorge/backrest/internal/relyper/infraapi"
	"github.com/garethgeorge/backrest/internal/relyper/mcp"
	"github.com/garethgeorge/backrest/internal/relyper/mcpauth"
	"github.com/garethgeorge/backrest/internal/relyper/portal"
	"go.uber.org/zap"
)

// Deps are the Backrest components the MCP server uses.
type Deps struct {
	Config        *config.ConfigManager
	Orchestrator  *orchestrator.Orchestrator
	OpLog         *oplog.OpLog
	LogStore      *logstore.LogStore
	Backrest      backrestconnect.BackrestHandler
	Authenticator *auth.Authenticator
	DataDir       string
	SSHDir        string
	Version       string
}

// Routes are the HTTP routes to mount. Authed routes go behind Backrest's
// authentication middleware; Unauthed routes authenticate themselves (MCP
// bearer tokens) or are public by design (OAuth endpoints and metadata).
type Routes struct {
	Authed   map[string]http.Handler
	Unauthed map[string]http.Handler
}

// Setup creates the token store, approval store, audit log, MCP server and
// OAuth server. If the stores cannot be opened, MCP is disabled and an error
// is returned; backups keep working.
func Setup(d Deps) (*Routes, error) {
	dir := filepath.Join(d.DataDir, "relyper")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tokens, err := mcpauth.OpenStore(filepath.Join(dir, "mcp-auth.json"))
	if err != nil {
		return nil, err
	}
	auditLog, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		return nil, err
	}
	approvals := approval.NewStore()

	oauth := &mcpauth.OAuth{
		Store:   tokens,
		Audit:   auditLog,
		MCPPath: mcp.Path,
		Login: func(user, password string) error {
			_, err := d.Authenticator.Login(user, password)
			return err
		},
		LoginAvailable: func() bool { return loginAvailable(d.Config) },
	}
	oauth.Init()

	// Customers (tenants) and the customer portal. A broken tenant store
	// only disables these features; backups keep working.
	tenants, err := portal.Prepare(d.Config, d.Orchestrator, d.OpLog, auditLog, dir)
	if err != nil {
		zap.S().Errorw("customer features disabled", "error", err)
		tenants = nil
	}

	// Sign-in through Relyper Identity (OIDC), optional. A broken
	// configuration only disables this sign-in; local users keep working.
	var idp *identity.Service
	idCfg, idErr := identity.ConfigFromEnv(nil)
	if idErr != nil {
		zap.S().Errorw("Relyper Identity sign-in disabled: invalid configuration", "error", idErr)
		idCfg = nil
	} else if idCfg != nil {
		idp, idErr = identity.New(identity.Options{
			Config: idCfg, DataDir: dir, Version: d.Version, Audit: auditLog, BaseURL: mcpauth.BaseURL,
			AuthEnabled: func() bool { return authEnabled(d.Config) },
			Portal:      tenants.IdentityGate(),
		})
		if idErr != nil {
			zap.S().Errorw("Relyper Identity sign-in disabled", "error", idErr)
			idp = nil
		}
	}
	if idp != nil {
		d.Authenticator.SetExternalVerifier(idp.VerifySession)
		oauth.External = &mcpauth.External{
			Name:    idp.DisplayName(),
			Enabled: idp.Enabled,
			Start: func(w http.ResponseWriter, r *http.Request, consentID string) {
				idp.Start(w, r, identity.FlowConsent, consentID)
			},
		}
		idp.SetConsentCompleter(func(w http.ResponseWriter, r *http.Request, consentID string, id *identity.Identity, e *identity.Error) {
			if e != nil {
				oauth.CompleteExternal(w, r, consentID, "", e.Message)
				return
			}
			oauth.CompleteExternal(w, r, consentID, id.UserName(), "")
		})
	}

	// Application-aware backup: discovery, coverage, app plans, database
	// dumps and verification. Docker is optional.
	inf, err := infra.New(infra.Deps{Config: d.Config, Orchestrator: d.Orchestrator, OpLog: d.OpLog, Audit: auditLog, DataDir: dir}, infraSettings(dir, d.SSHDir))
	if err != nil {
		zap.S().Errorw("Relyper infrastructure features disabled", "error", err)
		inf = nil
	} else {
		go inf.Run(context.Background())
	}

	srv := mcp.New(mcp.Deps{
		Infra:               inf,
		Config:              d.Config,
		Orchestrator:        d.Orchestrator,
		OpLog:               d.OpLog,
		LogStore:            d.LogStore,
		Backrest:            d.Backrest,
		Tokens:              tokens,
		Approvals:           approvals,
		Audit:               auditLog,
		SSHDir:              d.SSHDir,
		StatePath:           filepath.Join(dir, "mcp-state.json"),
		Version:             d.Version,
		ResourceMetadataURL: oauth.ResourceMetadataURL,
	})

	h := &Handler{tokens: tokens, approvals: approvals, audit: auditLog, server: srv, config: d.Config, idp: idp}
	path, handler := v1connect.NewRelyperMcpHandler(h)

	routes := &Routes{
		Authed:   map[string]http.Handler{path: handler},
		Unauthed: oauth.Routes(),
	}
	if inf != nil {
		routes.Authed[infraapi.Prefix] = &infraapi.Handler{Infra: inf, PlanExists: func(id string) bool {
			_, err := d.Orchestrator.GetPlan(id)
			return err == nil
		}}
	}
	if tenants != nil {
		authed, unauthed := tenants.Finish(context.Background(), idp, idp.IsAdmin, d.Backrest)
		for p, hd := range authed {
			routes.Authed[p] = hd
		}
		for p, hd := range unauthed {
			routes.Unauthed[p] = hd
		}
	}
	routes.Authed[identity.PathSetup] = identity.SetupHandler(identity.SetupOptions{
		Config: idCfg, ConfigErr: idErr, Service: idp, BaseURL: mcpauth.BaseURL,
		LocalAuth: func() bool { return authEnabled(d.Config) }, Getenv: os.Getenv,
	})
	routes.Unauthed[mcp.Path] = srv.Handler()
	if idp != nil {
		for p, hd := range idp.Routes() {
			routes.Unauthed[p] = hd
		}
	} else {
		// The web UI asks whether provider sign-in is offered.
		routes.Unauthed[identity.PathStatus] = http.HandlerFunc(identity.DisabledStatus)
	}
	zap.S().Infow("MCP server enabled", "path", mcp.Path, "token_store", filepath.Join(dir, "mcp-auth.json"))
	return routes, nil
}

func authEnabled(c *config.ConfigManager) bool {
	cfg, err := c.Get()
	if err != nil {
		return false
	}
	a := cfg.GetAuth()
	return a != nil && !a.GetDisabled()
}

func loginAvailable(c *config.ConfigManager) bool {
	cfg, err := c.Get()
	if err != nil {
		return false
	}
	a := cfg.GetAuth()
	return a != nil && !a.GetDisabled() && len(a.GetUsers()) > 0
}

// Handler implements relyper.v1.RelyperMcp.
type Handler struct {
	tokens    *mcpauth.Store
	approvals *approval.Store
	audit     *audit.Log
	server    *mcp.Server
	config    *config.ConfigManager
	idp       *identity.Service // nil if Relyper Identity is not configured
}

// requireAdmin refuses users signed in through the identity provider who do
// not have the admin role. Local users are the emergency access and keep
// full rights.
func (h *Handler) requireAdmin(ctx context.Context, action string) error {
	if h.idp.IsAdmin(ctx) {
		return nil
	}
	user := currentUser(ctx)
	h.audit.Record(audit.Event{Actor: "user:" + user, Source: "ui", Action: action, Outcome: "forbidden", Message: "missing role " + h.idp.AdminRole()})
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this needs the role %s", h.idp.AdminRole()))
}

var _ v1connect.RelyperMcpHandler = (*Handler)(nil)

func currentUser(ctx context.Context) string {
	if u, ok := ctx.Value(auth.UserContextKey).(*v1.User); ok && u != nil {
		return u.GetName()
	}
	return "anonymous (authentication disabled)"
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func tokenProto(t *mcpauth.Token) *relyperv1.McpToken {
	now := time.Now()
	active := t.Active(now)
	if t.Kind == mcpauth.KindOAuth {
		active = t.RevokedAt.IsZero() && now.Before(t.RefreshExpiresAt)
	}
	return &relyperv1.McpToken{
		Id: t.ID, Kind: t.Kind, Name: t.Name, Scopes: t.Scopes, CreatedBy: t.CreatedBy,
		CreatedAtMs: ms(t.CreatedAt), ExpiresAtMs: ms(t.ExpiresAt), LastUsedAtMs: ms(t.LastUsedAt),
		RevokedAtMs: ms(t.RevokedAt), RefreshExpiresAtMs: ms(t.RefreshExpiresAt), Active: active,
	}
}

func (h *Handler) GetMcpStatus(ctx context.Context, req *connect.Request[relyperv1.GetMcpStatusRequest]) (*connect.Response[relyperv1.McpStatus], error) {
	out := &relyperv1.McpStatus{
		EndpointPath:     mcp.Path,
		PublicUrl:        os.Getenv("RELYPER_PUBLIC_URL"),
		LoginAvailable:   loginAvailable(h.config),
		PendingApprovals: int32(h.approvals.PendingCount()),
		AuditLogPath:     h.audit.Path(),
		CanAdminister:    h.idp.IsAdmin(ctx),
		AdminRole:        h.idp.AdminRole(),
	}
	if h.idp.Enabled() {
		out.IdentityProvider = h.idp.DisplayName()
	}
	for _, s := range mcpauth.AllScopes {
		out.Scopes = append(out.Scopes, &relyperv1.McpScope{Name: s.Name, Description: s.Description, Restricted: s.Restricted})
	}
	for _, t := range h.server.Tools() {
		out.Tools = append(out.Tools, &relyperv1.McpTool{Name: t.Name, Scope: t.Scope, Restricted: t.Restricted, Description: t.Description})
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) ListTokens(ctx context.Context, req *connect.Request[relyperv1.ListTokensRequest]) (*connect.Response[relyperv1.TokenList], error) {
	out := &relyperv1.TokenList{}
	for _, t := range h.tokens.List() {
		out.Tokens = append(out.Tokens, tokenProto(&t))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) CreateToken(ctx context.Context, req *connect.Request[relyperv1.CreateTokenRequest]) (*connect.Response[relyperv1.CreateTokenResponse], error) {
	if err := h.requireAdmin(ctx, "token.create"); err != nil {
		return nil, err
	}
	user := currentUser(ctx)
	days := req.Msg.LifetimeDays
	if days <= 0 || days > 366 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("lifetime_days must be between 1 and 366"))
	}
	t, secret, err := h.tokens.CreatePersonal(req.Msg.Name, req.Msg.Scopes, time.Duration(days)*24*time.Hour, user)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	h.audit.Record(audit.Event{Actor: "user:" + user, Source: "ui", Action: "token.create", Outcome: "ok", Message: fmt.Sprintf("%s (%s) %v, %d days", t.Name, t.ID, t.Scopes, days)})
	return connect.NewResponse(&relyperv1.CreateTokenResponse{Token: tokenProto(t), Secret: secret}), nil
}

func (h *Handler) RevokeToken(ctx context.Context, req *connect.Request[relyperv1.RevokeTokenRequest]) (*connect.Response[relyperv1.McpToken], error) {
	if err := h.requireAdmin(ctx, "token.revoke"); err != nil {
		return nil, err
	}
	t, err := h.tokens.Revoke(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	user := currentUser(ctx)
	h.audit.Record(audit.Event{Actor: "user:" + user, Source: "ui", Action: "token.revoke", Outcome: "ok", Message: fmt.Sprintf("%s (%s)", t.Name, t.ID)})
	return connect.NewResponse(tokenProto(t)), nil
}

func approvalProto(a *approval.Request) *relyperv1.Approval {
	preview, _ := json.MarshalIndent(a.Preview, "", "  ")
	return &relyperv1.Approval{
		Id: a.ID, Tool: a.Tool, Summary: a.Summary, ParamsJson: a.ParamsJSON, PreviewJson: string(preview),
		TokenId: a.TokenID, TokenName: a.TokenName, Status: string(a.Status),
		CreatedAtMs: ms(a.CreatedAt), ExpiresAtMs: ms(a.ExpiresAt), DecidedBy: a.DecidedBy, DecidedAtMs: ms(a.DecidedAt),
		DenyReason: a.DenyReason, ExecutedAtMs: ms(a.ExecutedAt), OperationId: a.OperationID,
	}
}

func (h *Handler) ListApprovals(ctx context.Context, req *connect.Request[relyperv1.ListApprovalsRequest]) (*connect.Response[relyperv1.ApprovalList], error) {
	out := &relyperv1.ApprovalList{}
	for _, a := range h.approvals.List() {
		out.Approvals = append(out.Approvals, approvalProto(&a))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) DecideApproval(ctx context.Context, req *connect.Request[relyperv1.DecideApprovalRequest]) (*connect.Response[relyperv1.Approval], error) {
	if err := h.requireAdmin(ctx, "approval.decide"); err != nil {
		return nil, err
	}
	user := currentUser(ctx)
	if len(req.Msg.Reason) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("reason is too long"))
	}
	a, err := h.approvals.Decide(req.Msg.Id, req.Msg.Approve, user, req.Msg.Reason)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	outcome := "approved"
	if !req.Msg.Approve {
		outcome = "denied"
	}
	h.audit.Record(audit.Event{Actor: "user:" + user, Source: "ui", Action: "approval.decide", Outcome: outcome, ApprovalID: a.ID, Message: a.Tool + ": " + a.Summary})
	return connect.NewResponse(approvalProto(a)), nil
}

func (h *Handler) ListAuditEvents(ctx context.Context, req *connect.Request[relyperv1.ListAuditEventsRequest]) (*connect.Response[relyperv1.AuditEventList], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	events, err := h.audit.Recent(limit)
	if err != nil {
		return nil, err
	}
	out := &relyperv1.AuditEventList{}
	for _, e := range events {
		out.Events = append(out.Events, &relyperv1.AuditEvent{
			TimeMs: e.Time.UnixMilli(), Actor: e.Actor, Source: e.Source, Action: e.Action, Outcome: e.Outcome,
			Params: e.Params, Message: e.Message, ApprovalId: e.ApprovalID, RemoteAddr: e.RemoteAddr,
		})
	}
	return connect.NewResponse(out), nil
}

func infraSettings(dataDir, sshDir string) infra.Settings {
	st := infra.SettingsFromEnv(dataDir)
	if sshDir != "" {
		st.KnownHostsPath = filepath.Join(sshDir, "known_hosts")
	}
	return st
}
