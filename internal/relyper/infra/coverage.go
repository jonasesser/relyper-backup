package infra

import (
	"context"
	"errors"
	"path"
	"regexp"
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
	Kind         string `json:"kind"` // data, database, database_files, service
	Name         string `json:"name"`
	Path         string `json:"path,omitempty"`
	State        string `json:"state"`
	Plan         string `json:"plan,omitempty"`
	LastSnapshot string `json:"last_snapshot,omitempty"`
	SnapshotID   string `json:"snapshot_id,omitempty"`
	Detail       string `json:"detail,omitempty"`
	Class        string `json:"class,omitempty"` // data class, see discovery
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
	for _, app := range inv.Apps {
		ac := AppCoverage{App: app.ID, Name: app.Name, Kind: app.Kind, Notes: app.Notes}
		var dumpPlans []*v1.Plan
		for _, p := range cfg.Plans {
			if id, dir := HookApp(p); id == app.ID && planCovers(p, dbdump.Dir(dir, app.ID)) {
				dumpPlans = append(dumpPlans, p)
			}
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
				if len(dumpPlans) == 0 {
					item.State = StateUnprotected
				} else {
					dir := dbdump.Dir(s.s.DumpDir, app.ID)
					if _, d := HookApp(dumpPlans[0]); d != "" {
						dir = dbdump.Dir(d, app.ID)
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
				item := CoverageItem{Service: svc.Name, Kind: "data", Name: name, Path: d.BackupPath, Class: d.Class}
				switch {
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
		for _, f := range app.Deployment {
			item := CoverageItem{Kind: "deployment", Name: f.HostPath, Path: f.BackupPath, Class: "deployment"}
			if f.BackupPath == "" {
				item.State = StateNotReachable
				item.Detail = "mount " + f.HostPath + " into Relyper Backup (read-only) to back up the deployment definition"
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
		ac.State = StateProtected
		planSet := map[string]bool{}
		for _, it := range ac.Items {
			rep.Totals[it.State]++
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
		if it.State != StateNotNeeded {
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
			act("Create a backup plan for this app (it includes database dumps).")
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
