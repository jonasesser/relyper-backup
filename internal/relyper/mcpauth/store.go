// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcpauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Token kinds.
const (
	KindPersonal = "personal" // created by a user in the web UI
	KindOAuth    = "oauth"    // issued to an OAuth client after consent
)

// Secret prefixes make leaked secrets easy to recognize (and to scan for).
const (
	personalPrefix = "rbk_pat_"
	accessPrefix   = "rbk_oat_"
	refreshPrefix  = "rbk_ort_"
)

// Lifetimes of OAuth credentials.
const (
	AccessTokenLifetime  = time.Hour
	RefreshTokenLifetime = 30 * 24 * time.Hour
	MaxPersonalLifetime  = 366 * 24 * time.Hour
)

var (
	ErrInvalidToken = errors.New("invalid or expired token")
	ErrNotFound     = errors.New("not found")
)

// Token is one credential: a personal access token or an OAuth grant. Only
// hashes of the secrets are stored.
type Token struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Scopes    []string  `json:"scopes"`
	CreatedBy string    `json:"createdBy"` // user who created or consented
	CreatedAt time.Time `json:"createdAt"`
	// ExpiresAt is when the access secret stops working. For OAuth grants it
	// moves forward with every refresh.
	ExpiresAt  time.Time `json:"expiresAt"`
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`
	RevokedAt  time.Time `json:"revokedAt,omitzero"`

	SecretHash string `json:"secretHash"`

	// OAuth only.
	ClientID         string    `json:"clientId,omitempty"`
	RefreshHash      string    `json:"refreshHash,omitempty"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt,omitzero"`
	Resource         string    `json:"resource,omitempty"`
}

// Active reports whether the access secret is currently usable.
func (t *Token) Active(now time.Time) bool {
	return t.RevokedAt.IsZero() && now.Before(t.ExpiresAt)
}

// Client is an OAuth client registered through dynamic client registration.
type Client struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirectUris"`
	CreatedAt    time.Time `json:"createdAt"`
}

type fileData struct {
	Version int       `json:"version"`
	Tokens  []*Token  `json:"tokens"`
	Clients []*Client `json:"clients"`
}

// Store persists tokens and clients in one JSON file (mode 0600).
type Store struct {
	path string
	now  func() time.Time

	mu       sync.Mutex
	data     fileData
	lastSave time.Time
}

// Limits keep an unauthenticated registration endpoint from filling the disk.
const (
	maxClients = 200
	maxTokens  = 1000
)

// OpenStore loads the store at path, creating an empty one if missing.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.data = fileData{Version: 1}
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	default:
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	s.lastSave = s.now()
	b, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomSecret(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func randomID(prefix string) string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func hashEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// pruneLocked drops entries that can never be used again.
func (s *Store) pruneLocked() {
	now := s.now()
	s.data.Tokens = slices.DeleteFunc(s.data.Tokens, func(t *Token) bool {
		end := t.ExpiresAt
		if t.RefreshExpiresAt.After(end) {
			end = t.RefreshExpiresAt
		}
		if !t.RevokedAt.IsZero() && t.RevokedAt.After(end) {
			end = t.RevokedAt
		}
		// keep 30 days for the UI and the audit trail
		return now.Sub(end) > 30*24*time.Hour
	})
}

// CreatePersonal creates a personal access token and returns its secret.
// The secret is not stored and cannot be shown again.
func (s *Store) CreatePersonal(name string, scopes []string, lifetime time.Duration, createdBy string) (*Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, "", errors.New("name must be 1 to 100 characters")
	}
	scopes, err := NormalizeScopes(scopes)
	if err != nil {
		return nil, "", err
	}
	if len(scopes) == 0 {
		return nil, "", errors.New("at least one scope is required")
	}
	if lifetime <= 0 || lifetime > MaxPersonalLifetime {
		return nil, "", fmt.Errorf("lifetime must be between 1 day and %d days", int(MaxPersonalLifetime.Hours()/24))
	}
	secret := randomSecret(personalPrefix)
	now := s.now()
	t := &Token{
		ID:         randomID("tok_"),
		Kind:       KindPersonal,
		Name:       name,
		Scopes:     scopes,
		CreatedBy:  createdBy,
		CreatedAt:  now,
		ExpiresAt:  now.Add(lifetime),
		SecretHash: hashSecret(secret),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if len(s.data.Tokens) >= maxTokens {
		return nil, "", errors.New("too many tokens, revoke unused ones first")
	}
	s.data.Tokens = append(s.data.Tokens, t)
	if err := s.saveLocked(); err != nil {
		return nil, "", err
	}
	c := *t
	return &c, secret, nil
}

// Verify returns the token for an access secret if it is active.
func (s *Store) Verify(secret string) (*Token, error) {
	if !strings.HasPrefix(secret, personalPrefix) && !strings.HasPrefix(secret, accessPrefix) {
		return nil, ErrInvalidToken
	}
	h := hashSecret(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, t := range s.data.Tokens {
		if !hashEqual(t.SecretHash, h) {
			continue
		}
		if !t.Active(now) {
			return nil, ErrInvalidToken
		}
		t.LastUsedAt = now
		// last-used is informational; do not write the file on every request
		if now.Sub(s.lastSave) > time.Minute {
			_ = s.saveLocked()
		}
		c := *t
		return &c, nil
	}
	return nil, ErrInvalidToken
}

// Get returns a token by ID, or nil.
func (s *Store) Get(id string) *Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.data.Tokens {
		if t.ID == id {
			c := *t
			return &c
		}
	}
	return nil
}

// List returns all tokens, newest first.
func (s *Store) List() []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Token, 0, len(s.data.Tokens))
	for _, t := range s.data.Tokens {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b Token) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out
}

