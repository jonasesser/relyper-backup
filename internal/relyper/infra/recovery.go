// Relyper-owned: guided recovery of an app. Step 1 restores the latest
// snapshot of the app's plan into a staging directory (never on top of live
// data). Step 2 is a generated runbook with the exact commands to put the
// staged data back into production in the right order. Replacing production
// data stays a manual step on the Docker host: it needs the app stopped and
// a human who checks the staged data first.
package infra

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/orchestrator/tasks"
	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

// RestoreOrder returns the services of an app in the order they should be
// started after a restore: databases first, then services after the
// services they depend on.
func RestoreOrder(app *discovery.App) []string {
	byName := map[string]discovery.Service{}
	for _, s := range app.Services {
		byName[s.Name] = s
	}
	names := make([]string, 0, len(app.Services))
	for _, s := range app.Services {
		names = append(names, s.Name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		di, dj := byName[names[i]].Database != nil, byName[names[j]].Database != nil
		if di != dj {
			return di
		}
		return names[i] < names[j]
	})
	var out []string
	done := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(n string)
	visit = func(n string) {
		if done[n] || visiting[n] {
			return // cycles are broken in the base order
		}
		visiting[n] = true
		deps := append([]string(nil), byName[n].DependsOn...)
		sort.Strings(deps)
		for _, d := range deps {
			if _, ok := byName[d]; ok {
				visit(d)
			}
		}
		visiting[n] = false
		done[n] = true
		out = append(out, n)
	}
	for _, n := range names {
		visit(n)
	}
	return out
}

// RestoreDir is where staged restores go.
func (s *Service) RestoreDir() string {
	if s.s.RestoreDir != "" {
		return s.s.RestoreDir
	}
	if fi, err := os.Stat("/restore"); err == nil && fi.IsDir() {
		return "/restore"
	}
	return filepath.Join(s.d.DataDir, "restore")
}

// StageResult describes a started staging restore.
type StageResult struct {
	App        string   `json:"app"`
	Plan       string   `json:"plan"`
	SnapshotID string   `json:"snapshot_id"`
	Snapshot   string   `json:"snapshot_time"`
	Target     string   `json:"target"`
	Operations []int64  `json:"operation_ids"`
	Paths      []string `json:"paths"`
}

// appPlan returns the plan that backs up an app: the plan that dumps its
// databases, else the default app plan ID, else a plan covering its data.
func (s *Service) appPlan(cfg *v1.Config, app *discovery.App) *v1.Plan {
	for _, p := range cfg.GetPlans() {
		if a, _ := HookApp(p); a == app.ID {
			return p
		}
	}
	if p := s.plan(cfg, DefaultPlanID(app.ID)); p != nil {
		return p
	}
	for _, svc := range app.Services {
		for _, d := range svc.Data {
			if d.BackupPath == "" {
				continue
			}
			for _, p := range cfg.GetPlans() {
				if planCovers(p, d.BackupPath) {
					return p
				}
			}
		}
	}
	return nil
}

// StageAppRestore restores the latest snapshot of the app's plan into a new
// staging directory. Each snapshot path becomes its own restore operation,
// visible in the operation list; the layout below the target mirrors the
// absolute paths.
func (s *Service) StageAppRestore(ctx context.Context, appID, actor string) (*StageResult, error) {
	if !discovery.ValidAppID(appID) {
		return nil, errors.New("invalid app ID")
	}
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	app := s.Inventory(ctx, false).Find(appID)
	if app == nil {
		return nil, fmt.Errorf("app %q not found", appID)
	}
	plan := s.appPlan(cfg, app)
	if plan == nil {
		return nil, errors.New("the app has no backup plan")
	}
	run := s.planRun(cfg, plan.Id)
	if run.snapshot == nil {
		return nil, fmt.Errorf("plan %q has no snapshot yet", plan.Id)
	}
	repo, err := s.d.Orchestrator.GetRepo(plan.Repo)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(s.RestoreDir(), discovery.Slug(appID)+"-"+time.Now().UTC().Format("20060102-150405"))
	if _, err := os.Stat(target); err == nil {
		return nil, errors.New("the staging directory exists already; try again in a second")
	}
	res := &StageResult{App: appID, Plan: plan.Id, SnapshotID: run.snapshot.Id, Snapshot: fmtTime(time.UnixMilli(run.snapshot.UnixTimeMs)), Target: target}
	for _, p := range run.snapshot.Paths {
		clean := path.Clean("/" + filepath.ToSlash(p))
		if clean == "/" {
			continue
		}
		dst := filepath.Join(target, filepath.FromSlash(path.Dir(clean)))
		id, err := s.d.Orchestrator.ScheduleTask(tasks.NewOneoffRestoreTask(repo, plan.Id, 0, time.Now(), run.snapshot.Id, clean, dst), tasks.TaskPriorityInteractive)
		if err != nil {
			return nil, err
		}
		res.Operations = append(res.Operations, id)
		res.Paths = append(res.Paths, clean)
	}
	s.record(actor, "restore.stage", "ok", fmt.Sprintf("%s: snapshot %s of plan %s -> %s", appID, short(run.snapshot.Id), plan.Id, target))
	return res, nil
}

