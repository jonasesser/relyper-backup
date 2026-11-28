package identity

// relyper: sessions for the customer portal (docs/CONCEPT-MULTITENANT.md).
//
// Portal sessions are signed with their own key and issuer. VerifySession
// (the operator verifier used by the web UI middleware) never accepts them,
// and VerifyPortal never accepts operator sessions, so a customer token
// cannot call the Backrest API and an operator token cannot act as a
// customer.

import (
	"github.com/golang-jwt/jwt/v5"
)

const (
	portalIssuer   = "relyper-backup/portal"
	portalAudience = "relyper-portal"
)

// Membership is what the portal gate grants an identity.
type Membership struct {
	Tenant   string // internal tenant ID
	External string // claim value that matched the tenant
	Admin    bool   // customer admin role
}

// PortalGate decides whether an identity may use the customer portal.
type PortalGate interface {
	// Admit returns the membership or ok=false. It is called at sign-in;
	// the portal checks the membership again on every request.
	Admit(id *Identity) (m Membership, ok bool)
}

type portalClaims struct {
	jwt.RegisteredClaims
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Roles    []string `json:"roles"`
	Tenant   string   `json:"tenant"`
	External string   `json:"ext"`
}

// PortalSession is a verified portal session.
type PortalSession struct {
	Subject  string
	Email    string
	Name     string
	Roles    []string
	Tenant   string
	External string
}

// UserName is used in the audit log.
func (p *PortalSession) UserName() string { return "relyper:" + p.Email }

func (s *Service) issuePortalSession(id *Identity, m Membership) (string, error) {
	now := s.now()
	c := portalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: portalIssuer, Subject: id.Subject, ID: randomString(16), Audience: jwt.ClaimStrings{portalAudience},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.SessionTTL)),
		},
		Email: id.Email, Name: id.DisplayName, Roles: id.Roles, Tenant: m.Tenant, External: m.External,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.portalKey)
}

// VerifyPortal checks a portal bearer token. The caller must still check the
// tenant against the current tenant store.
func (s *Service) VerifyPortal(token string) (*PortalSession, bool) {
	if s == nil || token == "" {
		return nil, false
	}
	var c portalClaims
	t, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) { return s.portalKey, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(portalIssuer),
		jwt.WithAudience(portalAudience), jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.now))
	if err != nil || !t.Valid || c.Subject == "" || c.Email == "" || c.Tenant == "" {
		return nil, false
	}
	return &PortalSession{Subject: c.Subject, Email: c.Email, Name: c.Name, Roles: c.Roles, Tenant: c.Tenant, External: c.External}, true
}

// PortalEnabled reports whether customers can sign in.
func (s *Service) PortalEnabled() bool { return s != nil && s.opt.Portal != nil && s.Enabled() }
