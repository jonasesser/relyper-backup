// Relyper-owned unit tests for coverage helpers.
package infra

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

func TestHookApp(t *testing.T) {
	p := &v1.Plan{Hooks: []*v1.Hook{{
		Conditions: []v1.Hook_Condition{v1.Hook_CONDITION_SNAPSHOT_START},
		Action:     &v1.Hook_ActionCommand{ActionCommand: &v1.Hook_Command{Command: "relyper-dbdump dump --dir '/data/relyper/dumps' 'compose:shop'"}},
	}}}
	if app, dir := HookApp(p); app != "compose:shop" || dir != "/data/relyper/dumps" {
		t.Fatalf("%q %q", app, dir)
	}
	p.Hooks[0].Conditions = []v1.Hook_Condition{v1.Hook_CONDITION_SNAPSHOT_END}
	if app, _ := HookApp(p); app != "" {
		t.Fatal("a hook after the backup must not count as dump")
	}
}

func TestPlanCovers(t *testing.T) {
	p := &v1.Plan{Paths: []string{"/srv/data", "/var/lib/docker/volumes"}, Excludes: []string{"/srv/data/cache", "*.tmp"}}
	cases := map[string]bool{
		"/srv/data":                        true,
		"/srv/data/x/y":                    true,
		"/srv/database":                    false,
		"/srv/data/cache/a":                false,
		"/var/lib/docker/volumes/v1/_data": true,
		"/etc":                             false,
	}
	for path, want := range cases {
		if got := planCovers(p, path); got != want {
			t.Errorf("planCovers(%q) = %v", path, got)
		}
	}
}

func TestRedactURI(t *testing.T) {
	cases := map[string]string{
		"rest:https://user:secret@backup.example/repo": "rest:https://backup.example/repo",
		"sftp:u123@host.example:/backups":              "sftp:u123@host.example:/backups",
		"s3:https://s3.example/bucket":                 "s3:https://s3.example/bucket",
		"/mnt/repo":                                    "/mnt/repo",
	}
	for in, want := range cases {
		if got := RedactURI(in); got != want {
			t.Errorf("RedactURI(%q) = %q", in, got)
		}
	}
	if repoType("/mnt/repo") != "local" || repoType("sftp:u@h:/x") != "sftp" {
		t.Error("repoType")
	}
}

func TestEvaluate(t *testing.T) {
	s := &Service{s: Settings{CoverageMaxAge: 48 * time.Hour}}
	now := time.Now()
	plan := &v1.Plan{Id: "p", Paths: []string{"/srv"}, Schedule: &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "0 2 * * *"}}}
	snap := &v1.ResticSnapshot{Id: "abc", UnixTimeMs: now.Add(-time.Hour).UnixMilli(), Paths: []string{"/srv"}}
	failed := &v1.Operation{UnixTimeStartMs: now.UnixMilli(), DisplayMessage: "repository locked"}

	check := func(name string, run planRun, path, want string) {
		t.Helper()
		got := s.evaluate(nil, map[string]planRun{"p": run}, []*v1.Plan{plan}, path, now)
		if got.State != want {
			t.Errorf("%s: %s, want %s", name, got.State, want)
		}
	}
	check("fresh", planRun{snapshot: snap}, "/srv/a", StateProtected)
	check("never", planRun{}, "/srv/a", StateNotInSnapshot)
	check("failed first", planRun{lastBackup: failed}, "/srv/a", StateFailing)
	old := &v1.ResticSnapshot{Id: "old", UnixTimeMs: now.Add(-72 * time.Hour).UnixMilli(), Paths: []string{"/srv"}}
	check("stale", planRun{snapshot: old}, "/srv/a", StateStale)
	check("stale and failing", planRun{snapshot: old, lastBackup: failed}, "/srv/a", StateFailing)
	check("path added after snapshot", planRun{snapshot: &v1.ResticSnapshot{UnixTimeMs: snap.UnixTimeMs, Paths: []string{"/other"}}}, "/srv/a", StateNotInSnapshot)
	if got := s.evaluate(nil, nil, nil, "/srv/a", now); got.State != StateUnprotected {
		t.Errorf("no plan: %s", got.State)
	}
	plan.Schedule = &v1.Schedule{Schedule: &v1.Schedule_Disabled{Disabled: true}}
	check("disabled", planRun{snapshot: snap}, "/srv/a", StateDisabled)
}

