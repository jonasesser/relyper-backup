// Package identity is Relyper-owned: sign-in through Relyper Identity, the
// Relyper OpenID Connect provider.
//
// It is the Go counterpart of the OIDC part of the TypeScript package
// @relyper/sp-auth (jonasesser/relyper-sp-auth) and makes the same checks:
// authorization code flow with PKCE S256, fresh state and nonce bound to the
// browser, a confidential client (client secret only sent to the token
// endpoint), ID tokens verified against the provider's JWKS with the
// algorithm pinned to RS256, iss/aud/azp/exp/nonce/sub checks, and a role
// gate on the "roles" claim at sign-in and on every request.
//
// Local user names and passwords keep working next to it as emergency access,
// so backups and restores never depend on the identity provider being up.
package identity

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read from RELYPER_OIDC_* environment variables.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURI must match the registration at the provider byte for byte.
	// Empty means <public URL>/auth/oidc/callback.
	RedirectURI string
	Scopes      []string
	// LoginRole is needed to sign in; AdminRole also allows signing in.
	LoginRole string
	// AdminRole is needed to approve MCP requests and manage MCP tokens.
	AdminRole string
	// DisplayName is shown on the sign-in button.
	DisplayName string
	SessionTTL  time.Duration
	// OperatorTenant is the tenant whose members may hold LoginRole and
	// AdminRole. Relyper Identity issues application roles per tenant, so
	// an operator role in a customer's tenant (for example by a mistake in
	// a team template) must not make that customer an operator. AnyTenant
	// switches the check off for providers without tenants.
	OperatorTenant string
}

// AnyTenant as OperatorTenant accepts operator roles from any tenant.
const AnyTenant = "*"

// DefaultScopes are the scopes requested from the provider.
var DefaultScopes = []string{"openid", "email", "profile", "roles"}

const (
	defaultLoginRole = "backup_user"
	defaultAdminRole = "backup_admin"
	// defaultOperatorTenant is the master tenant of Relyper Identity
	// (RELYPER_MASTER_TENANT_ID there).
	defaultOperatorTenant = "relyper"
	defaultSessionTTL     = 8 * time.Hour
)

// ConfigFromEnv reads the configuration. It returns (nil, nil) when
// RELYPER_OIDC_ISSUER is not set, i.e. the feature is off.
func ConfigFromEnv(getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	issuer := strings.TrimSpace(getenv("RELYPER_OIDC_ISSUER"))
	if issuer == "" {
		return nil, nil
	}
	c := &Config{
		Issuer:         issuer,
		ClientID:       strings.TrimSpace(getenv("RELYPER_OIDC_CLIENT_ID")),
		RedirectURI:    strings.TrimSpace(getenv("RELYPER_OIDC_REDIRECT_URI")),
		LoginRole:      strings.TrimSpace(getenv("RELYPER_OIDC_LOGIN_ROLE")),
		AdminRole:      strings.TrimSpace(getenv("RELYPER_OIDC_ADMIN_ROLE")),
		DisplayName:    strings.TrimSpace(getenv("RELYPER_OIDC_DISPLAY_NAME")),
		OperatorTenant: strings.TrimSpace(getenv("RELYPER_OIDC_OPERATOR_TENANT")),
		SessionTTL:     defaultSessionTTL,
		Scopes:         DefaultScopes,
	}
	c.ClientSecret = getenv("RELYPER_OIDC_CLIENT_SECRET")
	if f := strings.TrimSpace(getenv("RELYPER_OIDC_CLIENT_SECRET_FILE")); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("RELYPER_OIDC_CLIENT_SECRET_FILE: %w", err)
		}
		c.ClientSecret = strings.TrimSpace(string(b))
	}
	if s := strings.Fields(getenv("RELYPER_OIDC_SCOPES")); len(s) > 0 {
		c.Scopes = s
	}
	if c.LoginRole == "" {
		c.LoginRole = defaultLoginRole
	}
	if c.AdminRole == "" {
		c.AdminRole = defaultAdminRole
	}
	if c.OperatorTenant == "" {
		c.OperatorTenant = defaultOperatorTenant
	}
	if c.DisplayName == "" {
		c.DisplayName = "Relyper"
	}
	if h := strings.TrimSpace(getenv("RELYPER_OIDC_SESSION_HOURS")); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil || n < 1 || n > 168 {
			return nil, errors.New("RELYPER_OIDC_SESSION_HOURS must be a whole number from 1 to 168")
		}
		c.SessionTTL = time.Duration(n) * time.Hour
	}
	return c, c.validate()
}

func (c *Config) validate() error {
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return errors.New("RELYPER_OIDC_ISSUER must be an https URL")
	}
	if c.ClientID == "" {
		return errors.New("RELYPER_OIDC_CLIENT_ID is required")
	}
	if c.ClientSecret == "" {
		return errors.New("RELYPER_OIDC_CLIENT_SECRET or RELYPER_OIDC_CLIENT_SECRET_FILE is required")
	}
	if c.RedirectURI != "" {
		r, err := url.Parse(c.RedirectURI)
		if err != nil || r.Host == "" || (r.Scheme != "https" && !(r.Scheme == "http" && isLoopback(r.Hostname()))) {
			return errors.New("RELYPER_OIDC_REDIRECT_URI must be an https URL")
		}
		if r.Path != "/auth/oidc/callback" {
			return errors.New("RELYPER_OIDC_REDIRECT_URI must end in /auth/oidc/callback")
		}
	}
	hasOpenID := false
	for _, s := range c.Scopes {
		if s == "openid" {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		return errors.New("RELYPER_OIDC_SCOPES must contain openid")
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
