// Relyper-owned test: recovery kit keys against a real restic repository.
// Runs only when RELYPER_TEST_RESTIC points to a restic binary.
package portal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/orchestrator/repo"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
)

func TestKitWithRealRestic(t *testing.T) {
	bin := os.Getenv("RELYPER_TEST_RESTIC")
	if bin == "" {
		t.Skip("set RELYPER_TEST_RESTIC to a restic binary")
	}
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	cfg := &v1.Config{Instance: "host1", Repos: []*v1.Repo{{Id: "acme-repo", Uri: repoDir, Password: "server-password-123"}}}
	ro, err := repo.NewRepoOrchestrator(cfg, cfg.Repos[0], bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, _ := tenant.Open(filepath.Join(dir, "tenants.json"))
	store.Create("acme", "ACME", nil)
	store.AssignRepo("acme", "acme-repo")
	keys := &resticKeys{get: func(string) (*repo.RepoOrchestrator, error) { return ro, nil }}
	k := &Kits{Store: store, Config: func() (*v1.Config, error) { return cfg, nil }, Keys: keys, EscrowDir: dir}

	acme, _ := store.Get("acme")
	kit1, err := k.Create(context.Background(), acme, "acme-repo", "test")
	if err != nil {
		t.Fatal(err)
	}
	pw1 := kitLine(kit1, "Customer key:")
	// The customer key opens the repository with the plain restic CLI.
	plain := func(pw string) error {
		cmd := exec.Command(bin, "-r", repoDir, "snapshots", "--no-lock")
		cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+pw)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return &exec.ExitError{Stderr: out}
		}
		return nil
	}
	if err := plain(pw1); err != nil {
		t.Fatalf("customer key does not open the repository: %v", err)
	}
	// A second kit revokes the first customer key, the server key stays.
	acme, _ = store.Get("acme")
	kit2, err := k.Create(context.Background(), acme, "acme-repo", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := plain(pw1); err == nil {
		t.Fatal("first customer key still works")
	}
	if err := plain(kitLine(kit2, "Customer key:")); err != nil {
		t.Fatal("second customer key does not work")
	}
	if err := plain("server-password-123"); err != nil {
		t.Fatal("server key was removed")
	}
	list, err := keys.List(context.Background(), "acme-repo")
	if err != nil || len(list) != 2 {
		t.Fatalf("keys: %v %+v", err, list)
	}
	if !strings.Contains(kit2, "Server key ID:") {
		t.Fatal("kit lacks the server key ID")
	}
}
