// Relyper-owned tests: customer-owned storage, recovery kits and escrow.
package portal

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"
)

func fakeResolve(_ context.Context, host string) ([]net.IP, error) {
	switch host {
	case "s3.example.com", "rest.example.com":
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	case "rebind.example.com":
		return []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("10.0.0.5")}, nil
	case "meta.example.com":
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}
	return nil, errors.New("nxdomain")
}

func (f *fixture) withStorage(t *testing.T) *Storage {
	st := &Storage{Store: f.store, Config: func() (*v1.Config, error) { return f.cfg, nil }, Resolve: fakeResolve,
		Transform: func(fn func(cfg *v1.Config) error) error {
			c := proto.Clone(f.cfg).(*v1.Config)
			if err := fn(c); err != nil {
				return err
			}
			f.cfg = c
			return nil
		},
		AddRepo: func(_ context.Context, r *v1.Repo) (*v1.Repo, error) {
			r.Guid = "guid-" + r.Id
			if strings.Contains(r.Uri, "dup") {
				r.Guid = "g-beta"
			}
			f.cfg.Repos = append(f.cfg.Repos, r)
			return r, nil
		},
		RemoveRepo: func(_ context.Context, id string) error {
			f.cfg.Repos = slices.DeleteFunc(f.cfg.Repos, func(r *v1.Repo) bool { return r.Id == id })
			return nil
		}}
	f.h.d.Storage = st
	return st
}

