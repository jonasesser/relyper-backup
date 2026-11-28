package infra

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/oplog/sqlitestore"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

// The plan of Easypanel itself protects Easypanel and the configuration
// part of every Easypanel app; their data stays unprotected.
func TestEasypanelPlanCoverage(t *testing.T) {
	root := t.TempDir()
	ep := filepath.Join(root, "easypanel")
	for _, d := range []string{"data", "traefik", "backups", "projects/web/site/code/.git"} {
		os.MkdirAll(filepath.Join(ep, d), 0o700)
	}
	os.WriteFile(filepath.Join(ep, "projects/web/site/code/.env"), []byte("A=1"), 0o600)
	st, err := sqlitestore.NewMemorySqliteStore(t)
	if err != nil {
		t.Fatal(err)
	}
	ol, err := oplog.NewOpLog(st)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{s: Settings{DumpDir: filepath.Join(root, "dumps"), CoverageMaxAge: 48 * time.Hour}}
	paths := []string{filepath.Join(ep, "data"), filepath.Join(ep, "traefik"), filepath.Join(ep, "projects/web/site/code/.env"), s.InventoryDir()}
	cfg := &v1.Config{Instance: "i", Repos: []*v1.Repo{{Id: "r", Guid: "g"}},
		Plans: []*v1.Plan{{Id: "app-system-easypanel", Repo: "r", Paths: paths}}}
	now := time.Now().UnixMilli()
	id := strings.Repeat("a", 64)
	if err := ol.Add(&v1.Operation{InstanceId: "i", RepoId: "r", RepoGuid: "g", PlanId: "app-system-easypanel", SnapshotId: id, UnixTimeStartMs: now, Status: v1.OperationStatus_STATUS_SUCCESS,
		Op: &v1.Operation_OperationIndexSnapshot{OperationIndexSnapshot: &v1.OperationIndexSnapshot{Snapshot: &v1.ResticSnapshot{Id: id, UnixTimeMs: now, Paths: paths}}}}); err != nil {
		t.Fatal(err)
	}
	s.d = Deps{Config: &config.MemoryStore{Config: cfg}, OpLog: ol}
	s.inv = &discovery.Inventory{Docker: discovery.Host{Available: true, Easypanel: true,
		EasypanelConfig: []discovery.DeploymentFile{{HostPath: "/etc/easypanel", BackupPath: ep}}},
		Apps: []discovery.App{
			{ID: discovery.EasypanelSystemID, Name: "Easypanel", Kind: discovery.KindSystem, Services: []discovery.Service{{Name: "easypanel", State: "running"}}},
			{ID: "easypanel:web_site", Name: "site", Kind: discovery.KindEasypanel, Project: "web", Services: []discovery.Service{{Name: "site", State: "running", Image: "x:1",
				Data: []discovery.Data{{Type: "volume", Name: "d", HostPath: "/v/d", BackupPath: "/v/d", Class: discovery.ClassPersistent}}}}},
		}}
	s.invAt = time.Now()
	rep, err := s.Coverage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]map[string]string{}
	prot := map[string]string{}
	for _, a := range rep.Apps {
		states[a.App] = map[string]string{}
		for _, it := range a.Items {
			states[a.App][it.Kind] = it.State
		}
		prot[a.App] = a.Protection
	}
	if states[discovery.EasypanelSystemID]["easypanel_settings"] != StateProtected {
		t.Errorf("Easypanel settings (its own backups/ are not needed): %v", states[discovery.EasypanelSystemID])
	}
	site := states["easypanel:web_site"]
	for _, k := range []string{"easypanel_settings", "service_folder", "app_definition"} {
		if site[k] != StateProtected {
			t.Errorf("site %s: %s", k, site[k])
		}
	}
	if site["data"] != StateUnprotected || prot["easypanel:web_site"] != ProtPartial {
		t.Errorf("site data %s, protection %s", site["data"], prot["easypanel:web_site"])
	}
}
