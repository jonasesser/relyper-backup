// Relyper-owned unit tests for coverage helpers.
package infra

import (
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
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
