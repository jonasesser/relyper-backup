// Relyper-owned tests: only admins may manage MCP tokens and decide
// approvals when users sign in through Relyper Identity.
package mcpapi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	relyperv1 "github.com/garethgeorge/backrest/gen/go/relyper/v1"
	"github.com/garethgeorge/backrest/internal/relyper/approval"
	"github.com/garethgeorge/backrest/internal/relyper/audit"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"github.com/garethgeorge/backrest/internal/relyper/mcpauth"
)

func newRoleHandler(t *testing.T) *Handler {
	dir := t.TempDir()
	tokens, err := mcpauth.OpenStore(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := identity.ConfigFromEnv(func(k string) string {
		return map[string]string{"RELYPER_OIDC_ISSUER": "https://idp.example", "RELYPER_OIDC_CLIENT_ID": "a", "RELYPER_OIDC_CLIENT_SECRET": "b"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	idp, err := identity.New(identity.Options{Config: cfg, DataDir: dir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{tokens: tokens, approvals: approval.NewStore(), audit: log, idp: idp}
}

func TestOnlyAdminsManageMCP(t *testing.T) {
	h := newRoleHandler(t)
	user := identity.WithIdentity(context.Background(), &identity.Identity{Subject: "1", Email: "u@example.com", Roles: []string{"backup_user"}})
	admin := identity.WithIdentity(context.Background(), &identity.Identity{Subject: "2", Email: "a@example.com", Roles: []string{"backup_admin"}})
	local := context.Background()

	create := connect.NewRequest(&relyperv1.CreateTokenRequest{Name: "x", Scopes: []string{"status:read"}, LifetimeDays: 30})
	if _, err := h.CreateToken(user, create); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("user create token: %v", err)
	}
	if _, err := h.RevokeToken(user, connect.NewRequest(&relyperv1.RevokeTokenRequest{Id: "x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("user revoke token: %v", err)
	}
	if _, err := h.DecideApproval(user, connect.NewRequest(&relyperv1.DecideApprovalRequest{Id: "apr_x", Approve: true})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("user decide: %v", err)
	}
	for name, ctx := range map[string]context.Context{"admin": admin, "local user": local} {
		if _, err := h.CreateToken(ctx, create); err != nil {
			t.Fatalf("%s create token: %v", name, err)
		}
		// The approval does not exist; the role check must pass first.
		_, err := h.DecideApproval(ctx, connect.NewRequest(&relyperv1.DecideApprovalRequest{Id: "apr_x", Approve: true}))
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() == connect.CodePermissionDenied {
			t.Fatalf("%s decide: %v", name, err)
		}
	}
}
