package tenant

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/garethgeorge/backrest/internal/relyper/identity"
)

// PortalConfig is read from the environment.
type PortalConfig struct {
	Enabled   bool
	Claim     string // tenant claim in the ID token
	Role      string // may view
	AdminRole string // may also manage
}

// PortalConfigFromEnv reads RELYPER_PORTAL, RELYPER_TENANT_CLAIM,
// RELYPER_CUSTOMER_ROLE and RELYPER_CUSTOMER_ADMIN_ROLE.
func PortalConfigFromEnv(getenv func(string) string) PortalConfig {
	if getenv == nil {
		getenv = os.Getenv
	}
	v := func(k, d string) string {
		if s := strings.TrimSpace(getenv(k)); s != "" {
			return s
		}
		return d
	}
	on := strings.ToLower(strings.TrimSpace(getenv("RELYPER_PORTAL")))
	return PortalConfig{
		Enabled:   on == "1" || on == "true" || on == "yes",
		Claim:     v("RELYPER_TENANT_CLAIM", "tenant"),
		Role:      v("RELYPER_CUSTOMER_ROLE", "backup_customer"),
		AdminRole: v("RELYPER_CUSTOMER_ADMIN_ROLE", "backup_customer_admin"),
	}
}

// Gate admits customers to the portal. It implements identity.PortalGate.
type Gate struct {
	Store *Store
	Cfg   PortalConfig
}

// IsCustomer reports whether the roles contain a customer role.
func (g *Gate) IsCustomer(roles []string) bool {
	return slices.Contains(roles, g.Cfg.Role) || slices.Contains(roles, g.Cfg.AdminRole)
}

// IsAdmin reports whether the roles contain the customer admin role.
func (g *Gate) IsAdmin(roles []string) bool { return slices.Contains(roles, g.Cfg.AdminRole) }

// ClaimValues returns the tenant claim as strings. It accepts a string, a
// number, an object with "id" or "slug", or an array of those.
func ClaimValues(v any) []string {
	var out []string
	var add func(v any, depth int)
	add = func(v any, depth int) {
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				out = append(out, s)
			}
		case float64:
			out = append(out, fmt.Sprintf("%.0f", t))
		case map[string]any:
			for _, k := range []string{"id", "slug"} {
				if s, ok := t[k].(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
					return
				}
				if f, ok := t[k].(float64); ok {
					out = append(out, fmt.Sprintf("%.0f", f))
					return
				}
			}
		case []any:
			if depth == 0 {
				for _, e := range t {
					add(e, 1)
				}
			}
		}
	}
	add(v, 0)
	return out
}

// Admit maps the identity to exactly one active tenant. Several matching
// tenants are refused as ambiguous.
func (g *Gate) Admit(id *identity.Identity) (identity.Membership, bool) {
	if g == nil || g.Store == nil || id == nil || !g.IsCustomer(id.Roles) {
		return identity.Membership{}, false
	}
	var found *Tenant
	var ext string
	for _, v := range ClaimValues(id.Claims[g.Cfg.Claim]) {
		t, ok := g.Store.ByExternal(v)
		if !ok {
			continue
		}
		if found != nil && found.ID != t.ID {
			return identity.Membership{}, false
		}
		found, ext = t, v
	}
	if found == nil {
		return identity.Membership{}, false
	}
	return identity.Membership{Tenant: found.ID, External: ext, Admin: g.IsAdmin(id.Roles)}, true
}

// Check re-validates a portal session against the current store and
// settings. It returns the tenant and whether the member is a customer
// admin.
func (g *Gate) Check(p *identity.PortalSession) (*Tenant, bool, bool) {
	if g == nil || p == nil || !g.IsCustomer(p.Roles) {
		return nil, false, false
	}
	t, err := g.Store.Get(p.Tenant)
	if err != nil || t.Suspended || !t.MatchesExternal(p.External) {
		return nil, false, false
	}
	return t, g.IsAdmin(p.Roles), true
}
