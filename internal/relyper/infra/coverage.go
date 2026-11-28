package infra

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

// Coverage states of an item, from best to worst.
const (
	StateProtected     = "protected"       // in a snapshot younger than the maximum age
	StateStale         = "stale"           // last snapshot is older than the maximum age
	StateFailing       = "failing"         // the latest backup attempt failed
	StateNotInSnapshot = "not_in_snapshot" // a plan covers it, but no completed snapshot contains it yet
	StateDisabled      = "disabled"        // only covered by disabled plans
	StateUnprotected   = "unprotected"     // no plan covers it
	StateNotReachable  = "not_reachable"   // the data is not mounted into Relyper Backup
	StateRemote        = "remote"          // runs on another Swarm node
	StateStopped       = "stopped"         // Swarm service without a running task; its data here is still backed up
	StateViaDump       = "covered_by_dump" // live database files; the dump is what counts
	StateUnsupported   = "unsupported"     // a database without consistent dump support
	StateNoData        = "no_data"         // app without volumes, bind mounts or databases
	StateNotNeeded     = "not_needed"      // cache or temporary data, rebuilt by the app
)

// Protection is the overall status of an app in the terms of the product
// concept. It combines coverage with restore verification.
const (
	ProtFull        = "fully_protected"           // everything needed is in a recent snapshot and restores were verified
	ProtPartial     = "partially_protected"       // some required data is not backed up or not reachable
	ProtNone        = "not_protected"             // nothing required is in a snapshot
	ProtFailed      = "backup_failed"             // the latest backup attempt failed
	ProtOverdue     = "backup_overdue"            // the latest snapshot is older than the maximum age
	ProtUnverified  = "restore_not_verified"      // backed up, but no recent successful restore test
	ProtUnsupported = "unsupported_configuration" // a component cannot be backed up consistently
	ProtNoData      = "no_data"                   // nothing to back up
)

// CoverageItem is one volume, bind mount or database of an app.
type CoverageItem struct {
	Service      string `json:"service"`
	Kind         string `json:"kind"` // data, database, database_files, service, deployment, compose_directory, service_folder, easypanel_settings, app_definition
	Name         string `json:"name"`
	Path         string `json:"path,omitempty"`
	State        string `json:"state"`
	Plan         string `json:"plan,omitempty"`
	LastSnapshot string `json:"last_snapshot,omitempty"`
	SnapshotID   string `json:"snapshot_id,omitempty"`
	Detail       string `json:"detail,omitempty"`
	Class        string `json:"class,omitempty"` // data class, see discovery
	// SQLite lists SQLite database files inside the data (relative paths).
	SQLite []string `json:"sqlite,omitempty"`
}

// AppCoverage is the coverage of one app.
type AppCoverage struct {
	App   string `json:"app"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"` // worst relevant item state
	// Protection is the overall status (fully_protected, partially_protected,
	// not_protected, backup_failed, backup_overdue, restore_not_verified,
	// unsupported_configuration, no_data).
	Protection string `json:"protection"`
	// Actions are the recommended next steps, most important first.
	Actions   []string       `json:"actions,omitempty"`
	Databases []string       `json:"databases,omitempty"` // engines, for filtering
	Items     []CoverageItem `json:"items"`
	Plans     []string       `json:"plans,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	// Project is the Easypanel project; Tags the Relyper Backup tags;
	// Members the apps of a tag group (kind group).
	Project string   `json:"project,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Members []string `json:"members,omitempty"`
	// DatabaseLinks are databases the app's services connect to that run
	// elsewhere (another app on this host, or outside it).
	DatabaseLinks []AppDatabaseLink `json:"database_links,omitempty"`
	// OldTasks are containers of earlier Swarm tasks: history, not backed up.
	OldTasks []discovery.OldTask `json:"old_tasks,omitempty"`
}

