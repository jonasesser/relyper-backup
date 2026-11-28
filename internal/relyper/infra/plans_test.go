package infra

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

func TestProposeAppPlanItems(t *testing.T) {
	root := t.TempDir()
	ep := filepath.Join(root, "easypanel")
	for _, d := range []string{"projects/web/app/code", "traefik/config"} {
		if err := os.MkdirAll(filepath.Join(ep, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	code := filepath.Join(ep, "projects/web/app/code")
	s := &Service{s: Settings{DumpDir: filepath.Join(root, "dumps"), DumpCommand: "relyper-dbdump"}}
	s.inv = &discovery.Inventory{Docker: discovery.Host{Available: true, Easypanel: true,
		EasypanelConfig: []discovery.DeploymentFile{{HostPath: "/etc/easypanel", BackupPath: ep}}},
		Apps: []discovery.App{{ID: "compose:web_app", Name: "web_app", Kind: discovery.KindCompose,
			ConfigDirs: []discovery.DeploymentFile{{HostPath: "/etc/easypanel/projects/web/app/code", BackupPath: code}},
			Deployment: []discovery.DeploymentFile{{HostPath: "/etc/easypanel/projects/web/app/code/docker-compose.yml", BackupPath: code + "/docker-compose.yml"}},
			Services: []discovery.Service{{Name: "app", State: "running", Data: []discovery.Data{
				{Type: "bind", HostPath: "/var/run/docker.sock"}, // sockets never reach the plan (discovery drops them)
				{Type: "volume", Name: "uploads", HostPath: "/v/uploads", ContainerPath: "/uploads", BackupPath: "/v/uploads", Class: discovery.ClassPersistent},
				{Type: "bind", HostPath: code + "/cache", ContainerPath: "/cache", BackupPath: code + "/cache", Class: discovery.ClassCache},
			}}}}}}
	s.invAt = time.Now()

	p, err := s.ProposeAppPlan(context.Background(), "compose:web_app")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{code, "/v/uploads", s.InventoryDir()}
	if !slices.Equal(p.Paths, want) {
		t.Fatalf("paths %v, want %v (items %+v)", p.Paths, want, p.Items)
	}
	// The cache inside the compose directory is offered but off, so it is
	// excluded from the directory.
	if !slices.Equal(p.Excludes, []string{code + "/cache"}) {
		t.Fatalf("excludes %v", p.Excludes)
	}
	for _, it := range p.Items {
		if it.Path == filepath.Join(ep, "projects") {
			t.Fatalf("projects directory must not be in the Easypanel settings: %+v", it)
		}
	}
	// Choose: drop uploads, add the cache; the inventory stays (required).
	if err := p.choose([]string{code, code + "/cache"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Paths, []string{code, code + "/cache", s.InventoryDir()}) || len(p.Excludes) != 0 {
		t.Fatalf("chosen: %v excludes %v", p.Paths, p.Excludes)
	}
	if err := p.choose([]string{"/etc/shadow"}); err == nil {
		t.Fatal("a path outside the proposal must be refused")
	}
}

func TestProposeEasypanelServicePlan(t *testing.T) {
	root := t.TempDir()
	ep := filepath.Join(root, "easypanel")
	for _, d := range []string{"projects/hosting/budget/code", "traefik", "data"} {
		if err := os.MkdirAll(filepath.Join(ep, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{s: Settings{DumpDir: filepath.Join(root, "dumps"), DumpCommand: "relyper-dbdump"}}
	app := discovery.App{ID: "easypanel:hosting_budget", Name: "budget", Kind: discovery.KindEasypanel, Project: "hosting",
		Services: []discovery.Service{{Name: "budget", State: "running", Data: []discovery.Data{
			{Type: "volume", Name: "data", HostPath: "/v/data", ContainerPath: "/data", BackupPath: "/v/data", Class: discovery.ClassPersistent}}}}}
	s.inv = &discovery.Inventory{Docker: discovery.Host{Available: true, Easypanel: true,
		EasypanelConfig: []discovery.DeploymentFile{{HostPath: "/etc/easypanel", BackupPath: ep}}}, Apps: []discovery.App{app}}
	s.invAt = time.Now()
	p, err := s.ProposeAppPlan(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The app plan has its own service folder and data; the shared
	// Easypanel settings are only in the plan of Easypanel itself.
	want := []string{filepath.Join(ep, "projects/hosting/budget"), "/v/data", s.InventoryDir()}
	if !slices.Equal(p.Paths, want) {
		t.Errorf("paths %v, want %v", p.Paths, want)
	}
	if len(p.Notes) == 0 || !strings.Contains(p.Notes[0], "app-system-easypanel") {
		t.Errorf("notes %v", p.Notes)
	}
	// Without /etc/easypanel the preview says what is missing.
	s.inv.Docker.EasypanelConfig[0].BackupPath = ""
	p, err = s.ProposeAppPlan(context.Background(), app.ID)
	if err != nil || len(p.Skipped) == 0 || !strings.Contains(p.Skipped[0], "environment variables, domains") {
		t.Fatalf("skipped %v %v", p.Skipped, err)
	}
}

func TestProposeEasypanelSystemPlan(t *testing.T) {
	root := t.TempDir()
	ep := filepath.Join(root, "easypanel")
	for _, d := range []string{"projects/hosting/budget/code/.git", "projects/hosting/budget/code/src", "projects/hosting/budget/volumes/data",
		"projects/web/db/data", "projects/web/site/code/node_modules/x", "traefik", "data", "backups", "actions"} {
		if err := os.MkdirAll(filepath.Join(ep, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"projects/hosting/budget/code/.env", "projects/hosting/budget/code/docker-compose.yml", "projects/hosting/budget/code/Dockerfile",
		"projects/hosting/budget/code/main.go", "projects/web/site/code/index.php", "projects/web/site/code/node_modules/x/big.js"} {
		if err := os.WriteFile(filepath.Join(ep, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{s: Settings{DumpDir: filepath.Join(root, "dumps"), DumpCommand: "relyper-dbdump"}}
	s.inv = &discovery.Inventory{Docker: discovery.Host{Available: true, Easypanel: true,
		EasypanelConfig: []discovery.DeploymentFile{{HostPath: "/etc/easypanel", BackupPath: ep}}},
		Apps: []discovery.App{{ID: discovery.EasypanelSystemID, Name: "Easypanel", Kind: discovery.KindSystem,
			Services: []discovery.Service{{Name: "easypanel", State: "running"}}}}}
	s.invAt = time.Now()
	p, err := s.ProposeAppPlan(context.Background(), discovery.EasypanelSystemID)
	if err != nil {
		t.Fatal(err)
	}
	code := filepath.Join(ep, "projects/hosting/budget/code")
	// Code from Git: only the deployment files. Other code: code/ without
	// dependencies. volumes/ never.
	want := []string{filepath.Join(ep, "data"), filepath.Join(ep, "traefik"),
		filepath.Join(code, ".env"), filepath.Join(code, "Dockerfile"), filepath.Join(code, "docker-compose.yml"),
		filepath.Join(ep, "projects/web/site/code"), s.InventoryDir()}
	if !slices.Equal(p.Paths, want) {
		t.Fatalf("paths %v, want %v", p.Paths, want)
	}
	// Easypanel's own backups and action logs are offered but not chosen;
	// data/ of a service is mounted app data and not offered.
	for _, it := range p.Items {
		if (filepath.Base(it.Path) == "backups" || filepath.Base(it.Path) == "actions") && it.Selected {
			t.Errorf("%s must not be chosen", it.Path)
		}
		if strings.HasPrefix(it.Path, filepath.Join(ep, "projects/web/db")) {
			t.Errorf("service data offered: %s", it.Path)
		}
	}
	if !slices.Contains(p.Excludes, "node_modules") || !slices.Contains(p.Excludes, ".git") {
		t.Fatalf("excludes %v", p.Excludes)
	}
	// The size leaves out the excluded dependencies.
	if est := measure(context.Background(), []string{filepath.Join(ep, "projects/web/site/code")}, p.Excludes, time.Second); est.Files != 1 {
		t.Fatalf("measured %+v", est)
	}

	// The Easypanel-only scope selects just this app.
	apps, err := s.selectApps(s.inv, SetupRequest{Scope: ScopeEasypanelConfig})
	if err != nil || len(apps) != 1 || apps[0].ID != discovery.EasypanelSystemID {
		t.Fatalf("scope: %v %v", apps, err)
	}
}
