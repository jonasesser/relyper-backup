// Package roles is Relyper-owned: role-based access for the web UI pages
// that come from Backrest.
//
// Users signed in through Relyper Identity (OIDC) with the admin role
// (RELYPER_OIDC_ADMIN_ROLE, default backup_admin) have full access. Users
// with only the login role (RELYPER_OIDC_LOGIN_ROLE, default backup_user)
// get read-only access: they see plans, repositories, snapshots, operations,
// logs and the Relyper pages, but cannot change the configuration, start
// backups or restores, trust host keys or run commands. The configuration
// they receive has its secrets redacted.
//
// Local users (emergency access) and disabled authentication keep full
// access, as in Backrest. The check is done on the server for every request;
// unknown routes are denied (deny by default).
package roles

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/gen/go/v1/v1connect"
	"github.com/garethgeorge/backrest/gen/go/v1sync/v1syncconnect"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Policy decides about requests.
type Policy struct {
	// IsAdmin reports whether a request has full rights. It must return true
	// for local users and disabled authentication.
	IsAdmin func(ctx context.Context) bool
}

// ReadOnly reports whether the request is from a read-only user.
func (p *Policy) ReadOnly(ctx context.Context) bool {
	if p == nil || p.IsAdmin == nil {
		return false
	}
	if identity.FromContext(ctx) == nil {
		return false // local user or authentication disabled
	}
	return !p.IsAdmin(ctx)
}

// readOnlyProcedures are the Connect procedures a read-only user may call.
var readOnlyProcedures = map[string]bool{
	v1connect.BackrestGetConfigProcedure:                                   true, // redacted, see Backrest
	v1connect.BackrestGetOperationEventsProcedure:                          true,
	v1connect.BackrestGetOperationsProcedure:                               true,
	v1connect.BackrestListSnapshotsProcedure:                               true,
	v1connect.BackrestListSnapshotFilesProcedure:                           true,
	v1connect.BackrestGetLogsProcedure:                                     true,
	v1connect.BackrestGetSummaryDashboardProcedure:                         true,
	v1syncconnect.BackrestSyncStateServiceGetPeerSyncStatesStreamProcedure: true,
}

// readOnlyPrefixes are route prefixes that read-only users may use with
// GET (or that enforce the admin role themselves).
var (
	getPrefixes = []string{"/relyper/infra/", "/relyper/identity/", "/relyper/tenants/", "/metrics"}
	// The MCP management service checks the admin role itself for tokens
	// and approvals; listing is allowed.
	selfCheckedPrefixes = []string{"/relyper.v1.RelyperMcp/"}
)

// Allowed reports whether a read-only user may make the request.
func Allowed(r *http.Request) bool {
	path := r.URL.Path
	if r.Method == http.MethodOptions {
		return true
	}
	if readOnlyProcedures[path] {
		return true
	}
	for _, p := range selfCheckedPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		for _, p := range getPrefixes {
			if strings.HasPrefix(path, p) || path == strings.TrimSuffix(p, "/") {
				return true
			}
		}
	}
	return false
}

// Gate wraps an authenticated handler. It runs after authentication, so the
// request context carries the identity.
func (p *Policy) Gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.ReadOnly(r.Context()) && !Allowed(r) {
			deny(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const deniedMessage = "read-only access: this needs the admin role"

func deny(w http.ResponseWriter, r *http.Request) {
	if ew := connect.NewErrorWriter(); ew.IsSupported(r) {
		// A Connect error the web UI shows like any other.
		_ = ew.Write(w, r, connect.NewError(connect.CodePermissionDenied, errorString(deniedMessage)))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"` + deniedMessage + `"}`))
}

type errorString string

func (e errorString) Error() string { return string(e) }

// Backrest wraps the Backrest service so that read-only users receive the
// configuration without secrets.
type Backrest struct {
	v1connect.BackrestHandler
	Policy *Policy
}

// GetConfig redacts secrets for read-only users.
func (b *Backrest) GetConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[v1.Config], error) {
	resp, err := b.BackrestHandler.GetConfig(ctx, req)
	if err != nil || !b.Policy.ReadOnly(ctx) {
		return resp, err
	}
	return connect.NewResponse(RedactConfig(resp.Msg)), nil
}

// PathMe tells the web UI whether the current user is read-only.
const PathMe = "/relyper/identity/me"

// MeHandler answers GET PathMe.
func (p *Policy) MeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := identity.FromContext(r.Context())
		out := map[string]any{"read_only": p.ReadOnly(r.Context()), "provider": id != nil}
		if id != nil {
			out["user"] = id.UserName()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	})
}
