package roles

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/auth"
)

func TestDescribe(t *testing.T) {
	p := &Policy{IsAdmin: policy.IsAdmin, Tenant: func() string { return "relyper" }, ProviderName: func() string { return "Relyper" }}

	v := p.Describe(viewer())
	if v.Method != "relyper" || v.Email != "u@example.com" || v.User != "relyper:u@example.com" || !v.ReadOnly || v.Admin || v.Tenant != "relyper" || v.ProviderName != "Relyper" {
		t.Fatalf("viewer: %+v", v)
	}
	if a := p.Describe(admin()); !a.Admin || a.ReadOnly || a.Method != "relyper" {
		t.Fatalf("admin: %+v", a)
	}
	local := context.WithValue(context.Background(), auth.UserContextKey, &v1.User{Name: "breakglass", Password: &v1.User_PasswordBcrypt{PasswordBcrypt: "secret"}})
	if l := p.Describe(local); l.Method != "local" || l.User != "breakglass" || !l.Admin || l.Tenant != "" {
		t.Fatalf("local: %+v", l)
	}
	if n := p.Describe(context.Background()); n.Method != "none" || !n.Admin {
		t.Fatalf("none: %+v", n)
	}

	rec := httptest.NewRecorder()
	p.MeHandler().ServeHTTP(rec, httptest.NewRequest("GET", PathMe, nil).WithContext(local))
	if body := rec.Body.String(); !json.Valid([]byte(body)) || strings.Contains(body, "secret") || !strings.Contains(body, `"method":"local"`) {
		t.Fatalf("body: %s", body)
	}
}
