package roles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/gen/go/v1/v1connect"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"google.golang.org/protobuf/types/known/emptypb"
)

var policy = &Policy{IsAdmin: func(ctx context.Context) bool {
	id := identity.FromContext(ctx)
	return id == nil || slices.Contains(id.Roles, "backup_admin")
}}

func viewer() context.Context {
	return identity.WithIdentity(context.Background(), &identity.Identity{Subject: "u", Email: "u@example.com", Roles: []string{"backup_user"}})
}

func admin() context.Context {
	return identity.WithIdentity(context.Background(), &identity.Identity{Subject: "a", Email: "a@example.com", Roles: []string{"backup_admin"}})
}

func TestGate(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := policy.Gate(ok)
	cases := []struct {
		ctx    context.Context
		method string
		path   string
		want   int
	}{
		{viewer(), http.MethodPost, v1connect.BackrestGetOperationsProcedure, http.StatusTeapot},
		{viewer(), http.MethodPost, v1connect.BackrestGetConfigProcedure, http.StatusTeapot},
		{viewer(), http.MethodPost, v1connect.BackrestSetConfigProcedure, http.StatusForbidden},
		{viewer(), http.MethodPost, v1connect.BackrestBackupProcedure, http.StatusForbidden},
		{viewer(), http.MethodPost, v1connect.BackrestRestoreProcedure, http.StatusForbidden},
		{viewer(), http.MethodPost, v1connect.BackrestRunCommandProcedure, http.StatusForbidden},
		{viewer(), http.MethodPost, v1connect.BackrestGetDownloadURLProcedure, http.StatusForbidden},
		{viewer(), http.MethodPost, "/relyper.v1.RelyperSftp/TrustHostKey", http.StatusForbidden},
		{viewer(), http.MethodPost, "/v1sync.BackrestSyncStateService/SetRemoteClientConfig", http.StatusForbidden},
		{viewer(), http.MethodGet, "/relyper/infra/coverage", http.StatusTeapot},
		{viewer(), http.MethodPost, "/relyper/infra/backup", http.StatusForbidden},
		{viewer(), http.MethodPost, "/relyper/tenants/x", http.StatusForbidden},
		{viewer(), http.MethodGet, "/relyper/unknown", http.StatusForbidden},
		{viewer(), http.MethodPost, "/relyper.v1.RelyperMcp/ListTokens", http.StatusTeapot},
		{admin(), http.MethodPost, v1connect.BackrestSetConfigProcedure, http.StatusTeapot},
		{context.Background(), http.MethodPost, v1connect.BackrestRunCommandProcedure, http.StatusTeapot},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil).WithContext(c.ctx)
		if c.method == http.MethodPost && strings.Count(c.path, ".") > 0 && strings.HasPrefix(c.path, "/") && !strings.HasPrefix(c.path, "/relyper/") {
			r.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		got := rec.Code
		if c.want == http.StatusForbidden && got != http.StatusForbidden {
			// Connect requests get a Connect error (403 for permission_denied).
			t.Errorf("%s %s: got %d, want denied", c.method, c.path, got)
		} else if c.want != http.StatusForbidden && got != c.want {
			t.Errorf("%s %s: got %d, want %d", c.method, c.path, got, c.want)
		}
		if got == http.StatusForbidden && !strings.Contains(rec.Body.String(), "read-only") {
			t.Errorf("%s: body %q", c.path, rec.Body.String())
		}
	}
}

type fakeBackrest struct {
	v1connect.UnimplementedBackrestHandler
	cfg *v1.Config
}

func (f *fakeBackrest) GetConfig(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[v1.Config], error) {
	return connect.NewResponse(f.cfg), nil
}

func secretConfig() *v1.Config {
	return &v1.Config{
		Repos: []*v1.Repo{{
			Id: "r", Uri: "rest:https://user:repopass1@host/", Password: "repo-secret",
			Env: []string{"AWS_SECRET_ACCESS_KEY=aws-secret"}, Flags: []string{"--option=sftp.password=flagpass"},
			Hooks: []*v1.Hook{{Action: &v1.Hook_ActionWebhook{ActionWebhook: &v1.Hook_Webhook{WebhookUrl: "https://hook.example/tok3n"}}}},
		}},
		Plans: []*v1.Plan{{Id: "p", Repo: "r", Paths: []string{"/data"},
			Hooks: []*v1.Hook{{Action: &v1.Hook_ActionCommand{ActionCommand: &v1.Hook_Command{Command: "curl -H 'X: cmdsecret'"}}}}}},
		Auth: &v1.Auth{Users: []*v1.User{{Name: "admin", Password: &v1.User_PasswordBcrypt{PasswordBcrypt: "bcrypthash"}}}},
	}
}

func TestGetConfigRedactedForViewer(t *testing.T) {
	b := &Backrest{BackrestHandler: &fakeBackrest{cfg: secretConfig()}, Policy: policy}
	resp, err := b.GetConfig(viewer(), connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatal(err)
	}
	s := resp.Msg.String()
	for _, secret := range []string{"repopass1", "repo-secret", "aws-secret", "flagpass", "tok3n", "cmdsecret", "bcrypthash"} {
		if strings.Contains(s, secret) {
			t.Errorf("viewer config contains %q", secret)
		}
	}
	if resp.Msg.Repos[0].Id != "r" || resp.Msg.Plans[0].Paths[0] != "/data" || !strings.HasPrefix(resp.Msg.Repos[0].Env[0], "AWS_SECRET_ACCESS_KEY=") {
		t.Errorf("non-secret fields changed: %v", s)
	}
	// Admins and local users get the full configuration; the original is
	// not modified.
	full, _ := b.GetConfig(admin(), connect.NewRequest(&emptypb.Empty{}))
	if full.Msg.Repos[0].Password != "repo-secret" {
		t.Error("admin config redacted")
	}
}
