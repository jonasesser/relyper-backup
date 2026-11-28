// Relyper-owned: the backup setup wizard. It proposes one plan per app for a
// whole Docker environment, all Easypanel projects or a selection of apps,
// measures how much data each plan would back up, and creates the plans with
// shared options (schedule, retention, exclusions, bandwidth, compression).
package infra

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

// Setup scopes.
const (
	ScopeAll       = "all"       // every app on the Docker host
	ScopeEasypanel = "easypanel" // Easypanel itself and every Easypanel service
	// ScopeEasypanelConfig is Easypanel itself only: its settings and the
	// configuration and code of every service, without app data.
	ScopeEasypanelConfig = "easypanel_config"
	// ScopeDatabases is every app with a database server: one plan per app
	// with only the consistent dumps of its databases.
	ScopeDatabases = "databases"
	ScopeApps      = "apps" // the apps listed in Apps
)

// ModeDatabases creates dump-only plans (scope databases).
const ModeDatabases = "databases"

// DatabasePlanID is the ID of the dump-only plan of an app.
func DatabasePlanID(appID string) string {
	id := DefaultPlanID(appID)
	if len(id) > 61 {
		id = id[:61]
	}
	return id + "-db"
}

// SetupRequest selects the apps.
type SetupRequest struct {
	Scope string   `json:"scope"`
	Apps  []string `json:"apps,omitempty"`
}

// SizeEstimate is the measured size of a plan's paths. Complete is false
// when the measurement stopped early (time limit or unreadable files); Bytes
// is then a lower bound.
type SizeEstimate struct {
	Bytes    int64 `json:"bytes"`
	Files    int64 `json:"files"`
	Complete bool  `json:"complete"`
}

// SetupProposal is the proposal for one app.
type SetupProposal struct {
	App          string        `json:"app"`
	Name         string        `json:"name"`
	Kind         string        `json:"kind"`
	Plan         *AppPlan      `json:"plan,omitempty"`
	Error        string        `json:"error,omitempty"`
	ExistingPlan bool          `json:"existing_plan"` // a plan with the proposed ID exists
	HasDatabases bool          `json:"has_databases"`
	Size         *SizeEstimate `json:"size,omitempty"`
	// Items are the measured sizes of the chosen paths, largest first.
	Items []ItemSize `json:"items,omitempty"`
}

// ItemSize is the measured size of one chosen path of a plan.
type ItemSize struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	SizeEstimate
}

// SetupProposals is the wizard's preview.
type SetupProposals struct {
	Apps []SetupProposal `json:"apps"`
	// Mode is "databases" for dump-only plans (scope databases).
	Mode string `json:"mode,omitempty"`
	// Notes are hints about the selection, e.g. SQLite databases.
	Notes      []string               `json:"notes,omitempty"`
	TotalBytes int64                  `json:"total_bytes"`
	Complete   bool                   `json:"complete"`          // every size was measured completely
	Storage    map[string]StorageInfo `json:"storage,omitempty"` // by repository`
	Defaults   SetupOptions           `json:"defaults"`
}

// SetupOptions are applied to every plan created by the wizard. Empty
// schedules and an all-zero retention take the defaults.
type SetupOptions struct {
	// Cron schedules (local time). Apps with databases use DatabaseCron.
	FilesCron    string `json:"files_cron,omitempty"`
	DatabaseCron string `json:"database_cron,omitempty"`
	Daily        int32  `json:"daily,omitempty"`
	Weekly       int32  `json:"weekly,omitempty"`
	Monthly      int32  `json:"monthly,omitempty"`
	Yearly       int32  `json:"yearly,omitempty"`
	// Excludes are glob patterns (restic --exclude).
	Excludes []string `json:"excludes,omitempty"`
	// LimitUploadKiB limits the upload rate in KiB/s; 0 = unlimited.
	LimitUploadKiB int `json:"limit_upload_kib,omitempty"`
	// Compression is auto, max or off (restic repository format 2).
	Compression string `json:"compression,omitempty"`
}