// CoverageReport covers all apps.
type CoverageReport struct {
	GeneratedAt  string         `json:"generated_at"`
	Docker       discovery.Host `json:"docker"`
	MaxAgeHours  int            `json:"max_age_hours"`
	Apps         []AppCoverage  `json:"apps"`
	Totals       map[string]int `json:"totals"` // items per state
	PlansWithout []string       `json:"plans_without_app,omitempty"`
}

var stateRank = map[string]int{
	StateNoData: 0, StateNotNeeded: 0, StateProtected: 0, StateViaDump: 0, StateStopped: 0, StateRemote: 1, StateUnsupported: 2, StateStale: 2, StateNotInSnapshot: 3, StateDisabled: 4, StateFailing: 5, StateNotReachable: 6, StateUnprotected: 7,
}

var hookRe = regexp.MustCompile(`dump --dir '([^']+)' '([^']+)'`)

// HookApp returns the app ID and dump directory if the plan dumps an app's
// databases before backup.
func HookApp(p *v1.Plan) (appID, dir string) {
	for _, h := range p.GetHooks() {
		cmd := h.GetActionCommand().GetCommand()
		if cmd == "" || !strings.Contains(cmd, "dump --dir") {
			continue
		}
		hasStart := false
		for _, c := range h.Conditions {
			if c == v1.Hook_CONDITION_SNAPSHOT_START {
				hasStart = true
			}
		}
		if m := hookRe.FindStringSubmatch(cmd); m != nil && hasStart {
			return m[2], m[1]
		}
	}
	return "", ""
}

// under reports whether p equals root or lies below it.
func under(p, root string) bool {
	p, root = path.Clean(p), path.Clean(root)
	return p == root || root == "/" || strings.HasPrefix(p, root+"/")
}

func excluded(p string, excludes []string) bool {
	for _, e := range excludes {
		if strings.HasPrefix(e, "/") && !strings.ContainsAny(e, "*?[") && under(p, e) {
			return true
		}
	}
	return false
}

// planCovers reports whether the plan's configured paths include p.
func planCovers(plan *v1.Plan, p string) bool {
	if excluded(p, plan.Excludes) {
		return false
	}
	for _, root := range plan.Paths {
		if under(p, root) {
			return true
		}
	}
	return false
}

// planRun summarizes the completed runs of a plan.
type planRun struct {
	snapshot     *v1.ResticSnapshot // latest snapshot that is not forgotten
	lastBackup   *v1.Operation      // latest finished backup attempt
	lastBackupOK bool
}

func (s *Service) planRun(cfg *v1.Config, planID string) planRun {
	var r planRun
	if s.d.OpLog == nil {
		return r
	}
	q := oplog.Query{}.SetInstanceID(cfg.Instance).SetPlanID(planID).SetReversed(true)
	stop := errors.New("stop")
	_ = s.d.OpLog.Query(q, func(op *v1.Operation) error {
		if is := op.GetOperationIndexSnapshot(); is != nil && r.snapshot == nil && !is.Forgot && is.Snapshot != nil {
			r.snapshot = is.Snapshot
		}
		if b := op.GetOperationBackup(); b != nil && r.lastBackup == nil && !b.DryRun &&
			op.Status != v1.OperationStatus_STATUS_PENDING && op.Status != v1.OperationStatus_STATUS_INPROGRESS && op.Status != v1.OperationStatus_STATUS_SYSTEM_CANCELLED && op.Status != v1.OperationStatus_STATUS_USER_CANCELLED {
			r.lastBackup = op
			r.lastBackupOK = op.Status == v1.OperationStatus_STATUS_SUCCESS || op.Status == v1.OperationStatus_STATUS_WARNING
		}
		if r.snapshot != nil && r.lastBackup != nil {
			return stop
		}
		return nil
	})
	return r
}

func snapshotContains(snap *v1.ResticSnapshot, p string) bool {
	for _, sp := range snap.GetPaths() {
		if under(p, sp) {
			return true
		}
	}
	return false
}

