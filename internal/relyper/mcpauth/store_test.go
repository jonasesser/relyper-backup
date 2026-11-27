// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcpauth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersonalTokenLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, secret, err := s.CreatePersonal("laptop", []string{ScopePlansManage, ScopeStatusRead, ScopeStatusRead}, 24*time.Hour, "jonas")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tok.Scopes, " "); got != "status:read plans:manage" {
		t.Errorf("scopes not normalized: %q", got)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), secret) {
		t.Fatal("secret stored in plain text")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("store mode %v, want 0600", st.Mode().Perm())
	}
	if _, err := s.Verify(secret); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := s.Verify(secret + "x"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong secret accepted: %v", err)
	}
	// reload from disk
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Verify(secret); err != nil {
		t.Fatalf("verify after reload: %v", err)
	}
	if _, err := s2.Revoke(tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Verify(secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("revoked token accepted")
	}
}

func TestTokenExpiry(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "a.json"))
	now := time.Now()
	s.now = func() time.Time { return now }
	_, secret, err := s.CreatePersonal("x", []string{ScopeStatusRead}, time.Hour, "u")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := s.Verify(secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expired token accepted")
	}
}

func TestCreatePersonalValidation(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "a.json"))
	if _, _, err := s.CreatePersonal("x", []string{"root"}, time.Hour, "u"); err == nil {
		t.Error("unknown scope accepted")
	}
	if _, _, err := s.CreatePersonal("x", nil, time.Hour, "u"); err == nil {
		t.Error("empty scopes accepted")
	}
	if _, _, err := s.CreatePersonal("x", []string{ScopeStatusRead}, 400*24*time.Hour, "u"); err == nil {
		t.Error("too long lifetime accepted")
	}
}

func TestOAuthGrantRefreshRotates(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "a.json"))
	c, err := s.RegisterClient("Claude", []string{"https://claude.ai/api/mcp/auth_callback"})
	if err != nil {
		t.Fatal(err)
	}
	_, access, refresh, err := s.IssueGrant(c, []string{ScopeStatusRead}, "jonas", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(access); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(refresh); err == nil {
		t.Fatal("refresh token accepted as access token")
	}
	if _, _, _, err := s.Refresh(refresh, "other-client"); err == nil {
		t.Fatal("refresh by another client accepted")
	}
	_, access2, refresh2, err := s.Refresh(refresh, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(access); err == nil {
		t.Error("old access token still valid after refresh")
	}
	if _, _, _, err := s.Refresh(refresh, c.ID); err == nil {
		t.Error("old refresh token reusable")
	}
	if _, err := s.Verify(access2); err != nil {
		t.Error(err)
	}
	if refresh2 == refresh {
		t.Error("refresh token not rotated")
	}
}

func TestValidRedirectURI(t *testing.T) {
	for uri, want := range map[string]bool{
		"https://claude.ai/api/mcp/auth_callback":               true,
		"https://chatgpt.com/connector_platform_oauth_redirect": true,
		"http://localhost:33418/callback":                       true,
		"http://127.0.0.1:5000/cb":                              true,
		"http://evil.example/cb":                                false,
		"javascript:alert(1)":                                   false,
		"https://x.example/cb#frag":                             false,
		"cursor://anysphere.cursor-retrieval/oauth/callback":    true,
		"": false,
	} {
		if got := validRedirectURI(uri); got != want {
			t.Errorf("validRedirectURI(%q) = %v, want %v", uri, got, want)
		}
	}
}