// GuideStep is one step of a recovery runbook.
type GuideStep struct {
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

// RecoveryGuide is the runbook to put staged data back into production.
type RecoveryGuide struct {
	App     string      `json:"app"`
	Plan    string      `json:"plan,omitempty"`
	Staging string      `json:"staging"`
	Order   []string    `json:"order"`
	Steps   []GuideStep `json:"steps"`
	Notes   []string    `json:"notes,omitempty"`
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// AppRecoveryGuide returns the runbook for an app. staging is the staging
// directory of a restore (as Relyper Backup sees it); empty means the
// placeholder <staging>.
func (s *Service) AppRecoveryGuide(ctx context.Context, appID, staging string) (*RecoveryGuide, error) {
	if !discovery.ValidAppID(appID) {
		return nil, errors.New("invalid app ID")
	}
	app := s.Inventory(ctx, false).Find(appID)
	if app == nil {
		return nil, fmt.Errorf("app %q not found", appID)
	}
	if staging != "" {
		rel, err := filepath.Rel(s.RestoreDir(), filepath.Clean(staging))
		if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			return nil, errors.New("the staging directory must be below the restore directory")
		}
	} else {
		staging = "<staging>"
	}
	g := &RecoveryGuide{App: appID, Staging: staging, Order: RestoreOrder(app)}
	if cfg, err := s.cfg(); err == nil {
		if p := s.appPlan(cfg, app); p != nil {
			g.Plan = p.Id
		}
	}
	byName := map[string]discovery.Service{}
	for _, svc := range app.Services {
		byName[svc.Name] = svc
	}
	ctr := func(svc discovery.Service) string {
		if svc.SwarmService != "" {
			return svc.SwarmService
		}
		if svc.Container != "" {
			return svc.Container
		}
		return svc.Name
	}
	swarm := false
	for _, svc := range app.Services {
		swarm = swarm || svc.SwarmService != ""
	}

	g.Steps = append(g.Steps, GuideStep{
		Title:  "Check the staged data",
		Detail: "Look through the restored files before you touch production. Paths below the staging directory mirror the original paths inside Relyper Backup.",
		Commands: []string{
			"ls -la " + shq(staging),
		},
	})

	var stop []string
	rev := append([]string(nil), g.Order...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	for _, n := range rev {
		svc := byName[n]
		if svc.SwarmService != "" {
			stop = append(stop, "docker service scale "+shq(svc.SwarmService)+"=0")
		} else {
			stop = append(stop, "docker stop "+shq(ctr(svc)))
		}
	}
	g.Steps = append(g.Steps, GuideStep{Title: "Stop the app", Detail: "Stop all services so nothing writes while data is replaced (reverse start order).", Commands: stop})

	var copyCmds []string
	for _, n := range g.Order {
		svc := byName[n]
		for _, d := range svc.Data {
			if d.DatabaseFiles || !discovery.Backed(d.Class) || d.BackupPath == "" {
				continue
			}
			src := strings.TrimRight(staging, "/") + d.BackupPath
			copyCmds = append(copyCmds, fmt.Sprintf("rsync -a --delete %s/ %s/   # %s %s", shq(src), shq(d.HostPath), n, d.ContainerPath))
		}
	}
	for _, f := range app.Deployment {
		if f.BackupPath != "" {
			copyCmds = append(copyCmds, fmt.Sprintf("cp -a %s %s   # deployment definition", shq(strings.TrimRight(staging, "/")+f.BackupPath), shq(f.HostPath)))
		}
	}
	if len(copyCmds) > 0 {
		g.Steps = append(g.Steps, GuideStep{
			Title:    "Copy the files back",
			Detail:   "Run on the Docker host. The source is the staging directory as seen on the host: if Relyper Backup mounts its restore directory from elsewhere (e.g. ./restore → /restore), adjust the first part of the path. --delete removes files that were created after the snapshot.",
			Commands: copyCmds,
		})
	}

	var dbs, dbStart []string
	for _, n := range g.Order {
		svc := byName[n]
		if svc.Database == nil {
			continue
		}
		if svc.SwarmService != "" {
			dbStart = append(dbStart, "docker service scale "+shq(svc.SwarmService)+"=1")
		} else {
			dbStart = append(dbStart, "docker start "+shq(ctr(svc)))
		}
		dbs = append(dbs, n+"="+ctr(svc))
	}
	if len(dbs) > 0 {
		dumpDir := strings.TrimRight(staging, "/") + dbdump.Dir(s.s.DumpDir, appID)
		cmds := append(dbStart, fmt.Sprintf("sh %s/%s %s", shq(dumpDir), dbdump.RestoreScriptName, strings.Join(dbs, " ")))
		detail := "Start the database containers, wait until they accept connections, then load the dumps. The script asks before replacing each database and checks the dump checksums; " + dbdump.RestoreNotesName + " next to it lists the expected content. Snapshots made before Relyper Backup wrote this script have none: load the dumps by hand as described in the recovery report."
		if swarm {
			detail += " For Swarm services pass the running task container names instead of the service names (docker ps)."
		}
		g.Steps = append(g.Steps, GuideStep{Title: "Load the databases", Detail: detail, Commands: cmds})
	}

	var start []string
	for _, n := range g.Order {
		svc := byName[n]
		if svc.Database != nil {
			continue
		}
		if svc.SwarmService != "" {
			start = append(start, "docker service scale "+shq(svc.SwarmService)+"=1")
		} else {
			start = append(start, "docker start "+shq(ctr(svc)))
		}
	}
	if len(start) > 0 {
		g.Steps = append(g.Steps, GuideStep{Title: "Start the app", Detail: "In this order: services after the services they depend on.", Commands: start})
	}
	g.Steps = append(g.Steps, GuideStep{
		Title:  "Check the app",
		Detail: "Open the app and check recent data. Then run a backup and a database verification on the Infrastructure page so the coverage shows the restored state.",
	})
	if swarm {
		g.Notes = append(g.Notes, "Swarm: replica counts are set to 1 above; use the counts of your deployment.")
	}
	if app.Kind == "easypanel" {
		g.Notes = append(g.Notes, "Easypanel: you can also stop and start the services in the Easypanel UI instead of the docker commands.")
	}
	for _, svc := range app.Services {
		if svc.UnsupportedDatabase != "" {
			g.Notes = append(g.Notes, fmt.Sprintf("%s (%s) has no dump; its files were backed up as files while it ran and may need the engine's own repair tools.", svc.Name, svc.UnsupportedDatabase))
		}
	}
	return g, nil
}

// InventoryDir holds the recovery report that every app plan backs up, so
// a snapshot carries the instructions to restore it.
func (s *Service) InventoryDir() string { return filepath.Join(s.s.DumpDir, "_inventory") }

// InventoryReportName is the file name of the report in InventoryDir.
const InventoryReportName = "recovery-report.md"

// WriteInventory writes the current recovery report into InventoryDir.
func (s *Service) WriteInventory(ctx context.Context) error {
	rep, err := s.RecoveryReport(ctx)
	if err != nil {
		return err
	}
	dir := s.InventoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+InventoryReportName+".tmp")
	if err := os.WriteFile(tmp, []byte(rep), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, InventoryReportName))
}
