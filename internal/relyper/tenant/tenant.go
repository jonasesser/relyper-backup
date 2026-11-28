// Package tenant is Relyper-owned: customers (tenants) of a Relyper Backup
// instance that is run as a service. A tenant has assigned plans and
// repositories, notification settings and recovery kit records. The store is
// a separate file (<data>/relyper/tenants.json), so upstream config
// migrations never see it. Design: docs/CONCEPT-MULTITENANT.md.
package tenant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Notification events a tenant can subscribe to.
const (
	EventFailure = "failure"
	EventWarning = "warning"
	EventSuccess = "success"
	EventStale   = "stale"
)

// AllEvents in display order.
var AllEvents = []string{EventFailure, EventWarning, EventSuccess, EventStale}

// DefaultEvents are enabled for new tenants.
var DefaultEvents = []string{EventFailure, EventWarning, EventStale}

const (
	maxRecipients = 10
	// IDPrefix is put in front of plans and repositories that customers
	// create in the portal: t-<tenant>-<name>.
	IDPrefix = "t-"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// ErrNotFound is returned for unknown tenants.
var ErrNotFound = errors.New("tenant not found")

// Notify are the e-mail settings of a tenant.
type Notify struct {
	Recipients []string `json:"recipients,omitempty"`
	Events     []string `json:"events,omitempty"`
}

// Has reports whether the event is enabled.
func (n Notify) Has(event string) bool { return slices.Contains(n.Events, event) }

// Kit records a recovery kit (customer key) for one repository. The key
// itself is never stored.
type Kit struct {
	Repo      string    `json:"repo"`
	KeyID     string    `json:"keyId"` // restic key ID of the customer key
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy"`
	Escrowed  bool      `json:"escrowed"`
	// EscrowFile is the sealed copy, relative to the escrow directory.
	EscrowFile string `json:"escrowFile,omitempty"`
}

// Tenant is one customer.
type Tenant struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ExternalIDs []string  `json:"externalIds,omitempty"` // values of the identity claim
	Suspended   bool      `json:"suspended,omitempty"`
	Escrow      bool      `json:"escrow,omitempty"`
	Plans       []string  `json:"plans,omitempty"`
	Repos       []string  `json:"repos,omitempty"`
	OwnRepos    []string  `json:"ownRepos,omitempty"` // subset of Repos connected by the customer
	Notify      Notify    `json:"notify"`
	Kits        []Kit     `json:"kits,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// HasPlan reports whether the plan is assigned.
func (t *Tenant) HasPlan(id string) bool { return slices.Contains(t.Plans, id) }

// HasRepo reports whether the repository is assigned.
func (t *Tenant) HasRepo(id string) bool { return slices.Contains(t.Repos, id) }

// IsOwnRepo reports whether the customer connected the repository.
func (t *Tenant) IsOwnRepo(id string) bool { return slices.Contains(t.OwnRepos, id) }

// Kit returns the current kit of a repository or nil.
func (t *Tenant) Kit(repo string) *Kit {
	for i := range t.Kits {
		if t.Kits[i].Repo == repo {
			return &t.Kits[i]
		}
	}
	return nil
}

// MatchesExternal reports whether a claim value belongs to this tenant.
func (t *Tenant) MatchesExternal(v string) bool {
	if v == "" {
		return false
	}
	if len(t.ExternalIDs) == 0 {
		return v == t.ID
	}
	return slices.Contains(t.ExternalIDs, v)
}

func (t *Tenant) clone() *Tenant {
	c := *t
	c.ExternalIDs = slices.Clone(t.ExternalIDs)
	c.Plans = slices.Clone(t.Plans)
	c.Repos = slices.Clone(t.Repos)
	c.OwnRepos = slices.Clone(t.OwnRepos)
	c.Notify.Recipients = slices.Clone(t.Notify.Recipients)
	c.Notify.Events = slices.Clone(t.Notify.Events)
	c.Kits = slices.Clone(t.Kits)
	return &c
}

type file struct {
	Version int       `json:"version"`
	Tenants []*Tenant `json:"tenants"`
}

// Store keeps the tenants. Safe for concurrent use; every change is written
// to disk before it is visible.
type Store struct {
	path    string
	mu      sync.RWMutex
	tenants map[string]*Tenant
	now     func() time.Time
}

// Open loads or creates the store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, tenants: map[string]*Tenant{}, now: time.Now}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, t := range f.Tenants {
		if t != nil && t.ID != "" {
			s.tenants[t.ID] = t
		}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	f := file{Version: 1}
	for _, t := range s.tenants {
		f.Tenants = append(f.Tenants, t)
	}
	sort.Slice(f.Tenants, func(i, j int) bool { return f.Tenants[i].ID < f.Tenants[j].ID })
	b, err := json.MarshalIndent(f, "", "  ")
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

// Path is the file the store is saved in.
func (s *Store) Path() string { return s.path }

// List returns copies of all tenants sorted by ID.
func (s *Store) List() []*Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns a copy of a tenant.
func (s *Store) Get(id string) (*Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	return t.clone(), nil
}

// ByExternal returns the active tenant whose external IDs contain v.
func (s *Store) ByExternal(v string) (*Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tenants {
		if !t.Suspended && t.MatchesExternal(v) {
			return t.clone(), true
		}
	}
	return nil, false
}

// OwnerOfPlan returns the tenant a plan is assigned to.
func (s *Store) OwnerOfPlan(planID string) (*Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tenants {
		if t.HasPlan(planID) {
			return t.clone(), true
		}
	}
	return nil, false
}

// OwnerOfRepo returns the tenant a repository is assigned to.
func (s *Store) OwnerOfRepo(repoID string) (*Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tenants {
		if t.HasRepo(repoID) {
			return t.clone(), true
		}
	}
	return nil, false
}

// ValidateID checks a tenant ID.
func ValidateID(id string) error {
	if !idPattern.MatchString(id) || strings.Contains(id, "--") {
		return errors.New("tenant ID must be 3–40 lowercase letters, digits or single hyphens")
	}
	return nil
}

func cleanName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len(n) > 100 || strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errors.New("name must be 1–100 characters without control characters")
	}
	return n, nil
}

func cleanExternal(ids []string) ([]string, error) {
	var out []string
	for _, v := range ids {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if len(v) > 200 || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, errors.New("external IDs must be up to 200 characters without control characters")
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, nil
}

// Create adds a tenant.
func (s *Store) Create(id, name string, external []string) (*Tenant, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	n, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	ext, err := cleanExternal(external)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[id]; ok {
		return nil, errors.New("a tenant with this ID exists")
	}
	if err := s.externalFreeLocked(id, ext); err != nil {
		return nil, err
	}
	t := &Tenant{ID: id, Name: n, ExternalIDs: ext, Notify: Notify{Events: slices.Clone(DefaultEvents)}, CreatedAt: s.now().UTC()}
	s.tenants[id] = t
	if err := s.saveLocked(); err != nil {
		delete(s.tenants, id)
		return nil, err
	}
	return t.clone(), nil
}

// externalFreeLocked makes sure no claim value maps to two tenants.
func (s *Store) externalFreeLocked(self string, ext []string) error {
	values := ext
	if len(values) == 0 {
		values = []string{self}
	}
	for _, o := range s.tenants {
		if o.ID == self {
			continue
		}
		for _, v := range values {
			if o.MatchesExternal(v) {
				return fmt.Errorf("external ID %q already belongs to tenant %s", v, o.ID)
			}
		}
	}
	return nil
}

// Update changes a tenant under the lock and saves it. f works on a copy;
// the change is applied only if f and saving succeed.
func (s *Store) Update(id string, f func(t *Tenant) error) (*Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	t := cur.clone()
	if err := f(t); err != nil {
		return nil, err
	}
	if t.ID != id {
		return nil, errors.New("tenant ID cannot change")
	}
	s.tenants[id] = t
	if err := s.saveLocked(); err != nil {
		s.tenants[id] = cur
		return nil, err
	}
	return t.clone(), nil
}

// Settings an operator can change.
type Settings struct {
	Name        *string
	ExternalIDs *[]string
	Suspended   *bool
	Escrow      *bool
}

// Configure applies operator settings.
func (s *Store) Configure(id string, in Settings) (*Tenant, error) {
	var name string
	var ext []string
	var err error
	if in.Name != nil {
		if name, err = cleanName(*in.Name); err != nil {
			return nil, err
		}
	}
	if in.ExternalIDs != nil {
		if ext, err = cleanExternal(*in.ExternalIDs); err != nil {
			return nil, err
		}
	}
	return s.Update(id, func(t *Tenant) error {
		if in.Name != nil {
			t.Name = name
		}
		if in.ExternalIDs != nil {
			if err := s.externalFreeLocked(id, ext); err != nil {
				return err
			}
			t.ExternalIDs = ext
		}
		if in.Suspended != nil {
			t.Suspended = *in.Suspended
		}
		if in.Escrow != nil {
			t.Escrow = *in.Escrow
		}
		return nil
	})
}

// Delete removes a tenant. Plans and repositories stay in the Backrest
// configuration and fall back to the operator.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.tenants[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.tenants, id)
	if err := s.saveLocked(); err != nil {
		s.tenants[id] = cur
		return err
	}
	return nil
}

// AssignPlan assigns a plan; a plan belongs to at most one tenant.
func (s *Store) AssignPlan(id, planID string) (*Tenant, error) {
	return s.assign(id, planID, func(t *Tenant) *[]string { return &t.Plans }, "plan")
}

// AssignRepo assigns a repository; a repository belongs to at most one tenant.
func (s *Store) AssignRepo(id, repoID string) (*Tenant, error) {
	return s.assign(id, repoID, func(t *Tenant) *[]string { return &t.Repos }, "repository")
}

func (s *Store) assign(id, obj string, list func(*Tenant) *[]string, kind string) (*Tenant, error) {
	if obj == "" {
		return nil, fmt.Errorf("%s ID is required", kind)
	}
	return s.Update(id, func(t *Tenant) error {
		// Update holds the write lock.
		for _, o := range s.tenants {
			if o.ID != id && slices.Contains(*list(o), obj) {
				return fmt.Errorf("%s %s is already assigned to tenant %s", kind, obj, o.ID)
			}
		}
		l := list(t)
		if !slices.Contains(*l, obj) {
			*l = append(*l, obj)
			sort.Strings(*l)
		}
		return nil
	})
}

// UnassignPlan removes a plan from a tenant.
func (s *Store) UnassignPlan(id, planID string) (*Tenant, error) {
	return s.Update(id, func(t *Tenant) error {
		t.Plans = slices.DeleteFunc(t.Plans, func(p string) bool { return p == planID })
		return nil
	})
}

// UnassignRepo removes a repository (and its kit record) from a tenant.
func (s *Store) UnassignRepo(id, repoID string) (*Tenant, error) {
	return s.Update(id, func(t *Tenant) error {
		t.Repos = slices.DeleteFunc(t.Repos, func(p string) bool { return p == repoID })
		t.OwnRepos = slices.DeleteFunc(t.OwnRepos, func(p string) bool { return p == repoID })
		t.Kits = slices.DeleteFunc(t.Kits, func(k Kit) bool { return k.Repo == repoID })
		return nil
	})
}

// CleanNotify validates notification settings.
func CleanNotify(n Notify) (Notify, error) {
	var out Notify
	for _, r := range n.Recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		a, err := mail.ParseAddress(r)
		if err != nil || a.Name != "" || a.Address != r || strings.ContainsAny(r, "\r\n,;<>\"") {
			return Notify{}, fmt.Errorf("invalid e-mail address %q", r)
		}
		if !slices.Contains(out.Recipients, r) {
			out.Recipients = append(out.Recipients, r)
		}
	}
	if len(out.Recipients) > maxRecipients {
		return Notify{}, fmt.Errorf("at most %d recipients", maxRecipients)
	}
	for _, e := range n.Events {
		if !slices.Contains(AllEvents, e) {
			return Notify{}, fmt.Errorf("unknown event %q", e)
		}
		if !slices.Contains(out.Events, e) {
			out.Events = append(out.Events, e)
		}
	}
	return out, nil
}

// SetNotify stores notification settings.
func (s *Store) SetNotify(id string, n Notify) (*Tenant, error) {
	clean, err := CleanNotify(n)
	if err != nil {
		return nil, err
	}
	return s.Update(id, func(t *Tenant) error {
		t.Notify = clean
		return nil
	})
}

// ObjectID returns the ID for a plan or repository a customer creates.
func ObjectID(tenantID, name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`).MatchString(name) {
		return "", errors.New("name must be up to 31 lowercase letters, digits or hyphens")
	}
	id := IDPrefix + tenantID + "-" + name
	if len(id) > 50 {
		return "", errors.New("name too long for this tenant (IDs have at most 50 characters)")
	}
	return id, nil
}

