package portal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
	"golang.org/x/crypto/nacl/box"
)

// KeyInfo is one restic key of a repository.
type KeyInfo struct {
	ID       string `json:"id"`
	Current  bool   `json:"current"`
	UserName string `json:"userName"`
	HostName string `json:"hostName"`
}

// KeyOps runs restic key commands against a configured repository.
type KeyOps interface {
	List(ctx context.Context, repoID string) ([]KeyInfo, error)
	Add(ctx context.Context, repoID, password, user string) error
	Remove(ctx context.Context, repoID, keyID string) error
}

// Kits creates recovery kits: a separate restic key for the customer,
// shown once, optionally sealed for the operator (escrow).
type Kits struct {
	Store  *tenant.Store
	Config func() (*v1.Config, error)
	Keys   KeyOps
	// EscrowPublicKey is the operator's X25519 public key; nil when escrow
	// is not set up on this server.
	EscrowPublicKey *[32]byte
	EscrowDir       string
	Now             func() time.Time
}

// ErrEscrowNotConfigured is returned when a tenant booked escrow but the
// server has no escrow public key.
var ErrEscrowNotConfigured = errors.New("key escrow is booked for this customer but not set up on the server; ask the operator")

// ParseEscrowPublicKey reads RELYPER_ESCROW_PUBLIC_KEY (base64, 32 bytes).
func ParseEscrowPublicKey(s string) (*[32]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("RELYPER_ESCROW_PUBLIC_KEY must be a base64 X25519 public key (create one with relyper-escrow keygen)")
	}
	var k [32]byte
	copy(k[:], b)
	return &k, nil
}

// EscrowRecord is what a sealed copy contains.
type EscrowRecord struct {
	Tenant    string    `json:"tenant"`
	Repo      string    `json:"repo"`
	URI       string    `json:"uri"`
	KeyID     string    `json:"keyId"`
	Password  string    `json:"password"`
	CreatedAt time.Time `json:"createdAt"`
}

// Seal encrypts a record to the public key (NaCl sealed box). Only the
// holder of the private key can open it.
func Seal(pub *[32]byte, rec EscrowRecord) ([]byte, error) {
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	sealed, err := box.SealAnonymous(nil, b, pub, rand.Reader)
	if err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(sealed) + "\n"), nil
}

// Open decrypts a sealed copy with the operator's key pair.
func Open(pub, priv *[32]byte, data []byte) (*EscrowRecord, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, errors.New("not a sealed escrow file")
	}
	plain, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok {
		return nil, errors.New("the file cannot be opened with this key")
	}
	var rec EscrowRecord
	if err := json.Unmarshal(plain, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

var keyIDPattern = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// Create adds a new customer key to the repository, removes the previous
// customer key, records the kit and returns the kit text. The key is not
// stored anywhere except, with escrow, in the sealed copy.
func (k *Kits) Create(ctx context.Context, t *tenant.Tenant, repoID, by string) (string, error) {
	if !t.HasRepo(repoID) {
		return "", errNotFound
	}
	cfg, err := k.Config()
	if err != nil {
		return "", err
	}
	repo := newScope(cfg, t).repo(repoID)
	if repo == nil {
		return "", errNotFound
	}
	if t.Escrow && k.EscrowPublicKey == nil {
		return "", ErrEscrowNotConfigured
	}
	before, err := k.Keys.List(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("list keys: %w", err)
	}
	password := randomKey()
	user := "customer-" + t.ID
	if err := k.Keys.Add(ctx, repoID, password, user); err != nil {
		return "", fmt.Errorf("add key: %w", err)
	}
	after, err := k.Keys.List(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("list keys: %w", err)
	}
	known := map[string]bool{}
	var serverKey string
	for _, ki := range before {
		known[ki.ID] = true
		if ki.Current {
			serverKey = ki.ID
		}
	}
	var newID string
	for _, ki := range after {
		if !known[ki.ID] {
			if newID != "" {
				return "", errors.New("could not identify the new key; another key was added at the same time")
			}
			newID = ki.ID
		}
	}
	if !keyIDPattern.MatchString(newID) {
		return "", errors.New("could not identify the new key")
	}
	now := time.Now
	if k.Now != nil {
		now = k.Now
	}
	kit := tenant.Kit{Repo: repoID, KeyID: newID, CreatedAt: now().UTC(), CreatedBy: by}
	if t.Escrow {
		rel := filepath.Join(t.ID, repoID+"-"+newID+".sealed")
		sealed, err := Seal(k.EscrowPublicKey, EscrowRecord{Tenant: t.ID, Repo: repoID, URI: repo.GetUri(), KeyID: newID, Password: password, CreatedAt: kit.CreatedAt})
		if err == nil {
			path := filepath.Join(k.EscrowDir, rel)
			if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
				err = os.WriteFile(path, sealed, 0o600)
			}
		}
		if err != nil {
			_ = k.Keys.Remove(ctx, repoID, newID)
			return "", fmt.Errorf("escrow: %w", err)
		}
		kit.Escrowed, kit.EscrowFile = true, rel
	}
	old := t.Kit(repoID)
	if _, err := k.Store.SetKit(t.ID, kit); err != nil {
		_ = k.Keys.Remove(ctx, repoID, newID)
		return "", err
	}
	// Only the latest kit works: remove the previous customer key, but
	// never the key the server itself uses.
	var warn string
	if old != nil && old.KeyID != "" && old.KeyID != serverKey && old.KeyID != newID {
		if err := k.Keys.Remove(ctx, repoID, old.KeyID); err != nil {
			warn = "The previous customer key " + old.KeyID + " could not be removed; ask the operator to remove it."
		}
		if old.EscrowFile != "" {
			_ = os.Remove(filepath.Join(k.EscrowDir, old.EscrowFile))
		}
	}
	return kitText(t, repo, kit, password, serverKey, warn, now()), nil
}

