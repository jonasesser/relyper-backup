package portal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
	"google.golang.org/protobuf/proto"
)

// Storage connects repositories that customers own.
type Storage struct {
	Store *tenant.Store
	// AddRepo initializes (or opens) and saves the repository, like the
	// Backrest AddRepo call. It returns the saved repository with its GUID.
	AddRepo func(ctx context.Context, r *v1.Repo) (*v1.Repo, error)
	// RemoveRepo deletes the repository entry (never data in the storage).
	RemoveRepo func(ctx context.Context, id string) error
	// Transform changes the configuration atomically.
	Transform func(fn func(cfg *v1.Config) error) error
	Config    func() (*v1.Config, error)
	// Resolve looks up a host name; defaults to net.DefaultResolver.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
}

// StorageRequest is what a customer enters. Only the fields of the chosen
// type are used.
type StorageRequest struct {
	Name string `json:"name"`
	Type string `json:"type"` // s3, b2, azure, rest
	// Existing: open an existing repository with Password instead of
	// creating a new one with a generated key.
	Existing bool   `json:"existing"`
	Password string `json:"password,omitempty"`

	Endpoint  string `json:"endpoint,omitempty"` // s3: https://host[:port]; rest: https URL
	Bucket    string `json:"bucket,omitempty"`   // s3, b2; azure: container
	Path      string `json:"path,omitempty"`
	Region    string `json:"region,omitempty"`
	KeyID     string `json:"keyId,omitempty"`     // s3 access key, b2 account ID, azure account name, rest user
	SecretKey string `json:"secretKey,omitempty"` // s3 secret, b2 key, azure key, rest password
}