func TestCoverageChange(t *testing.T) {
	s := &Service{notified: map[string]string{}}
	steps := []struct {
		state string
		want  bool
	}{{StateProtected, false}, {StateStale, true}, {StateStale, false}, {StateFailing, true}, {StateProtected, true}, {StateProtected, false}}
	for i, st := range steps {
		if subj, _ := s.coverageChange("compose:shop", "shop", st.state); (subj != "") != st.want {
			t.Errorf("step %d (%s): %q", i, st.state, subj)
		}
	}
}

func TestProtection(t *testing.T) {
	s := &Service{}
	cases := []struct {
		name  string
		items []string
		want  string
	}{
		{"all protected", []string{StateProtected, StateViaDump, StateNotNeeded}, ProtFull},
		{"one missing", []string{StateProtected, StateUnprotected}, ProtPartial},
		{"not reachable", []string{StateProtected, StateNotReachable}, ProtPartial},
		{"nothing", []string{StateUnprotected, StateUnprotected, StateNotNeeded}, ProtNone},
		{"failing", []string{StateFailing, StateProtected}, ProtFailed},
		{"stale", []string{StateStale, StateProtected}, ProtOverdue},
		{"unsupported db", []string{StateUnsupported, StateProtected}, ProtUnsupported},
		{"remote", []string{StateRemote, StateProtected}, ProtPartial},
		{"stopped service, data protected", []string{StateStopped, StateProtected}, ProtFull},
	}
	for _, c := range cases {
		ac := AppCoverage{App: "compose:x", State: StateProtected}
		for _, st := range c.items {
			ac.Items = append(ac.Items, CoverageItem{State: st})
		}
		s.protection(&ac, time.Now())
		if ac.Protection != c.want {
			t.Errorf("%s: %s, want %s", c.name, ac.Protection, c.want)
		}
		if c.want != ProtFull && len(ac.Actions) == 0 {
			t.Errorf("%s: no recommended action", c.name)
		}
	}
}

func TestOperationMessage(t *testing.T) {
	failed := &v1.Operation{Id: 1, PlanId: "app-shop", RepoId: "hosteurope", Status: v1.OperationStatus_STATUS_ERROR, DisplayMessage: "repository locked\nmore", Op: &v1.Operation_OperationBackup{OperationBackup: &v1.OperationBackup{}}}
	if subj, body := operationMessage(failed, false); subj != "Relyper Backup: backup of plan app-shop (hosteurope) FAILED" || body != "repository locked" {
		t.Errorf("%q %q", subj, body)
	}
	ok := &v1.Operation{Id: 2, PlanId: "app-shop", RepoId: "r", Status: v1.OperationStatus_STATUS_SUCCESS, Op: &v1.Operation_OperationBackup{OperationBackup: &v1.OperationBackup{}}}
	if subj, _ := operationMessage(ok, false); subj != "" {
		t.Error("success reported although disabled")
	}
	if subj, _ := operationMessage(ok, true); subj == "" {
		t.Error("success not reported")
	}
	check := &v1.Operation{Id: 3, PlanId: "_system_", RepoId: "r", Status: v1.OperationStatus_STATUS_ERROR, Op: &v1.Operation_OperationCheck{OperationCheck: &v1.OperationCheck{}}}
	if subj, _ := operationMessage(check, false); subj != "Relyper Backup: check of repository r FAILED" {
		t.Errorf("check: %q", subj)
	}
}

