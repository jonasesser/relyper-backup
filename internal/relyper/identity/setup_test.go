package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupNeverReturnsSecret(t *testing.T) {
	env := map[string]string{
		"RELYPER_OIDC_ISSUER": "https://id.example", "RELYPER_OIDC_CLIENT_ID": "backup",
		"RELYPER_OIDC_CLIENT_SECRET": "s3cr3t-value",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	h := SetupHandler(SetupOptions{Config: cfg, BaseURL: func(*http.Request) string { return "https://backup.example/" },
		LocalAuth: func() bool { return true }, Getenv: func(k string) string { return env[k] }})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathSetup, nil))
	if strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Fatalf("secret leaked: %s", rec.Body.String())
	}
	var s Setup
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Configured || !s.SecretSet || s.ClientID != "backup" || s.CallbackURL != "https://backup.example/auth/oidc/callback" || s.Enabled {
		t.Fatalf("unexpected %+v", s)
	}
}

func TestSetupReportsError(t *testing.T) {
	env := map[string]string{"RELYPER_OIDC_ISSUER": "https://id.example"}
	s := BuildSetup(SetupOptions{ConfigErr: errors.New("RELYPER_OIDC_CLIENT_ID is required"), Getenv: func(k string) string { return env[k] }},
		httptest.NewRequest(http.MethodGet, PathSetup, nil))
	if !s.Configured || s.Error == "" || s.Issuer != "https://id.example" {
		t.Fatalf("unexpected %+v", s)
	}
}