// DefaultSetupOptions follow the concept's recommendations: databases every
// six hours, files daily, 7 daily, 4 weekly and 12 monthly snapshots.
func DefaultSetupOptions() SetupOptions {
	return SetupOptions{FilesCron: "30 2 * * *", DatabaseCron: "15 */6 * * *", Daily: 7, Weekly: 4, Monthly: 12, Compression: "auto"}
}

// SetupCreateRequest creates plans for the given apps.
type SetupCreateRequest struct {
	Apps    []string     `json:"apps"`
	Repo    string       `json:"repo"`
	Options SetupOptions `json:"options"`
	// Mode "databases" creates dump-only plans (<plan>-db).
	Mode string `json:"mode,omitempty"`
	// Groups (mode databases) puts several apps into one shared dump plan
	// per group: the apps get the group name as tag, and the plan of the
	// tag group dumps the databases of all of them (group name -> apps).
	Groups map[string][]string `json:"groups,omitempty"`
}

// SetupResult is the outcome for one app.
type SetupResult struct {
	App    string `json:"app"`
	PlanID string `json:"plan_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

const (
	sizeTimeLimit = 8 * time.Second
	maxSetupApps  = 200
)

func (s *Service) selectApps(inv *discovery.Inventory, req SetupRequest) ([]discovery.App, error) {
	var out []discovery.App
	switch req.Scope {
	case ScopeAll:
		out = append(out, inv.Apps...)
	case ScopeEasypanel:
		for _, a := range inv.Apps {
			if a.Kind == discovery.KindEasypanel || a.ID == discovery.EasypanelSystemID {
				out = append(out, a)
			}
		}
	case ScopeDatabases:
		for _, a := range inv.Apps {
			if a.Kind != discovery.KindGroup && hasRunningDatabase(a) {
				out = append(out, a)
			}
		}
	case ScopeEasypanelConfig:
		if a := inv.Find(discovery.EasypanelSystemID); a != nil {
			out = append(out, *a)
		}
	case ScopeApps:
		for _, id := range req.Apps {
			a := inv.Find(id)
			if a == nil {
				return nil, fmt.Errorf("app %q not found", id)
			}
			out = append(out, *a)
		}
	default:
		return nil, fmt.Errorf("unknown scope %q (all, easypanel, easypanel_config, databases or apps)", req.Scope)
	}
	if len(out) > maxSetupApps {
		return nil, fmt.Errorf("too many apps (%d); select fewer", len(out))
	}
	return out, nil
}

func hasRunningDatabase(a discovery.App) bool {
	for _, svc := range a.Services {
		if svc.Database != nil && svc.State != "stopped" && svc.State != "remote" {
			return true
		}
	}
	return false
}

// dumpOnly turns an app plan into a plan of only its database dumps.
func dumpOnly(p *AppPlan) {
	p.PlanID = DatabasePlanID(p.App)
	p.choose([]string{p.DumpDir})
	p.Notes = append(p.Notes, "Only the database dumps (and the recovery report); the app's files need the app plan.")
}

// addTag adds a tag to an app, keeping its other tags.
func (s *Service) addTag(ctx context.Context, appID, tag, actor string) error {
	a := s.Inventory(ctx, false).Find(appID)
	if a == nil {
		return fmt.Errorf("app %q not found", appID)
	}
	if slices.Contains(a.Tags, tag) {
		return nil
	}
	_, err := s.SetTags(ctx, appID, append(append([]string{}, a.Tags...), tag), actor)
	return err
}

// ProposeSetup previews the plans for the selected apps, with measured sizes.
func (s *Service) ProposeSetup(ctx context.Context, req SetupRequest) (*SetupProposals, error) {
	inv := s.Inventory(ctx, false)
	if !inv.Docker.Available {
		return nil, fmt.Errorf("docker is not available: %s", inv.Docker.Error)
	}
	apps, err := s.selectApps(inv, req)
	if err != nil {
		return nil, err
	}
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	plans := map[string]bool{}
	for _, p := range cfg.GetPlans() {
		plans[p.Id] = true
	}
	out := &SetupProposals{Apps: []SetupProposal{}, Complete: true, Defaults: DefaultSetupOptions()}
	if req.Scope == ScopeDatabases {
		out.Mode = ModeDatabases
		for _, a := range inv.Apps {
			if a.Kind == discovery.KindGroup {
				continue
			}
			for _, svc := range a.Services {
				for _, d := range svc.Data {
					for _, f := range d.SQLite {
						out.Notes = append(out.Notes, fmt.Sprintf("%s: SQLite database %s is a file in the app's data; it is backed up with the app plan", a.Name, f))
					}
				}
			}
		}
	}
	deadline := time.Now().Add(sizeTimeLimit * 3)
	for _, a := range apps {
		sp := SetupProposal{App: a.ID, Name: a.Name, Kind: a.Kind}
		p, err := s.ProposeAppPlan(ctx, a.ID)
		if err != nil {
			sp.Error = err.Error()
		}
		if p != nil && req.Scope == ScopeDatabases {
			dumpOnly(p)
		}
		if p != nil && len(p.Paths) > 0 {
			sp.Plan = p
			sp.ExistingPlan = plans[p.PlanID]
			sp.HasDatabases = len(p.Databases) > 0
			est := SizeEstimate{Complete: true}
			for _, it := range p.Items {
				if !it.Selected || it.Path == p.DumpDir {
					continue // dumps are made at backup time
				}
				limit := time.Until(deadline)
				if limit > sizeTimeLimit {
					limit = sizeTimeLimit
				}
				e := measure(ctx, []string{it.Path}, p.Excludes, limit)
				sp.Items = append(sp.Items, ItemSize{Path: it.Path, Label: it.Label, SizeEstimate: e})
				est.Bytes += e.Bytes
				est.Files += e.Files
				est.Complete = est.Complete && e.Complete
			}
			sort.SliceStable(sp.Items, func(i, j int) bool { return sp.Items[i].Bytes > sp.Items[j].Bytes })
			sp.Size = &est
			out.TotalBytes += est.Bytes
			out.Complete = out.Complete && est.Complete
		}
		out.Apps = append(out.Apps, sp)
	}
	for _, r := range cfg.GetRepos() {
		if st := s.storageNow(r, time.Now()); st != nil {
			if out.Storage == nil {
				out.Storage = map[string]StorageInfo{}
			}
			out.Storage[r.Id] = *st
		}
	}
	return out, nil
}

// measure sums file sizes below paths, without the excluded paths.
func measure(ctx context.Context, paths []string, excludes []string, limit time.Duration) SizeEstimate {
	est := SizeEstimate{Complete: true}
	if limit <= 0 {
		return SizeEstimate{}
	}
	stop := time.Now().Add(limit)
	errStop := errors.New("stop")
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				est.Complete = false
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if path != root && (slices.Contains(excludes, path) || slices.Contains(excludes, d.Name())) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if est.Files%1024 == 0 && (time.Now().After(stop) || ctx.Err() != nil) {
				return errStop
			}
			if d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					est.Bytes += info.Size()
					est.Files++
				}
			}
			return nil
		})
		if err != nil {
			est.Complete = false
			if errors.Is(err, errStop) {
				break
			}
		}
	}
	return est
}

var cronRe = regexp.MustCompile(`^[0-9*/,\-]+( [0-9*/,\-A-Za-z]+){4}$`)

func (o *SetupOptions) validate() error {
	if !cronRe.MatchString(o.FilesCron) || !cronRe.MatchString(o.DatabaseCron) {
		return errors.New("schedules must be cron expressions with five fields")
	}
	for _, n := range []int32{o.Daily, o.Weekly, o.Monthly, o.Yearly} {
		if n < 0 || n > 1000 {
			return errors.New("retention counts must be between 0 and 1000")
		}
	}
	for _, e := range o.Excludes {
		if strings.TrimSpace(e) == "" || strings.ContainsAny(e, "\n\r\x00") {
			return errors.New("invalid exclude pattern")
		}
	}
	if o.LimitUploadKiB < 0 {
		return errors.New("the upload limit must not be negative")
	}
	switch o.Compression {
	case "", "auto", "max", "off":
	default:
		return errors.New("compression must be auto, max or off")
	}
	return nil
}

// CreateSetup creates one plan per app with the shared options. Apps that
// fail (e.g. the plan already exists) are reported and do not stop the rest.
func (s *Service) CreateSetup(ctx context.Context, req SetupCreateRequest, actor string) ([]SetupResult, error) {
	o := req.Options
	def := DefaultSetupOptions()
	if o.FilesCron == "" {
		o.FilesCron = def.FilesCron
	}
	if o.DatabaseCron == "" {
		o.DatabaseCron = def.DatabaseCron
	}
	if o.Daily+o.Weekly+o.Monthly+o.Yearly == 0 {
		o.Daily, o.Weekly, o.Monthly, o.Yearly = def.Daily, def.Weekly, def.Monthly, def.Yearly
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	grouped := 0
	for _, apps := range req.Groups {
		grouped += len(apps)
	}
	if len(req.Apps)+grouped == 0 || len(req.Apps)+grouped > maxSetupApps {
		return nil, errors.New("select between 1 and 200 apps")
	}
	if len(req.Groups) > 0 && req.Mode != ModeDatabases {
		return nil, errors.New("groups are only supported for database plans")
	}
	// Shared plans: tag the apps, then treat each tag group like an app.
	apps := append([]string{}, req.Apps...)
	var groupErrs []SetupResult
	for name, members := range req.Groups {
		tag := discovery.NormalizeTag(name)
		if tag == "" || len(members) == 0 {
			groupErrs = append(groupErrs, SetupResult{App: discovery.GroupID(name), Error: "invalid group name or no database in it (letters, digits, '-', '_', '.')"})
			continue
		}
		var err error
		for _, id := range members {
			if err = s.addTag(ctx, id, tag, actor); err != nil {
				break
			}
		}
		if err != nil {
			groupErrs = append(groupErrs, SetupResult{App: discovery.GroupID(tag), Error: err.Error()})
			continue
		}
		apps = append(apps, discovery.GroupID(tag))
	}
	if len(req.Groups) > 0 {
		s.Inventory(ctx, true) // the new tag groups
	}
	ret := &v1.RetentionPolicy{Policy: &v1.RetentionPolicy_PolicyTimeBucketed{PolicyTimeBucketed: &v1.RetentionPolicy_TimeBucketedCounts{
		Daily: o.Daily, Weekly: o.Weekly, Monthly: o.Monthly, Yearly: o.Yearly}}}
	var flags []string
	if o.LimitUploadKiB > 0 {
		flags = append(flags, fmt.Sprintf("--limit-upload=%d", o.LimitUploadKiB))
	}
	if o.Compression == "max" || o.Compression == "off" {
		flags = append(flags, "--compression="+o.Compression)
	}
	out := groupErrs
	for _, app := range apps {
		p, err := s.ProposeAppPlan(ctx, app)
		cron := o.FilesCron
		if err == nil && len(p.Databases) > 0 {
			cron = o.DatabaseCron
		}
		res := SetupResult{App: app}
		var planID string
		var paths []string
		if err == nil && req.Mode == ModeDatabases {
			if len(p.Databases) == 0 {
				err = errors.New("no running database to dump")
			} else {
				planID, paths = DatabasePlanID(app), []string{p.DumpDir}
			}
		} else if req.Mode != "" && req.Mode != ModeDatabases {
			err = fmt.Errorf("unknown mode %q", req.Mode)
		}
		if err == nil {
			p, err = s.CreateAppPlan(ctx, CreateAppPlanRequest{
				AppID: app, RepoID: req.Repo, PlanID: planID, Paths: paths,
				Schedule:  &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: cron}, Clock: v1.Schedule_CLOCK_LOCAL},
				Retention: ret, Excludes: o.Excludes, BackupFlags: flags,
			}, actor)
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.PlanID = p.PlanID
		}
		out = append(out, res)
	}
	return out, nil
}