// evaluate returns the best state of p across plans.
func (s *Service) evaluate(cfg *v1.Config, runs map[string]planRun, plans []*v1.Plan, p string, now time.Time) CoverageItem {
	best := CoverageItem{State: StateUnprotected}
	for _, plan := range plans {
		item := CoverageItem{Plan: plan.Id}
		run := runs[plan.Id]
		switch {
		case plan.GetSchedule().GetDisabled():
			item.State = StateDisabled
		case run.snapshot == nil || !snapshotContains(run.snapshot, p):
			item.State = StateNotInSnapshot
			if run.lastBackup != nil && !run.lastBackupOK {
				item.State = StateFailing
				item.Detail = firstLine(run.lastBackup.DisplayMessage)
			}
		default:
			t := time.UnixMilli(run.snapshot.UnixTimeMs)
			item.LastSnapshot, item.SnapshotID = fmtTime(t), run.snapshot.Id
			item.State = StateProtected
			if now.Sub(t) > s.s.CoverageMaxAge {
				item.State = StateStale
			}
			if run.lastBackup != nil && !run.lastBackupOK && run.lastBackup.UnixTimeStartMs > run.snapshot.UnixTimeMs {
				item.Detail = "the latest backup attempt failed: " + firstLine(run.lastBackup.DisplayMessage)
				if item.State == StateStale {
					item.State = StateFailing
				}
			}
		}
		if plan.GetSchedule().GetDisabled() && item.State != StateDisabled {
			item.State = StateDisabled
		}
		if best.Plan == "" || stateRank[item.State] < stateRank[best.State] {
			best = item
		}
	}
	return best
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Coverage computes the coverage of all discovered apps.
func (s *Service) Coverage(ctx context.Context) (*CoverageReport, error) {
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	inv := s.Inventory(ctx, false)
	now := time.Now()
	rep := &CoverageReport{GeneratedAt: fmtTime(now), Docker: inv.Docker, MaxAgeHours: int(s.s.CoverageMaxAge.Hours()), Totals: map[string]int{}}
	runs := map[string]planRun{}
	for _, p := range cfg.Plans {
		runs[p.Id] = s.planRun(cfg, p.Id)
	}
	usedPlans := map[string]bool{}
	// A dump plan of a tag group (or of a whole Easypanel project, from
	// before each Easypanel service became its own app) also dumps the
	// databases of its members.
	hookMembers := map[string]map[string]bool{}
	for _, p := range cfg.Plans {
		if id, _ := HookApp(p); id != "" && hookMembers[id] == nil {
			hookMembers[id] = map[string]bool{id: true}
			if g := inv.Find(id); g != nil {
				for _, m := range g.Members {
					hookMembers[id][m] = true
				}
			}
		}
	}
	for _, app := range inv.Apps {
		ac := AppCoverage{App: app.ID, Name: app.Name, Kind: app.Kind, Notes: app.Notes, Project: app.Project, Tags: app.Tags, Members: app.Members, OldTasks: app.OldTasks}
		for _, svc := range app.Services {
			for _, l := range svc.DatabaseLinks {
				if l.App != "" && slices.Contains(app.Members, l.App) {
					continue // a member of the same group: its dump is in the plan
				}
				ac.DatabaseLinks = append(ac.DatabaseLinks, AppDatabaseLink{From: svc.Name, DatabaseLink: l})
			}
		}
		// dumpPlansFor lists the plans that dump the databases of a service
		// and the app whose dump directory they use. A service of a group is
		// also protected by the dump plan of the app it belongs to.
		dumpPlansFor := func(svc discovery.Service) (plans []*v1.Plan, dumpApp string) {
			dumpApp = app.ID
			for _, p := range cfg.Plans {
				id, dir := HookApp(p)
				if (hookMembers[id][app.ID] || (svc.Member != "" && hookMembers[id][svc.Member])) && planCovers(p, dbdump.Dir(dir, id)) {
					if len(plans) == 0 {
						dumpApp = id
					}
					plans = append(plans, p)
				}
			}
			return plans, dumpApp
		}
		dbState := StateUnprotected
		for _, svc := range app.Services {
			if svc.State == "remote" {
				ac.Items = append(ac.Items, CoverageItem{Service: svc.Name, Kind: "service", Name: svc.SwarmService, State: StateRemote, Detail: "runs on another Swarm node"})
				continue
			}
			if svc.State == "stopped" {
				detail := "stopped (no running task); its data on this node is still backed up"
				if svc.Database != nil {
					detail = "stopped database; its files are backed up as files, a dump is made again once it runs"
				}
				ac.Items = append(ac.Items, CoverageItem{Service: svc.Name, Kind: "service", Name: svc.SwarmService, State: StateStopped, Detail: detail})
			}
			if svc.UnsupportedDatabase != "" {
				ac.Databases = appendUnique(ac.Databases, svc.UnsupportedDatabase)
				ac.Items = append(ac.Items, CoverageItem{Service: svc.Name, Kind: "database", Name: svc.UnsupportedDatabase, State: StateUnsupported,
					Detail: "no consistent dump support for " + svc.UnsupportedDatabase + "; its files are only backed up as files"})
			}
			if svc.Database != nil && svc.State == "stopped" {
				ac.Databases = appendUnique(ac.Databases, svc.Database.Engine)
			} else if svc.Database != nil {
				ac.Databases = appendUnique(ac.Databases, svc.Database.Engine)
				item := CoverageItem{Service: svc.Name, Kind: "database", Name: svc.Database.Engine}
				dumpPlans, dumpApp := dumpPlansFor(svc)
				if len(dumpPlans) == 0 {
					item.State = StateUnprotected
				} else {
					dir := dbdump.Dir(s.s.DumpDir, dumpApp)
					if _, d := HookApp(dumpPlans[0]); d != "" {
						dir = dbdump.Dir(d, dumpApp)
					}
					item = mergeItem(item, s.evaluate(cfg, runs, dumpPlans, dir, now))
					item.Path = dir
				}
				dbState = item.State
				ac.Items = append(ac.Items, item)
			}
			for _, d := range svc.Data {
				name := d.Name
				if name == "" {
					name = d.HostPath
				}
				item := CoverageItem{Service: svc.Name, Kind: "data", Name: name, Path: d.BackupPath, Class: d.Class, SQLite: d.SQLite}
				switch {
				case d.Class == discovery.ClassSource:
					item.Kind, item.State = "source_mount", StateNotNeeded
					item.Detail = "read-only mount through which Relyper Backup reads other apps' data; backed up through those apps (and the Easypanel card), not here"
				case d.Class == discovery.ClassCache || d.Class == discovery.ClassTemporary:
					item.State = StateNotNeeded
					item.Detail = d.Class + " data is rebuilt by the app and not backed up"
					if d.Type == "tmpfs" {
						item.Name = "tmpfs " + d.ContainerPath
					}
				case d.DatabaseFiles:
					item.Kind, item.State = "database_files", StateViaDump
					item.Detail = "live database files are not backed up as files; the dump protects them"
					if dbState != StateProtected {
						item.State = dbState
					}
				case d.BackupPath == "":
					item.State = StateNotReachable
					item.Detail = "mount " + d.HostPath + " into Relyper Backup (read-only) to back it up"
				default:
					var covering []*v1.Plan
					for _, p := range cfg.Plans {
						if planCovers(p, d.BackupPath) {
							covering = append(covering, p)
						}
					}
					item = mergeItem(item, s.evaluate(cfg, runs, covering, d.BackupPath, now))
				}
				ac.Items = append(ac.Items, item)
			}
		}
		// The compose directory (compose files, .env, …) replaces the single
		// compose files inside it.
		type cfgFile struct {
			f   discovery.DeploymentFile
			dir bool
		}
		var cfgFiles []cfgFile
		for _, d := range app.ConfigDirs {
			cfgFiles = append(cfgFiles, cfgFile{d, true})
		}
		for _, f := range app.Deployment {
			inside := false
			for _, d := range app.ConfigDirs {
				if strings.HasPrefix(f.HostPath, strings.TrimSuffix(d.HostPath, "/")+"/") {
					inside = true
				}
			}
			if !inside {
				cfgFiles = append(cfgFiles, cfgFile{f, false})
			}
		}
		for _, cf := range cfgFiles {
			f := cf.f
			item := CoverageItem{Kind: "deployment", Name: f.HostPath, Path: f.BackupPath, Class: "deployment"}
			what := "the deployment definition"
			if cf.dir {
				item.Kind, what = "compose_directory", "the compose directory (compose files, .env)"
			}
			if f.BackupPath == "" {
				item.State = StateNotReachable
				item.Detail = "mount " + f.HostPath + " into Relyper Backup (read-only) to back up " + what
			} else {
				var covering []*v1.Plan
				for _, p := range cfg.Plans {
					if planCovers(p, f.BackupPath) {
						covering = append(covering, p)
					}
				}
				item = mergeItem(item, s.evaluate(cfg, runs, covering, f.BackupPath, now))
			}
			ac.Items = append(ac.Items, item)
		}
		ac.Items = append(ac.Items, s.configItems(cfg, runs, inv, &app, ac.Items, hookMembers, now)...)
		ac.State = StateProtected
		planSet := map[string]bool{}
		for _, it := range ac.Items {
			if app.Kind != discovery.KindGroup {
				rep.Totals[it.State]++
			}
			if stateRank[it.State] > stateRank[ac.State] {
				ac.State = it.State
			}
			if it.Plan != "" && !planSet[it.Plan] {
				planSet[it.Plan] = true
				ac.Plans = append(ac.Plans, it.Plan)
				usedPlans[it.Plan] = true
			}
		}
		if len(ac.Items) == 0 || allNotNeeded(ac.Items) {
			ac.State = StateNoData
			ac.Notes = append(ac.Notes, "no volumes, bind mounts or databases: nothing to back up")
		}
		s.protection(&ac, now)
		rep.Apps = append(rep.Apps, ac)
	}
	for _, p := range cfg.Plans {
		if !usedPlans[p.Id] {
			rep.PlansWithout = append(rep.PlansWithout, p.Id)
		}
	}
	return rep, nil
}

func allNotNeeded(items []CoverageItem) bool {
	for _, it := range items {
		// A stopped service without data is only a marker, not data.
		if it.State != StateNotNeeded && !(it.Kind == "service" && it.State == StateStopped) {
			return false
		}
	}
	return true
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// AppDatabaseLink is a database a service of the app connects to.
type AppDatabaseLink struct {
	From string `json:"from"` // the app's service
	discovery.DatabaseLink
}

// protection derives the overall status and the recommended actions.
func (s *Service) protection(ac *AppCoverage, now time.Time) {
	count := map[string]int{}
	required := 0
	for _, it := range ac.Items {
		count[it.State]++
		if it.State != StateNotNeeded && it.State != StateRemote && it.State != StateStopped {
			required++
		}
	}
	missing := count[StateUnprotected] + count[StateNotReachable] + count[StateNotInSnapshot] + count[StateDisabled]
	act := func(a string) { ac.Actions = appendUnique(ac.Actions, a) }
	if count[StateUnprotected] > 0 {
		if len(ac.Plans) == 0 {
			act(createPlanAction(ac))
		} else {
			act("Add the unprotected items to a plan, or create the app backup plan.")
		}
	}
	if count[StateNotReachable] > 0 {
		act("Mount the unreachable paths read-only into Relyper Backup (see DEPLOYMENT.md), then refresh.")
	}
	if count[StateNotInSnapshot] > 0 {
		act("Run a backup so that a completed snapshot contains all items.")
	}
	if count[StateDisabled] > 0 {
		act("Enable the plan's schedule.")
	}
	if count[StateFailing] > 0 {
		act("Fix the failing backup (see the item details and the plan's operation log).")
	}
	if count[StateStale] > 0 {
		act("Check the plan's schedule; the latest snapshot is older than the maximum age.")
	}
	if count[StateUnsupported] > 0 {
		act("Stop the unsupported database during the backup or dump it from your own hook; its files alone are not a consistent backup.")
	}
	if count[StateRemote] > 0 {
		act("Run Relyper Backup on the other Swarm nodes too, or pin the services with data to this node.")
	}
	unverified := s.unverified(ac)
	switch {
	case ac.State == StateNoData:
		ac.Protection = ProtNoData
		return
	case count[StateFailing] > 0:
		ac.Protection = ProtFailed
	case missing > 0 && missing >= required-count[StateUnsupported]:
		ac.Protection = ProtNone
	case missing > 0 || count[StateRemote] > 0:
		ac.Protection = ProtPartial
	case count[StateStale] > 0:
		ac.Protection = ProtOverdue
	case count[StateUnsupported] > 0:
		ac.Protection = ProtUnsupported
	case unverified != "":
		ac.Protection = ProtUnverified
	default:
		ac.Protection = ProtFull
	}
	if unverified != "" && missing == 0 && count[StateFailing] == 0 {
		act(unverified)
	}
}

// unverified returns the missing restore verification, or "".
func (s *Service) unverified(ac *AppCoverage) string {
	if s.store == nil {
		return ""
	}
	if len(ac.Databases) > 0 && hasSupportedDB(ac) {
		v := s.store.verifications(ac.App)
		if len(v) == 0 || !v[0].OK || olderThan(v[0].Time, time.Now(), verifyMaxAge) {
			return "Run a database restore test (it restores the dumps into a throwaway container)."
		}
	}
	for _, p := range ac.Plans {
		t := s.store.restoreTests(p)
		if len(t) == 0 || !t[0].OK || olderThan(t[0].Time, time.Now(), verifyMaxAge) {
			return "Run a file restore test for plan " + p + "."
		}
	}
	return ""
}

func hasSupportedDB(ac *AppCoverage) bool {
	for _, it := range ac.Items {
		if it.Kind == "database" && it.State != StateUnsupported {
			return true
		}
	}
	return false
}

func mergeItem(base, eval CoverageItem) CoverageItem {
	base.State, base.Plan, base.LastSnapshot, base.SnapshotID = eval.State, eval.Plan, eval.LastSnapshot, eval.SnapshotID
	if eval.Detail != "" {
		base.Detail = eval.Detail
	}
	return base
}

// createPlanAction says what an app plan would contain about databases,
// so the action never promises dumps that do not exist.
func createPlanAction(ac *AppCoverage) string {
	var own []string
	for _, it := range ac.Items {
		if it.Kind == "database" && it.State != StateUnsupported {
			own = append(own, it.Name+" ("+it.Service+")")
		}
	}
	if len(own) > 0 {
		return "Create a backup plan for this app; it also dumps its databases: " + strings.Join(own, ", ") + "."
	}
	for _, it := range ac.Items {
		if len(it.SQLite) > 0 {
			return "Create a backup plan for this app; its SQLite databases are backed up with the " + it.Name + " volume."
		}
	}
	var other []string
	for _, l := range ac.DatabaseLinks {
		switch {
		case l.App != "":
			other = appendUnique(other, l.App)
		case l.Engine == "sqlite":
		default:
			other = appendUnique(other, l.Host+" (not on this host)")
		}
	}
	if len(other) > 0 {
		return "Create a backup plan for this app. Its database runs in " + strings.Join(other, ", ") + ": protect that too, e.g. with one tag group for both."
	}
	return "Create a backup plan for this app (no database found in it)."
}

// configItems are the settings every app has besides its data: for an
// Easypanel service its folder (code/) and Easypanel's own settings
// (environment variables, domains, ports, Traefik, certificates), and for
// every app its definition in the recovery report.
func (s *Service) configItems(cfg *v1.Config, runs map[string]planRun, inv *discovery.Inventory, app *discovery.App, items []CoverageItem, hookMembers map[string]map[string]bool, now time.Time) []CoverageItem {
	if app.Kind == discovery.KindGroup {
		return nil
	}
	covered := func(item CoverageItem, p string) CoverageItem {
		var covering []*v1.Plan
		for _, pl := range cfg.Plans {
			if planCovers(pl, p) {
				covering = append(covering, pl)
			}
		}
		return mergeItem(item, s.evaluate(cfg, runs, covering, p, now))
	}
	var out []CoverageItem
	if inv.Docker.Easypanel && isEasypanelApp(inv, app) {
		mounted := false
		for _, d := range inv.Docker.EasypanelConfig {
			if d.BackupPath == "" {
				continue
			}
			mounted = true
			for _, ps := range easypanelServices(inv, app) {
				dir := filepath.Join(d.BackupPath, "projects", ps[0], ps[1])
				if st, err := os.Stat(dir); err == nil && st.IsDir() {
					out = append(out, covered(CoverageItem{Service: ps[1], Kind: "service_folder", Name: "Easypanel service folder (code/, files)", Path: dir, Class: "configuration"}, dir))
				}
			}
			// Easypanel's settings: the item is as good as the worst entry.
			item := CoverageItem{Kind: "easypanel_settings", Name: "Easypanel settings (environment variables, domains, ports, Traefik, certificates)", Path: d.BackupPath, Class: "configuration", State: StateProtected}
			entries := easypanelEntries(d.BackupPath)
			if len(entries) == 0 {
				item.State = StateUnprotected
			}
			for _, e := range entries {
				ev := covered(CoverageItem{}, e)
				if stateRank[ev.State] >= stateRank[item.State] {
					item = mergeItem(item, ev)
				}
			}
			if app.Kind != discovery.KindSystem && item.State != StateProtected {
				item.Detail = appendDetail(item.Detail, "shared by all services; back them up with the plan of Easypanel itself ("+DefaultPlanID(discovery.EasypanelSystemID)+")")
			}
			out = append(out, item)
		}
		if !mounted {
			out = append(out, CoverageItem{Kind: "easypanel_settings", Name: "Easypanel settings (environment variables, domains, ports, Traefik, certificates)", State: StateNotReachable, Class: "configuration",
				Detail: "mount /etc/easypanel:/etc/easypanel:ro into Relyper Backup to back them up"})
		}
	}
	if app.Kind != discovery.KindSystem {
		def := filepath.Join(s.InventoryDir(), "apps", discovery.Slug(app.ID)+".json")
		// Every app plan carries the whole recovery report; the definition
		// counts as protected only by a plan of this app (its own plan, its
		// dump plan, or one that also backs up its data or settings).
		var own []*v1.Plan
		for _, pl := range cfg.Plans {
			if !planCovers(pl, def) {
				continue
			}
			id, _ := HookApp(pl)
			mine := pl.Id == DefaultPlanID(app.ID) || hookMembers[id][app.ID]
			for _, it := range append(items, out...) {
				if it.Path != "" && it.Kind != "database" && planCovers(pl, it.Path) {
					mine = true
				}
			}
			if mine {
				own = append(own, pl)
			}
		}
		item := mergeItem(CoverageItem{Kind: "app_definition", Name: "app definition (images, mounts, ports, domains, networks; in the recovery report)", Path: def, Class: "configuration"}, s.evaluate(cfg, runs, own, def, now))
		if app.Kind != discovery.KindEasypanel && len(app.ConfigDirs) == 0 {
			item.Detail = appendDetail(item.Detail, "values of environment variables are not readable through the Docker guard; keep them where you deploy from")
		}
		out = append(out, item)
	}
	return out
}

func appendDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