func kitText(t *tenant.Tenant, repo *v1.Repo, kit tenant.Kit, password, serverKey, warn string, now time.Time) string {
	own := t.IsOwnRepo(repo.GetId())
	uri := RedactURI(repo.GetUri())
	if own {
		uri = repo.GetUri() // the customer entered it, including any credentials
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }
	var b strings.Builder
	b.WriteString("RELYPER BACKUP - RECOVERY KIT\n=============================\n\n")
	b.WriteString("Keep this file offline (printed, password manager or encrypted USB stick).\n")
	b.WriteString("Anyone with this file and access to the storage can read your backups.\n\n")
	fmt.Fprintf(&b, "Customer:      %s (%s)\n", t.Name, t.ID)
	fmt.Fprintf(&b, "Repository:    %s\n", repo.GetId())
	fmt.Fprintf(&b, "Created:       %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Location:      %s\n", uri)
	fmt.Fprintf(&b, "Customer key:  %s\n", password)
	fmt.Fprintf(&b, "Key ID:        %s\n", kit.KeyID)
	if serverKey != "" {
		fmt.Fprintf(&b, "Server key ID: %s (the key Relyper Backup uses for scheduled backups)\n", serverKey)
	}
	if kit.Escrowed {
		b.WriteString("Escrow:        a sealed copy of this key is held by your provider (key escrow).\n")
	} else {
		b.WriteString("Escrow:        none. Nobody else has this key. If you lose this file, your\n               provider cannot give it back to you.\n")
	}
	b.WriteString("\nThis kit replaces every earlier kit of this repository; earlier customer\nkeys no longer work.\n")
	if warn != "" {
		b.WriteString("\nNOTE: " + warn + "\n")
	}
	b.WriteString("\nSTORAGE ACCESS\n--------------\n")
	if own {
		b.WriteString("This is your own storage. Set these variables as entered when it was connected:\n")
		for _, e := range repo.GetEnv() {
			if i := strings.Index(e, "="); i > 0 {
				fmt.Fprintf(&b, "  export %s=%s\n", e[:i], q(e[i+1:]))
			}
		}
		if len(repo.GetEnv()) == 0 {
			b.WriteString("  (none needed beyond the location)\n")
		}
	} else {
		b.WriteString("This repository is on your provider's storage. To restore without Relyper\nBackup, ask your provider for read access credentials for this location.\n")
	}
	b.WriteString("\nRESTORE WITH THE RESTIC COMMAND LINE (https://restic.net)\n---------------------------------------------------------\n")
	fmt.Fprintf(&b, "  export RESTIC_REPOSITORY=%s\n", q(uri))
	fmt.Fprintf(&b, "  export RESTIC_PASSWORD=%s\n", q(password))
	b.WriteString("  restic snapshots\n  restic restore latest --target ./restore\n  restic restore <snapshot-id> --target ./restore --include /path/inside/snapshot\n")
	if own && serverKey != "" {
		b.WriteString("\nEND THE SERVICE ON YOUR OWN STORAGE\n-----------------------------------\n")
		b.WriteString("To stop Relyper Backup from reading or writing this repository, remove the\nserver key (after that, scheduled backups to it fail):\n")
		fmt.Fprintf(&b, "  restic key remove %s\n", serverKey)
	}
	return b.String()
}

func (h *Handler) serveKits(w http.ResponseWriter, r *http.Request, m *member, parts []string) {
	k := h.d.Kits
	if k == nil || r.Method != http.MethodPost || len(parts) != 2 {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if !h.requireAdmin(w, r, m, "portal.kit.create") {
		return
	}
	if !h.allowAction(m.tenant.ID) {
		fail(w, http.StatusTooManyRequests, "too many actions in the last hour, try again later")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	text, err := k.Create(ctx, m.tenant, parts[1], m.session.UserName())
	switch {
	case errors.Is(err, errNotFound):
		fail(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, ErrEscrowNotConfigured):
		h.record(r, m, "portal.kit.create", "error", parts[1]+": escrow not configured")
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		h.record(r, m, "portal.kit.create", "error", parts[1])
		fail(w, http.StatusInternalServerError, "the recovery kit could not be created")
		return
	}
	h.record(r, m, "portal.kit.create", "ok", parts[1])
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="relyper-recovery-kit-`+parts[1]+`.txt"`)
	_, _ = w.Write([]byte(text))
}