func TestStorageTargets(t *testing.T) {
	r := &v1.Repo{Uri: "sftp:u123@storage.example:/backups/relyper", Flags: []string{"--option=sftp.args=-i /config/.backrest-ssh/id_ed25519 -p 2222 -o BatchMode=yes"}}
	host, port, user, dir, id, ok := sftpTarget(r)
	if !ok || host != "storage.example" || port != 2222 || user != "u123" || dir != "/backups/relyper" || id != "/config/.backrest-ssh/id_ed25519" {
		t.Errorf("%s %d %s %s %s %v", host, port, user, dir, id, ok)
	}
	if h, p, _, d, _, ok := sftpTarget(&v1.Repo{Uri: "sftp://u@h.example:2200//abs/repo"}); !ok || h != "h.example" || p != 2200 || d != "/abs/repo" {
		t.Errorf("url form: %s %d %s", h, p, d)
	}
	if localRepoPath("/mnt/repo") != "/mnt/repo" || localRepoPath("local:/x/y") != "/x/y" || localRepoPath("s3:bucket") != "" {
		t.Error("localRepoPath")
	}
	free, total, err := statfs(t.TempDir() + "/not/yet")
	if err != nil || total <= 0 || free < 0 {
		t.Errorf("statfs: %d %d %v", free, total, err)
	}
}

func TestMeasure(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "x"), make([]byte, 1000), 0o644)
	os.WriteFile(filepath.Join(dir, "y"), make([]byte, 24), 0o644)
	dumps := filepath.Join(dir, "dumps")
	os.MkdirAll(dumps, 0o755)
	os.WriteFile(filepath.Join(dumps, "db.sql"), make([]byte, 5000), 0o644)
	est := measure(context.Background(), []string{dir}, nil, time.Second)
	if est.Bytes != 6024 || est.Files != 3 || !est.Complete {
		t.Fatalf("%+v", est)
	}
	// Excluded directories are not counted.
	if est := measure(context.Background(), []string{dir}, []string{dumps}, time.Second); est.Bytes != 1024 || est.Files != 2 {
		t.Fatalf("excludes: %+v", est)
	}
	if est := measure(context.Background(), []string{filepath.Join(dir, "missing")}, nil, time.Second); est.Complete {
		t.Fatalf("missing path counted as complete: %+v", est)
	}
	if est := measure(context.Background(), []string{dir}, nil, 0); est.Complete {
		t.Fatal("no time is not complete")
	}
}

func TestSetupOptionsValidate(t *testing.T) {
	o := DefaultSetupOptions()
	if err := o.validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []func(*SetupOptions){
		func(o *SetupOptions) { o.FilesCron = "daily" },
		func(o *SetupOptions) { o.DatabaseCron = "* * * *; rm -rf /" },
		func(o *SetupOptions) { o.Daily = -1 },
		func(o *SetupOptions) { o.Excludes = []string{"a\\nb", ""} },
		func(o *SetupOptions) { o.LimitUploadKiB = -5 },
		func(o *SetupOptions) { o.Compression = "zstd" },
	} {
		o := DefaultSetupOptions()
		bad(&o)
		if err := o.validate(); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}

func TestRestoreOrder(t *testing.T) {
	app := &discovery.App{Services: []discovery.Service{
		{Name: "web", DependsOn: []string{"api"}},
		{Name: "api", DependsOn: []string{"db", "cache"}},
		{Name: "cache", Database: &discovery.Database{Engine: "redis"}},
		{Name: "db", Database: &discovery.Database{Engine: "postgres"}},
		{Name: "worker", DependsOn: []string{"worker2"}},
		{Name: "worker2", DependsOn: []string{"worker"}}, // cycle
	}}
	got := strings.Join(RestoreOrder(app), ",")
	if got != "cache,db,api,web,worker2,worker" {
		t.Fatalf("order %s", got)
	}
}