// Revoke revokes a token or OAuth grant immediately.
func (s *Store) Revoke(id string) (*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.data.Tokens {
		if t.ID != id {
			continue
		}
		if t.RevokedAt.IsZero() {
			t.RevokedAt = s.now()
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
		}
		c := *t
		return &c, nil
	}
	return nil, ErrNotFound
}

// RegisterClient stores a new OAuth client.
func (s *Store) RegisterClient(name string, redirectURIs []string) (*Client, error) {
	c := &Client{ID: randomID("cli_"), Name: name, RedirectURIs: redirectURIs, CreatedAt: s.now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Clients) >= maxClients {
		// drop the oldest client that has no live grant
		live := map[string]bool{}
		now := s.now()
		for _, t := range s.data.Tokens {
			if t.Kind == KindOAuth && t.RevokedAt.IsZero() && now.Before(t.RefreshExpiresAt) {
				live[t.ClientID] = true
			}
		}
		idx := slices.IndexFunc(s.data.Clients, func(c *Client) bool { return !live[c.ID] })
		if idx < 0 {
			return nil, errors.New("too many registered clients")
		}
		s.data.Clients = slices.Delete(s.data.Clients, idx, idx+1)
	}
	s.data.Clients = append(s.data.Clients, c)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	cc := *c
	return &cc, nil
}

// GetClient returns a registered client, or nil.
func (s *Store) GetClient(id string) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.data.Clients {
		if c.ID == id {
			cc := *c
			return &cc
		}
	}
	return nil
}

// IssueGrant creates an OAuth grant and returns its access and refresh secrets.
func (s *Store) IssueGrant(client *Client, scopes []string, user, resource string) (*Token, string, string, error) {
	access, refresh := randomSecret(accessPrefix), randomSecret(refreshPrefix)
	now := s.now()
	t := &Token{
		ID:               randomID("tok_"),
		Kind:             KindOAuth,
		Name:             client.Name,
		Scopes:           scopes,
		CreatedBy:        user,
		CreatedAt:        now,
		ExpiresAt:        now.Add(AccessTokenLifetime),
		SecretHash:       hashSecret(access),
		ClientID:         client.ID,
		RefreshHash:      hashSecret(refresh),
		RefreshExpiresAt: now.Add(RefreshTokenLifetime),
		Resource:         resource,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if len(s.data.Tokens) >= maxTokens {
		return nil, "", "", errors.New("too many tokens")
	}
	s.data.Tokens = append(s.data.Tokens, t)
	if err := s.saveLocked(); err != nil {
		return nil, "", "", err
	}
	c := *t
	return &c, access, refresh, nil
}

// Refresh rotates the secrets of the grant that owns refreshSecret. The old
// refresh secret stops working.
func (s *Store) Refresh(refreshSecret, clientID string) (*Token, string, string, error) {
	if !strings.HasPrefix(refreshSecret, refreshPrefix) {
		return nil, "", "", ErrInvalidToken
	}
	h := hashSecret(refreshSecret)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, t := range s.data.Tokens {
		if t.Kind != KindOAuth || !hashEqual(t.RefreshHash, h) {
			continue
		}
		if t.ClientID != clientID || !t.RevokedAt.IsZero() || !now.Before(t.RefreshExpiresAt) {
			return nil, "", "", ErrInvalidToken
		}
		access, refresh := randomSecret(accessPrefix), randomSecret(refreshPrefix)
		t.SecretHash = hashSecret(access)
		t.RefreshHash = hashSecret(refresh)
		t.ExpiresAt = now.Add(AccessTokenLifetime)
		t.RefreshExpiresAt = now.Add(RefreshTokenLifetime)
		t.LastUsedAt = now
		if err := s.saveLocked(); err != nil {
			return nil, "", "", err
		}
		c := *t
		return &c, access, refresh, nil
	}
	return nil, "", "", ErrInvalidToken
}
