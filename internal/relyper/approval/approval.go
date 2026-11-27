// Package approval implements server-side approval of restricted MCP
// operations. A request is bound to the tool, a hash of its exact
// parameters and the token that asked for it; a signed-in user approves or
// denies it in the web UI; it can be used once and expires.
//
// Requests live in memory only: a restart discards pending approvals, which
// fails safe.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package approval

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Status of a request.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusDenied   Status = "denied"
	StatusUsed     Status = "used"
	StatusExpired  Status = "expired"
)

// DefaultTTL is how long a request waits for a decision, and how long an
// approved request can be executed.
const DefaultTTL = 15 * time.Minute

const maxRequests = 500

// Request is one approval request.
type Request struct {
	ID          string    `json:"id"`
	Tool        string    `json:"tool"`
	ParamsHash  string    `json:"paramsHash"`
	ParamsJSON  string    `json:"paramsJson"` // redacted, for display
	Summary     string    `json:"summary"`    // one line for the UI
	Preview     any       `json:"preview"`    // what would happen
	TokenID     string    `json:"tokenId"`
	TokenName   string    `json:"tokenName"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	DecidedBy   string    `json:"decidedBy,omitempty"`
	DecidedAt   time.Time `json:"decidedAt,omitzero"`
	DenyReason  string    `json:"denyReason,omitempty"`
	ExecutedAt  time.Time `json:"executedAt,omitzero"`
	OperationID int64     `json:"operationId,omitempty"`
}

// Errors returned by Consume. Their texts are shown to the MCP client.
var (
	ErrNotFound       = errors.New("approval request not found (it may have expired or the server restarted); call the tool again without approval_id to create a new request")
	ErrPending        = errors.New("approval request is still waiting for a decision in the Relyper Backup web UI")
	ErrDenied         = errors.New("approval request was denied")
	ErrUsed           = errors.New("approval request was already used; every execution needs a new approval")
	ErrExpired        = errors.New("approval request expired; call the tool again without approval_id to create a new request")
	ErrMismatch       = errors.New("approval request does not match this call: tool and all parameters must be exactly the same as when approval was requested")
	ErrOtherPrincipal = errors.New("approval request belongs to a different token")
)

// Store keeps approval requests in memory.
type Store struct {
	mu       sync.Mutex
	requests []*Request
	now      func() time.Time
	ttl      time.Duration
	// OnChange is called (without the lock held) after a request was created
	// or changed, e.g. to wake up the UI.
	OnChange func()
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{now: time.Now, ttl: DefaultTTL}
}

// HashParams returns the canonical hash of a tool call's parameters.
// encoding/json writes struct fields in declaration order and sorts map keys,
// so equal inputs give equal hashes.
func HashParams(tool string, params any) (string, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{0})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) expireLocked() {
	now := s.now()
	for _, r := range s.requests {
		if (r.Status == StatusPending || r.Status == StatusApproved) && !now.Before(r.ExpiresAt) {
			r.Status = StatusExpired
		}
	}
	// keep finished requests for a day so the UI can show them
	s.requests = slices.DeleteFunc(s.requests, func(r *Request) bool {
		return r.Status != StatusPending && r.Status != StatusApproved && now.Sub(r.ExpiresAt) > 24*time.Hour
	})
}

func (s *Store) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// Create stores a new pending request.
func (s *Store) Create(tool, paramsHash, paramsJSON, summary string, preview any, tokenID, tokenName string) (*Request, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	now := s.now()
	r := &Request{
		ID:         "apr_" + hex.EncodeToString(b),
		Tool:       tool,
		ParamsHash: paramsHash,
		ParamsJSON: paramsJSON,
		Summary:    summary,
		Preview:    preview,
		TokenID:    tokenID,
		TokenName:  tokenName,
		Status:     StatusPending,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.ttl),
	}
	s.mu.Lock()
	s.expireLocked()
	pending := 0
	for _, x := range s.requests {
		if x.Status == StatusPending && x.TokenID == tokenID {
			pending++
		}
	}
	if pending >= 20 {
		s.mu.Unlock()
		return nil, errors.New("too many pending approval requests for this token; decide or let them expire first")
	}
	if len(s.requests) >= maxRequests {
		s.mu.Unlock()
		return nil, errors.New("too many approval requests")
	}
	s.requests = append(s.requests, r)
	c := *r
	s.mu.Unlock()
	s.changed()
	return &c, nil
}

// Get returns a copy of a request, or nil.
func (s *Store) Get(id string) *Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	for _, r := range s.requests {
		if r.ID == id {
			c := *r
			return &c
		}
	}
	return nil
}

// List returns all known requests, newest first.
func (s *Store) List() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	out := make([]Request, 0, len(s.requests))
	for _, r := range s.requests {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b Request) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out
}

// PendingCount returns the number of requests waiting for a decision.
func (s *Store) PendingCount() int {
	n := 0
	for _, r := range s.List() {
		if r.Status == StatusPending {
			n++
		}
	}
	return n
}

// Decide approves or denies a pending request. user is the signed-in web UI
// user; MCP tokens can never reach this method.
func (s *Store) Decide(id string, approve bool, user, reason string) (*Request, error) {
	s.mu.Lock()
	s.expireLocked()
	var r *Request
	for _, x := range s.requests {
		if x.ID == id {
			r = x
		}
	}
	if r == nil {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if r.Status != StatusPending {
		s.mu.Unlock()
		return nil, fmt.Errorf("request is %s, not pending", r.Status)
	}
	now := s.now()
	r.DecidedBy, r.DecidedAt = user, now
	if approve {
		r.Status = StatusApproved
		// the client gets the full TTL from the decision to execute
		r.ExpiresAt = now.Add(s.ttl)
	} else {
		r.Status = StatusDenied
		r.DenyReason = reason
	}
	c := *r
	s.mu.Unlock()
	s.changed()
	return &c, nil
}

// Consume checks that request id approves exactly this call and marks it
// used. It must be called right before executing the operation.
func (s *Store) Consume(id, tool, paramsHash, tokenID string) (*Request, error) {
	s.mu.Lock()
	s.expireLocked()
	var r *Request
	for _, x := range s.requests {
		if x.ID == id {
			r = x
		}
	}
	if r == nil {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if r.TokenID != tokenID {
		s.mu.Unlock()
		return nil, ErrOtherPrincipal
	}
	if r.Tool != tool || r.ParamsHash != paramsHash {
		s.mu.Unlock()
		return nil, ErrMismatch
	}
	var err error
	switch r.Status {
	case StatusApproved:
		r.Status = StatusUsed
		r.ExecutedAt = s.now()
	case StatusPending:
		err = ErrPending
	case StatusDenied:
		err = ErrDenied
	case StatusUsed:
		err = ErrUsed
	default:
		err = ErrExpired
	}
	c := *r
	s.mu.Unlock()
	if err != nil {
		return &c, err
	}
	s.changed()
	return &c, nil
}

// SetOperation records the operation an executed request started.
func (s *Store) SetOperation(id string, opID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.ID == id {
			r.OperationID = opID
		}
	}
}
