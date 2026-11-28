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
	want := []string{code, filepath.Join(ep, "traefik"), "/v/uploads", s.InventoryDir()}
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
	for _, want := range []string{filepath.Join(ep, "projects/hosting/budget"), filepath.Join(ep, "traefik"), filepath.Join(ep, "data"), "/v/data"} {
		if !slices.Contains(p.Paths, want) {
			t.Errorf("missing %s in %v", want, p.Paths)
		}
	}
	// Without /etc/easypanel the preview says what is missing.
	s.inv.Docker.EasypanelConfig[0].BackupPath = ""
	p, err = s.ProposeAppPlan(context.Background(), app.ID)
	if err != nil || len(p.Skipped) == 0 || !strings.Contains(p.Skipped[0], "environment variables, domains") {
		t.Fatalf("skipped %v %v", p.Skipped, err)
	}
}
