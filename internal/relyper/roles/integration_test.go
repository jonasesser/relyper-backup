package roles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/gen/go/v1/v1connect"
	"github.com/garethgeorge/backrest/internal/auth"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"google.golang.org/protobuf/types/known/emptypb"
)

type writableBackrest struct{ fakeBackrest }

func (f *writableBackrest) SetConfig(_ context.Context, req *connect.Request[v1.Config]) (*connect.Response[v1.Config], error) {
	f.cfg = req.Msg
	return connect.NewResponse(f.cfg), nil
}

// TestThroughAuthentication runs the gate behind Backrest's authentication
// middleware, as cmd/backrest mounts it, with a real Connect client.
func TestThroughAuthentication(t *testing.T) {
	cfg := secretConfig()
	store := &config.MemoryStore{Config: cfg}
	a := auth.NewAuthenticator([]byte("key"), store)
	a.SetExternalVerifier(func(ctx context.Context, token string) (context.Context, *v1.User, bool) {
		var roles []string
		switch token {
		case "viewer":
			roles = []string{"backup_user"}
		case "admin":
			roles = []string{"backup_admin"}
		default:
			return ctx, nil, false
		}
		id := &identity.Identity{Subject: token, Email: token + "@example.com", Roles: roles}
		return identity.WithIdentity(ctx, id), &v1.User{Name: id.UserName()}, true
	})
	backend := &writableBackrest{fakeBackrest{cfg: secretConfig()}}
	path, h := v1connect.NewBackrestHandler(&Backrest{BackrestHandler: backend, Policy: policy})
	mux := http.NewServeMux()
	mux.Handle(path, auth.RequireAuthentication(policy.Gate(h), a))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := func(token string) v1connect.BackrestClient {
		return v1connect.NewBackrestClient(srv.Client(), srv.URL, connect.WithInterceptors(connect.UnaryInterceptorFunc(
			func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", "Bearer "+token)
					return next(ctx, req)
				}
			})))
	}

	got, err := client("viewer").GetConfig(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Msg.String(), "repo-secret") {
		t.Fatal("viewer received the repository password")
	}
	_, err = client("viewer").SetConfig(context.Background(), connect.NewRequest(&v1.Config{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer SetConfig: %v", err)
	}
	if backend.cfg.Repos == nil {
		t.Fatal("viewer changed the configuration")
	}
	full, err := client("admin").GetConfig(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err != nil || full.Msg.Repos[0].Password != "repo-secret" {
		t.Fatalf("admin GetConfig: %v", err)
	}
	if _, err := client("admin").SetConfig(context.Background(), connect.NewRequest(&v1.Config{})); err != nil {
		t.Fatalf("admin SetConfig: %v", err)
	}
}