// AddOwnRepo records a repository the customer connected.
func (s *Store) AddOwnRepo(id, repoID string) (*Tenant, error) {
	return s.Update(id, func(t *Tenant) error {
		for _, o := range s.tenants {
			if o.ID != id && o.HasRepo(repoID) {
				return fmt.Errorf("repository %s is already assigned to tenant %s", repoID, o.ID)
			}
		}
		if !t.HasRepo(repoID) {
			t.Repos = append(t.Repos, repoID)
			sort.Strings(t.Repos)
		}
		if !t.IsOwnRepo(repoID) {
			t.OwnRepos = append(t.OwnRepos, repoID)
			sort.Strings(t.OwnRepos)
		}
		return nil
	})
}

// SetKit replaces the kit record of a repository.
func (s *Store) SetKit(id string, k Kit) (*Tenant, error) {
	return s.Update(id, func(t *Tenant) error {
		if !t.HasRepo(k.Repo) {
			return errors.New("repository not assigned")
		}
		t.Kits = slices.DeleteFunc(t.Kits, func(o Kit) bool { return o.Repo == k.Repo })
		t.Kits = append(t.Kits, k)
		return nil
	})
}

// Prune removes assignments of plans and repositories that no longer exist,
// so a later object with the same ID is not given to the tenant by accident.
func (s *Store) Prune(plans, repos map[string]bool) (removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tenants {
		before := len(t.Plans) + len(t.Repos)
		t.Plans = slices.DeleteFunc(t.Plans, func(p string) bool { return !plans[p] })
		t.Repos = slices.DeleteFunc(t.Repos, func(p string) bool { return !repos[p] })
		t.OwnRepos = slices.DeleteFunc(t.OwnRepos, func(p string) bool { return !repos[p] })
		t.Kits = slices.DeleteFunc(t.Kits, func(k Kit) bool { return !repos[k.Repo] })
		removed += before - len(t.Plans) - len(t.Repos)
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, s.saveLocked()
}
