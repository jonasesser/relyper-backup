// Relyper-owned: the sign-in setup view shown in Settings. It tells an
// administrator whether Relyper Identity is configured, why it is not
// offered, and which callback URL to register at the provider. It never
// returns the client secret.
package identity

import (
	"encoding/json"
	"net/http"
	"strings"
)

// PathSetup is mounted behind the web UI authentication.
const PathSetup = "/relyper/identity/setup"

// Setup describes the effective configuration without secrets.
type Setup struct {
	Configured     bool     `json:"configured"`      // RELYPER_OIDC_ISSUER is set
	Enabled        bool     `json:"enabled"`         // sign-in is offered on the login page
	Error          string   `json:"error,omitempty"` // configuration error
	LocalAuth      bool     `json:"local_auth"`      // web UI authentication is on
	Issuer         string   `json:"issuer,omitempty"`
	ClientID       string   `json:"client_id,omitempty"`
	SecretSet      bool     `json:"client_secret_set"`
	CallbackURL    string   `json:"callback_url"`
	PublicURLSet   bool     `json:"public_url_set"`
	LoginRole      string   `json:"login_role"`
	AdminRole      string   `json:"admin_role"`
	Scopes         []string `json:"scopes"`
	DisplayName    string   `json:"display_name"`
	SessionHours   int      `json:"session_hours"`
	Variables      []string `json:"variables"`
	RequiredClaims string   `json:"required_claims"`
}

// SetupOptions are the inputs of the setup view.
type SetupOptions struct {
	Config    *Config // nil if not configured or invalid
	ConfigErr error
	Service   *Service // nil if sign-in is not running
	BaseURL   func(r *http.Request) string
	LocalAuth func() bool
	Getenv    func(string) string
}

// Variables are the environment variables that configure the sign-in.
var Variables = []string{
	"RELYPER_OIDC_ISSUER", "RELYPER_OIDC_CLIENT_ID", "RELYPER_OIDC_CLIENT_SECRET",
	"RELYPER_OIDC_CLIENT_SECRET_FILE", "RELYPER_OIDC_REDIRECT_URI", "RELYPER_PUBLIC_URL",
	"RELYPER_OIDC_LOGIN_ROLE", "RELYPER_OIDC_ADMIN_ROLE", "RELYPER_OIDC_SCOPES",
	"RELYPER_OIDC_DISPLAY_NAME", "RELYPER_OIDC_SESSION_HOURS",
}

// SetupHandler answers GET PathSetup.
func SetupHandler(o SetupOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		noStore(w)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(BuildSetup(o, r))
	})
}

// BuildSetup computes the view for one request.
func BuildSetup(o SetupOptions, r *http.Request) Setup {
	getenv := o.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	s := Setup{
		Configured:     strings.TrimSpace(getenv("RELYPER_OIDC_ISSUER")) != "",
		LocalAuth:      o.LocalAuth != nil && o.LocalAuth(),
		PublicURLSet:   strings.TrimSpace(getenv("RELYPER_PUBLIC_URL")) != "",
		LoginRole:      defaultLoginRole,
		AdminRole:      defaultAdminRole,
		Scopes:         DefaultScopes,
		DisplayName:    "Relyper",
		SessionHours:   int(defaultSessionTTL.Hours()),
		Variables:      Variables,
		RequiredClaims: "roles (list of role names) and sub; email or preferred_username for display",
	}
	if o.ConfigErr != nil {
		s.Error = o.ConfigErr.Error()
	}
	if c := o.Config; c != nil {
		s.Issuer, s.ClientID, s.SecretSet = c.Issuer, c.ClientID, c.ClientSecret != ""
		s.LoginRole, s.AdminRole, s.Scopes = c.LoginRole, c.AdminRole, c.Scopes
		s.DisplayName, s.SessionHours = c.DisplayName, int(c.SessionTTL.Hours())
	} else if s.Configured {
		s.Issuer = strings.TrimSpace(getenv("RELYPER_OIDC_ISSUER"))
		s.ClientID = strings.TrimSpace(getenv("RELYPER_OIDC_CLIENT_ID"))
	}
	if o.Config != nil && o.Config.RedirectURI != "" {
		s.CallbackURL = o.Config.RedirectURI
	} else if o.BaseURL != nil {
		s.CallbackURL = strings.TrimRight(o.BaseURL(r), "/") + PathCallback
	}
	if o.Service != nil {
		s.Enabled = o.Service.Enabled()
	}
	return s
}