func TestStorageValidation(t *testing.T) {
	f := newFixture(t)
	st := f.withStorage(t)
	acme, _ := f.store.Get("acme")
	bad := []StorageRequest{
		{Name: "own", Type: "s3", Endpoint: "https://s3.example.com", Bucket: "b", KeyID: "k", SecretKey: "s"},                   // bucket too short
		{Name: "own", Type: "s3", Endpoint: "http://s3.example.com", Bucket: "bucket", KeyID: "k", SecretKey: "s"},               // no https
		{Name: "own", Type: "s3", Endpoint: "https://127.0.0.1:9000", Bucket: "bucket", KeyID: "k", SecretKey: "s"},              // loopback
		{Name: "own", Type: "s3", Endpoint: "https://meta.example.com", Bucket: "bucket", KeyID: "k", SecretKey: "s"},            // metadata
		{Name: "own", Type: "s3", Endpoint: "https://rebind.example.com", Bucket: "bucket", KeyID: "k", SecretKey: "s"},          // private address
		{Name: "own", Type: "s3", Endpoint: "https://s3.example.com", Bucket: "bucket", KeyID: "k", SecretKey: "${RELYPER_OIDC_CLIENT_SECRET}"}, // env expansion
		{Name: "own", Type: "s3", Endpoint: "https://s3.example.com", Bucket: "bucket", Path: "../x", KeyID: "k", SecretKey: "s"},
		{Name: "own", Type: "s3", Endpoint: "https://user:pw@s3.example.com", Bucket: "bucket", KeyID: "k", SecretKey: "s"},
		{Name: "own", Type: "rest", Endpoint: "https://rest.example.com/$HOME"},
		{Name: "own", Type: "rest", Endpoint: "https://intranet/repo"},
		{Name: "own", Type: "local", Path: "/etc"},
		{Name: "own", Type: "sftp", Endpoint: "host"},
		{Name: "Bad Name", Type: "b2", Bucket: "bucket", KeyID: "k", SecretKey: "s"},
		{Name: "own", Type: "b2", Bucket: "bucket", KeyID: "k", SecretKey: "s", Existing: true, Password: "short"},
	}
	for i, in := range bad {
		if _, err := st.Add(context.Background(), acme, in); err == nil {
			t.Errorf("case %d accepted: %+v", i, in)
		}
	}
	if len(f.cfg.Repos) != 3 {
		t.Fatalf("repositories added by refused requests: %d", len(f.cfg.Repos))
	}
	r, err := st.Add(context.Background(), acme, StorageRequest{Name: "own-s3", Type: "s3", Endpoint: "https://s3.example.com", Bucket: "acme-backup", Path: "relyper", Region: "eu-central-1", KeyID: "AKIA123", SecretKey: "s3cr3t/key"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Id != "t-acme-own-s3" || r.Uri != "s3:https://s3.example.com/acme-backup/relyper" || len(r.Password) < 40 || len(r.Flags) != 0 || len(r.Hooks) != 0 || r.CommandPrefix != nil {
		t.Fatalf("repo: %+v", r)
	}
	acme, _ = f.store.Get("acme")
	if !acme.IsOwnRepo(r.Id) {
		t.Fatal("not recorded as own storage")
	}
	// A repository that is already configured under another ID is refused.
	if _, err := st.Add(context.Background(), acme, StorageRequest{Name: "dup", Type: "b2", Bucket: "dup-bucket", KeyID: "k", SecretKey: "s"}); err == nil {
		t.Fatal("duplicate GUID accepted")
	}
	for _, x := range f.cfg.Repos {
		if x.Id == "t-acme-dup" {
			t.Fatal("duplicate not rolled back")
		}
	}
	r, err = st.Add(context.Background(), acme, StorageRequest{Name: "rest", Type: "rest", Endpoint: "https://rest.example.com:8000/acme", KeyID: "acme", SecretKey: "p@ss:word"})
	if err != nil || r.Uri != "rest:https://acme:p%40ss%3Aword@rest.example.com:8000/acme" {
		t.Fatalf("rest: %v %+v", err, r)
	}
}

func TestStorageCopyAndRemove(t *testing.T) {
	f := newFixture(t)
	st := f.withStorage(t)
	acme, _ := f.store.Get("acme")
	r, err := st.Add(context.Background(), acme, StorageRequest{Name: "mine", Type: "b2", Bucket: "acme-b2", KeyID: "k1", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	acme, _ = f.store.Get("acme")
	if _, err := st.CopyPlan(acme, "beta-db", r.Id, "copy"); !errors.Is(err, errNotFound) {
		t.Fatalf("copied a foreign plan: %v", err)
	}
	if _, err := st.CopyPlan(acme, "acme-web", "beta-repo", "copy"); !errors.Is(err, errNotFound) {
		t.Fatalf("copied to a foreign repository: %v", err)
	}
	if _, err := st.CopyPlan(acme, "acme-web", "acme-repo", "copy"); !errors.Is(err, errNotFound) {
		t.Fatalf("copied to operator storage: %v", err)
	}
	p, err := st.CopyPlan(acme, "acme-web", r.Id, "web-own")
	if err != nil || p.Id != "t-acme-web-own" || p.Repo != r.Id || p.Paths[0] != "/srv/acme" {
		t.Fatalf("copy: %v %+v", err, p)
	}
	// The copy shows up in the portal.
	_, ov, raw := f.call(t, "acme:backup_customer", http.MethodGet, "overview", nil)
	if len(ov["plans"].([]any)) != 2 || !strings.Contains(raw, `"ownStorage":true`) || strings.Contains(raw, "secret") {
		t.Fatalf("overview: %s", raw)
	}
	// Viewers cannot remove; another tenant gets 404.
	if code, _, _ := f.call(t, "acme:backup_customer", http.MethodDelete, "storage/"+r.Id, map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("viewer removed storage: %d", code)
	}
	if code, _, _ := f.call(t, "beta:backup_customer_admin", http.MethodDelete, "storage/"+r.Id, map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("other tenant: %d", code)
	}
	// Operator storage cannot be removed from the portal.
	if code, _, _ := f.call(t, "acme:backup_customer_admin", http.MethodDelete, "storage/acme-repo", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("operator storage removed: %d", code)
	}
	if code, _, raw := f.call(t, "acme:backup_customer_admin", http.MethodDelete, "storage/"+r.Id, map[string]any{}); code != http.StatusOK {
		t.Fatalf("remove: %d %s", code, raw)
	}
	for _, x := range f.cfg.Plans {
		if x.Id == "t-acme-web-own" {
			t.Fatal("plan of removed storage kept")
		}
	}
	acme, _ = f.store.Get("acme")
	if acme.HasRepo(r.Id) || acme.HasPlan("t-acme-web-own") {
		t.Fatal("assignments kept")
	}
}

type fakeKeys struct {
	keys    map[string][]KeyInfo
	removed []string
	n       int
}

func (k *fakeKeys) List(_ context.Context, repo string) ([]KeyInfo, error) {
	if k.keys[repo] == nil {
		k.keys[repo] = []KeyInfo{{ID: "aaaaaaaa", Current: true}}
	}
	return slices.Clone(k.keys[repo]), nil
}
func (k *fakeKeys) Add(_ context.Context, repo, pw, user string) error {
	k.n++
	k.keys[repo] = append(k.keys[repo], KeyInfo{ID: strings.Repeat(string(rune('a'+k.n)), 8), UserName: user})
	return nil
}
func (k *fakeKeys) Remove(_ context.Context, repo, id string) error {
	k.removed = append(k.removed, id)
	k.keys[repo] = slices.DeleteFunc(k.keys[repo], func(x KeyInfo) bool { return x.ID == id })
	return nil
}

func TestRecoveryKitAndEscrow(t *testing.T) {
	f := newFixture(t)
	keys := &fakeKeys{keys: map[string][]KeyInfo{}}
	pub, priv, _ := box.GenerateKey(rand.Reader)
	dir := t.TempDir()
	f.h.d.Kits = &Kits{Store: f.store, Config: func() (*v1.Config, error) { return f.cfg, nil }, Keys: keys, EscrowDir: dir}

	// Without escrow: kit is returned once and not stored.
	req := func(tok, repo string) (int, string) {
		code, _, raw := f.call(t, tok, http.MethodPost, "kits/"+repo, map[string]any{})
		return code, raw
	}
	if code, _ := req("acme:backup_customer", "acme-repo"); code != http.StatusForbidden {
		t.Fatalf("viewer created a kit: %d", code)
	}
	if code, _ := req("acme:backup_customer_admin", "beta-repo"); code != http.StatusNotFound {
		t.Fatalf("kit for a foreign repo: %d", code)
	}
	code, kit := req("acme:backup_customer_admin", "acme-repo")
	if code != http.StatusOK || !strings.Contains(kit, "Customer key:") || !strings.Contains(kit, "Escrow:        none") {
		t.Fatalf("kit: %d %s", code, kit)
	}
	if strings.Contains(kit, secretPW) {
		t.Fatal("operator storage credentials in the kit")
	}
	acme, _ := f.store.Get("acme")
	k := acme.Kit("acme-repo")
	if k == nil || k.KeyID != "bbbbbbbb" || k.Escrowed {
		t.Fatalf("kit record: %+v", k)
	}
	store, _ := os.ReadFile(f.store.Path())
	line := kitLine(kit, "Customer key:")
	if strings.Contains(string(store), line) {
		t.Fatal("customer key stored in tenants.json")
	}

	// Escrow booked but not configured: refused.
	yes := true
	f.store.Configure("acme", tenant.Settings{Escrow: &yes})
	if code, raw := req("acme:backup_customer_admin", "acme-repo"); code != http.StatusServiceUnavailable {
		t.Fatalf("escrow without key: %d %s", code, raw)
	}
	// With the public key: sealed copy, previous customer key removed.
	f.h.d.Kits.EscrowPublicKey = pub
	code, kit2 := req("acme:backup_customer_admin", "acme-repo")
	if code != http.StatusOK || !strings.Contains(kit2, "sealed copy") {
		t.Fatalf("escrow kit: %d %s", code, kit2)
	}
	if !slices.Contains(keys.removed, "bbbbbbbb") || slices.Contains(keys.removed, "aaaaaaaa") {
		t.Fatalf("removed keys %v (must remove the old customer key, never the server key)", keys.removed)
	}
	acme, _ = f.store.Get("acme")
	k = acme.Kit("acme-repo")
	sealed, err := os.ReadFile(filepath.Join(dir, k.EscrowFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), kitLine(kit2, "Customer key:")) {
		t.Fatal("sealed file contains the key in clear text")
	}
	rec, err := Open(pub, priv, sealed)
	if err != nil || rec.Password != kitLine(kit2, "Customer key:") || rec.Tenant != "acme" || rec.KeyID != k.KeyID {
		t.Fatalf("open: %v %+v", err, rec)
	}
	otherPub, otherPriv, _ := box.GenerateKey(rand.Reader)
	if _, err := Open(otherPub, otherPriv, sealed); err == nil {
		t.Fatal("opened with another key")
	}
}

func kitLine(kit, label string) string {
	for _, l := range strings.Split(kit, "\n") {
		if strings.HasPrefix(l, label) {
			return strings.TrimSpace(strings.TrimPrefix(l, label))
		}
	}
	return "<missing>"
}