var (
	reBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{1,62}$`)
	rePath   = regexp.MustCompile(`^[A-Za-z0-9._\-/]{0,200}$`)
	reRegion = regexp.MustCompile(`^[a-z0-9\-]{0,40}$`)
	reHost   = regexp.MustCompile(`^[A-Za-z0-9.\-]{1,253}$`)
	reKeyID  = regexp.MustCompile(`^[A-Za-z0-9._@+\-]{1,200}$`)
)

func printable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x21 || r == 0x7f })
}

// noExpansion rejects values that Backrest would expand with the server's
// environment (${VAR}, $VAR) and control characters.
func noExpansion(field, v string) error {
	if strings.Contains(v, "$") {
		return fmt.Errorf("%s must not contain $", field)
	}
	if !printable(v) {
		return fmt.Errorf("%s must not contain spaces or control characters", field)
	}
	return nil
}

func cleanPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if !rePath.MatchString(p) || slices.Contains(strings.Split(p, "/"), "..") {
		return "", errors.New("path may contain letters, digits, . _ - and /")
	}
	return p, nil
}

// blockedIP reports addresses the server must not connect to for a customer.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() ||
		// carrier-grade NAT and IPv4-mapped/compatible forms of the above
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) || ip.Equal(net.IPv4bcast)
}

func (s *Storage) checkHost(ctx context.Context, host string) error {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if ip := net.ParseIP(h); ip != nil {
		if blockedIP(ip) {
			return errors.New("the storage address is not allowed (internal network)")
		}
		return nil
	}
	if !reHost.MatchString(h) || !strings.Contains(h, ".") || strings.HasSuffix(strings.ToLower(h), ".local") || strings.HasSuffix(strings.ToLower(h), ".internal") {
		return errors.New("invalid storage host name")
	}
	resolve := s.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			var out []net.IP
			for _, a := range addrs {
				out = append(out, a.IP)
			}
			return out, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ips, err := resolve(ctx, h)
	if err != nil || len(ips) == 0 {
		return errors.New("the storage host name cannot be resolved")
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return errors.New("the storage address is not allowed (internal network)")
		}
	}
	return nil
}

// httpsEndpoint parses an https URL and returns host[:port] and path.
func httpsEndpoint(raw string) (host, path string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("endpoint must be an https URL without user, query or fragment")
	}
	if p := u.Port(); p != "" {
		if !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(p) {
			return "", "", errors.New("invalid port")
		}
	}
	return u.Host, u.Path, nil
}

// buildRepo turns the customer's input into a Backrest repository. It never
// takes flags, hooks, command prefixes or environment variable names from
// the customer.
func (s *Storage) buildRepo(ctx context.Context, id string, in StorageRequest) (*v1.Repo, error) {
	for f, v := range map[string]string{"endpoint": in.Endpoint, "bucket": in.Bucket, "path": in.Path, "region": in.Region, "key ID": in.KeyID, "secret key": in.SecretKey} {
		if v != "" {
			if err := noExpansion(f, v); err != nil {
				return nil, err
			}
		}
	}
	path, err := cleanPath(in.Path)
	if err != nil {
		return nil, err
	}
	r := &v1.Repo{Id: id}
	needKey := func() error {
		if in.KeyID == "" || in.SecretKey == "" {
			return errors.New("key ID and secret key are required")
		}
		if !reKeyID.MatchString(in.KeyID) || len(in.SecretKey) > 500 {
			return errors.New("invalid key ID or secret key")
		}
		return nil
	}
	switch in.Type {
	case "s3":
		if err := needKey(); err != nil {
			return nil, err
		}
		endpoint := in.Endpoint
		if endpoint == "" {
			endpoint = "https://s3.amazonaws.com"
		}
		host, p, err := httpsEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		if strings.Trim(p, "/") != "" {
			return nil, errors.New("put the bucket and path into their own fields, not into the endpoint")
		}
		if err := s.checkHost(ctx, host); err != nil {
			return nil, err
		}
		if !reBucket.MatchString(in.Bucket) || !reRegion.MatchString(in.Region) {
			return nil, errors.New("invalid bucket or region")
		}
		r.Uri = "s3:https://" + host + "/" + strings.TrimRight(in.Bucket+"/"+path, "/")
		r.Env = []string{"AWS_ACCESS_KEY_ID=" + in.KeyID, "AWS_SECRET_ACCESS_KEY=" + in.SecretKey}
		if in.Region != "" {
			r.Env = append(r.Env, "AWS_DEFAULT_REGION="+in.Region)
		}
	case "b2":
		if err := needKey(); err != nil {
			return nil, err
		}
		if !reBucket.MatchString(in.Bucket) {
			return nil, errors.New("invalid bucket")
		}
		r.Uri = "b2:" + in.Bucket + ":" + path
		r.Env = []string{"B2_ACCOUNT_ID=" + in.KeyID, "B2_ACCOUNT_KEY=" + in.SecretKey}
	case "azure":
		if err := needKey(); err != nil {
			return nil, err
		}
		if !reBucket.MatchString(in.Bucket) || !regexp.MustCompile(`^[a-z0-9]{3,24}$`).MatchString(in.KeyID) {
			return nil, errors.New("invalid container or account name")
		}
		r.Uri = "azure:" + in.Bucket + ":/" + path
		r.Env = []string{"AZURE_ACCOUNT_NAME=" + in.KeyID, "AZURE_ACCOUNT_KEY=" + in.SecretKey}
	case "rest":
		host, p, err := httpsEndpoint(in.Endpoint)
		if err != nil {
			return nil, err
		}
		if err := s.checkHost(ctx, host); err != nil {
			return nil, err
		}
		if p, err = cleanPath(p); err != nil {
			return nil, err
		}
		u := url.URL{Scheme: "https", Host: host, Path: "/" + p}
		if in.KeyID != "" {
			if !reKeyID.MatchString(in.KeyID) || len(in.SecretKey) > 500 {
				return nil, errors.New("invalid user or password")
			}
			u.User = url.UserPassword(in.KeyID, in.SecretKey)
		}
		r.Uri = "rest:" + u.String()
		if strings.Contains(r.Uri, "$") {
			return nil, errors.New("the URL must not contain $")
		}
	default:
		return nil, errors.New("type must be s3, b2, azure or rest")
	}
	if in.Existing {
		if len(in.Password) < 8 || len(in.Password) > 500 || strings.ContainsAny(in.Password, "\r\n\x00") {
			return nil, errors.New("enter the repository password (at least 8 characters)")
		}
		r.Password = in.Password
	} else {
		r.Password = randomKey()
	}
	// Fixed maintenance: check monthly (structure only), prune monthly.
	r.CheckPolicy = &v1.CheckPolicy{Schedule: &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "0 3 2 * *"}}, Mode: &v1.CheckPolicy_StructureOnly{StructureOnly: true}}
	r.PrunePolicy = &v1.PrunePolicy{Schedule: &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "0 4 1 * *"}}, MaxUnusedPercent: 10}
	return r, nil
}

func randomKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Add connects a customer repository.
func (s *Storage) Add(ctx context.Context, t *tenant.Tenant, in StorageRequest) (*v1.Repo, error) {
	id, err := tenant.ObjectID(t.ID, in.Name)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	for _, r := range cfg.GetRepos() {
		if r.GetId() == id {
			return nil, errors.New("a storage with this name exists")
		}
	}
	r, err := s.buildRepo(ctx, id, in)
	if err != nil {
		return nil, err
	}
	saved, err := s.AddRepo(ctx, proto.Clone(r).(*v1.Repo))
	if err != nil {
		return nil, errors.New("the storage could not be opened or initialized; check the details and the password")
	}
	// A repository already known under another ID (same data) is refused,
	// because operations are matched by repository GUID.
	cfg, _ = s.Config()
	for _, o := range cfg.GetRepos() {
		if o.GetId() != id && saved.GetGuid() != "" && o.GetGuid() == saved.GetGuid() {
			_ = s.RemoveRepo(ctx, id)
			return nil, errors.New("this repository is already connected")
		}
	}
	if _, err := s.Store.AddOwnRepo(t.ID, id); err != nil {
		_ = s.RemoveRepo(ctx, id)
		return nil, err
	}
	return saved, nil
}

// Remove disconnects a customer repository and deletes the plans that use
// it. Data in the storage is not touched.
func (s *Storage) Remove(ctx context.Context, t *tenant.Tenant, repoID string) error {
	if !t.IsOwnRepo(repoID) {
		return errNotFound
	}
	var plans []string
	err := s.Transform(func(cfg *v1.Config) error {
		cfg.Plans = slices.DeleteFunc(cfg.Plans, func(p *v1.Plan) bool {
			if p.GetRepo() == repoID {
				plans = append(plans, p.GetId())
				return true
			}
			return false
		})
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range plans {
		_, _ = s.Store.UnassignPlan(t.ID, p)
	}
	if err := s.RemoveRepo(ctx, repoID); err != nil {
		return err
	}
	_, err = s.Store.UnassignRepo(t.ID, repoID)
	return err
}

// CopyPlan creates a second plan with the same paths, schedule, retention
// and hooks as one of the tenant's plans, writing to the customer's own
// repository.
func (s *Storage) CopyPlan(t *tenant.Tenant, sourcePlan, repoID, name string) (*v1.Plan, error) {
	if !t.IsOwnRepo(repoID) {
		return nil, errNotFound
	}
	id, err := tenant.ObjectID(t.ID, name)
	if err != nil {
		return nil, err
	}
	var created *v1.Plan
	err = s.Transform(func(cfg *v1.Config) error {
		sc := newScope(cfg, t)
		src := sc.plan(sourcePlan)
		if src == nil || sc.repo(repoID) == nil {
			return errNotFound
		}
		if src.GetRepo() == repoID {
			return errors.New("this plan already writes to that storage")
		}
		for _, p := range cfg.GetPlans() {
			if p.GetId() == id {
				return errors.New("a plan with this name exists")
			}
		}
		p := proto.Clone(src).(*v1.Plan)
		p.Id = id
		p.Repo = repoID
		cfg.Plans = append(cfg.Plans, p)
		created = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A copied Budget plan exports the same user and can be restored too.
	if l := t.App(sourcePlan); l != nil {
		c := *l
		c.Plan, c.Created = id, time.Now().UTC()
		if _, err := s.Store.SetApp(t.ID, c); err != nil {
			return nil, err
		}
		return created, nil
	}
	if _, err := s.Store.AssignPlan(t.ID, id); err != nil {
		return nil, err
	}
	return created, nil
}

func (h *Handler) serveStorage(w http.ResponseWriter, r *http.Request, m *member, parts []string) {
	st := h.d.Storage
	if st == nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && !h.requireAdmin(w, r, m, "portal.storage") {
		return
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 1:
		var in StorageRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if !h.allowAction(m.tenant.ID) {
			fail(w, http.StatusTooManyRequests, "too many actions in the last hour, try again later")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		repo, err := st.Add(ctx, m.tenant, in)
		if err != nil {
			h.record(r, m, "portal.storage.add", "error", in.Type+": "+err.Error())
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		h.record(r, m, "portal.storage.add", "ok", repo.GetId()+" ("+RepoType(repo.GetUri())+")")
		writeJSON(w, http.StatusOK, RepoView{ID: repo.GetId(), Type: RepoType(repo.GetUri()), Location: RedactURI(repo.GetUri()), Own: true})
	case r.Method == http.MethodDelete && len(parts) == 2:
		err := st.Remove(r.Context(), m.tenant, parts[1])
		if errors.Is(err, errNotFound) {
			fail(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			h.record(r, m, "portal.storage.remove", "error", parts[1])
			fail(w, http.StatusInternalServerError, "the storage could not be removed")
			return
		}
		h.record(r, m, "portal.storage.remove", "ok", parts[1])
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "copy-plan":
		var in struct {
			Plan string `json:"plan"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		p, err := st.CopyPlan(m.tenant, in.Plan, parts[1], in.Name)
		if errors.Is(err, errNotFound) {
			fail(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			h.record(r, m, "portal.storage.copy_plan", "error", in.Plan+" -> "+parts[1]+": "+err.Error())
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		h.record(r, m, "portal.storage.copy_plan", "ok", in.Plan+" -> "+p.GetId())
		writeJSON(w, http.StatusOK, map[string]string{"id": p.GetId()})
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}
