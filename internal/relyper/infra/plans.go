package infra

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
	"google.golang.org/protobuf/proto"
)

// AppPlan is a proposed or created backup plan for an app.
type AppPlan struct {
	App         string   `json:"app"`
	PlanID      string   `json:"plan_id"`
	RepoID      string   `json:"repo_id,omitempty"`
	Paths       []string `json:"paths"`
	DumpDir     string   `json:"dump_dir,omitempty"`
	HookCommand string   `json:"hook_command,omitempty"`
	Databases   []string `json:"databases,omitempty"`
	Skipped     []string `json:"skipped,omitempty"` // data that cannot be backed up from here, with the reason
	Warnings    []string `json:"warnings,omitempty"`
	// Items is everything the plan can back up, with a default choice.
	// Paths holds the chosen ones; Excludes the paths of items that are
	// not chosen but lie inside a chosen directory.
	Items    []PlanItem `json:"items"`
	Excludes []string   `json:"excludes,omitempty"`
}

// PlanItem is one path an app plan can back up.
type PlanItem struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // config, data, easypanel, dumps, inventory
	Service string `json:"service,omitempty"`
	Label   string `json:"label"`
	Class   string `json:"class,omitempty"`
	// Selected is the default choice; Required items cannot be left out.
	Selected bool   `json:"selected"`
	Required bool   `json:"required,omitempty"`
	Note     string `json:"note,omitempty"`
}

var planIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// DefaultPlanID is the plan ID suggested for an app.
func DefaultPlanID(appID string) string {
	id := "app-" + discovery.Slug(appID)
	id = strings.NewReplacer("compose-", "", "swarm_stack-", "stack-", "easypanel-", "ep-", "container-", "").Replace(id)
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

// ProposeAppPlan shows what a plan for the app would contain.
func (s *Service) ProposeAppPlan(ctx context.Context, appID string) (*AppPlan, error) {
	if !discovery.ValidAppID(appID) {
		return nil, fmt.Errorf("invalid app ID %q", appID)
	}
	inv := s.Inventory(ctx, false)
	if !inv.Docker.Available {
		return nil, fmt.Errorf("docker is not available: %s", inv.Docker.Error)
	}
	app := inv.Find(appID)
	if app == nil {
		return nil, fmt.Errorf("app %q not found; list apps with the discovery first", appID)
	}
	p := &AppPlan{App: appID, PlanID: DefaultPlanID(appID)}
	seen := map[string]bool{}
	add := func(it PlanItem) {
		if it.Path == "" || seen[it.Path] {
			return
		}
		seen[it.Path] = true
		p.Items = append(p.Items, it)
	}
	// Configuration first: the whole compose project directory (compose
	// files, .env, override files), else the single compose files.
	for _, d := range app.ConfigDirs {
		if d.BackupPath == "" {
			p.Skipped = append(p.Skipped, "compose directory "+d.HostPath+" (with .env) is not mounted into Relyper Backup")
			continue
		}
		add(PlanItem{Path: d.BackupPath, Kind: "config", Label: "compose directory (compose files, .env, other files)", Selected: true})
	}
	for _, f := range app.Deployment {
		switch {
		case f.BackupPath == "":
			p.Skipped = append(p.Skipped, "deployment definition "+f.HostPath+" is not mounted into Relyper Backup")
		case !underAny(f.BackupPath, p.Items):
			add(PlanItem{Path: f.BackupPath, Kind: "config", Label: "deployment definition", Selected: true})
		}
	}
	// Easypanel keeps domains, Traefik routing, certificates and its own
	// settings in the directories mounted into its container. The projects
	// directory is left out: each app plan takes only its own project data.
	if inv.Docker.Easypanel && isEasypanelApp(app) {
		found := false
		for _, d := range inv.Docker.EasypanelConfig {
			if d.BackupPath == "" {
				continue
			}
			found = true
			for _, e := range easypanelEntries(d.BackupPath) {
				add(PlanItem{Path: e, Kind: "easypanel", Label: "Easypanel settings (domains, Traefik, certificates, panel data)", Selected: true})
			}
		}
		if !found {
			p.Skipped = append(p.Skipped, "Easypanel settings (/etc/easypanel: domains, Traefik, certificates) are not mounted into Relyper Backup; mount /etc/easypanel read-only")
		}
	}
	for _, svc := range app.Services {
		if svc.State == "remote" {
			p.Skipped = append(p.Skipped, svc.Name+": runs on another Swarm node")
			continue
		}
		if svc.Database != nil {
			p.Databases = append(p.Databases, svc.Name+" ("+svc.Database.Engine+")")
			if svc.State == "stopped" {
				p.Warnings = append(p.Warnings, svc.Name+" is stopped; while it stays stopped its files are backed up instead of a dump")
			} else if svc.State != "running" {
				p.Warnings = append(p.Warnings, svc.Name+" is not running; the dump before each backup will fail until it runs")
			}
		}
		if svc.UnsupportedDatabase != "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s (%s) has no dump support; its files are backed up as files, which is not consistent while it runs", svc.Name, svc.UnsupportedDatabase))
		}
		for _, d := range svc.Data {
			name := d.Name
			if name == "" {
				name = d.HostPath
			}
			label := fmt.Sprintf("%s %s → %s", d.Type, name, d.ContainerPath)
			switch {
			case d.Type == "tmpfs":
				continue // in memory only
			case d.DatabaseFiles:
				continue // the dump protects them
			case d.BackupPath == "":
				p.Skipped = append(p.Skipped, fmt.Sprintf("%s: %s is not mounted into Relyper Backup", svc.Name, d.HostPath))
			case !discovery.Backed(d.Class):
				add(PlanItem{Path: d.BackupPath, Kind: "data", Service: svc.Name, Label: label, Class: d.Class,
					Note: d.Class + " data is usually rebuilt by the app; choose it if you need it"})
			default:
				add(PlanItem{Path: d.BackupPath, Kind: "data", Service: svc.Name, Label: label, Class: d.Class, Selected: true})
			}
		}
	}
	if len(p.Databases) > 0 {
		if strings.ContainsAny(s.s.DumpDir, `'"\$`+"`") {
			return nil, errors.New("the dump directory contains characters that are not allowed")
		}
		p.DumpDir = dbdump.Dir(s.s.DumpDir, appID)
		p.HookCommand = fmt.Sprintf("%s dump --dir '%s' '%s'", s.s.DumpCommand, s.s.DumpDir, appID)
		p.Items = append([]PlanItem{{Path: p.DumpDir, Kind: "dumps", Label: "database dumps (made right before every backup)", Selected: true, Required: true}}, p.Items...)
	}
	if len(p.Items) == 0 {
		p.Paths = nil
		return p, errors.New("nothing of this app can be backed up from here: mount its data into Relyper Backup first")
	}
	// The recovery report travels with every app snapshot.
	p.Items = append(p.Items, PlanItem{Path: s.InventoryDir(), Kind: "inventory", Label: "recovery report (services, images, domains, ports, restore order)", Selected: true, Required: true})
	p.choose(nil)
	return p, nil
}

// choose sets Paths and Excludes from the selected items. selected nil keeps
// the defaults; otherwise it names the chosen item paths.
func (p *AppPlan) choose(selected []string) error {
	if selected != nil {
		want := map[string]bool{}
		for _, x := range selected {
			want[x] = true
		}
		known := map[string]bool{}
		for i := range p.Items {
			it := &p.Items[i]
			known[it.Path] = true
			it.Selected = it.Required || want[it.Path]
		}
		for _, x := range selected {
			if !known[x] {
				return fmt.Errorf("%s is not part of this app; choose from the proposed items", x)
			}
		}
	}
	p.Paths, p.Excludes = nil, nil
	for _, it := range p.Items {
		if it.Selected {
			p.Paths = append(p.Paths, it.Path)
		}
	}
	for _, it := range p.Items {
		if !it.Selected && underAnyPath(it.Path, p.Paths) {
			p.Excludes = append(p.Excludes, it.Path)
		}
	}
	return nil
}

func underAnyPath(x string, dirs []string) bool {
	for _, d := range dirs {
		if x != d && strings.HasPrefix(x, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}

func underAny(x string, items []PlanItem) bool {
	for _, it := range items {
		if x == it.Path || strings.HasPrefix(x, strings.TrimSuffix(it.Path, "/")+"/") {
			return true
		}
	}
	return false
}

func isEasypanelApp(app *discovery.App) bool {
	if app.Kind == discovery.KindEasypanel {
		return true
	}
	for _, d := range app.ConfigDirs {
		if strings.HasPrefix(d.HostPath, "/etc/easypanel/projects/") {
			return true
		}
	}
	for _, d := range app.Deployment {
		if strings.HasPrefix(d.HostPath, "/etc/easypanel/projects/") {
			return true
		}
	}
	return false
}

// easypanelEntries lists the entries of an Easypanel directory without the
// projects directory (app data is in the app plans).
func easypanelEntries(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Name() == "projects" {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// CreateAppPlanRequest creates a plan for an app.
type CreateAppPlanRequest struct {
	AppID     string
	RepoID    string
	PlanID    string              // optional
	Schedule  *v1.Schedule        // optional, default daily 02:30 local time
	Retention *v1.RetentionPolicy // optional, default 7 daily, 4 weekly, 6 monthly
	// Excludes (glob patterns) and BackupFlags (e.g. --limit-upload) are
	// optional.
	Excludes    []string
	BackupFlags []string
	// Paths chooses among the proposed items (nil: the defaults).
	Paths []string
}

// DefaultSchedule and DefaultRetention for app plans.
func DefaultSchedule() *v1.Schedule {
	return &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "30 2 * * *"}, Clock: v1.Schedule_CLOCK_LOCAL}
}

func DefaultRetention() *v1.RetentionPolicy {
	return &v1.RetentionPolicy{Policy: &v1.RetentionPolicy_PolicyTimeBucketed{PolicyTimeBucketed: &v1.RetentionPolicy_TimeBucketedCounts{Daily: 7, Weekly: 4, Monthly: 6}}}
}

// CreateAppPlan creates the backup plan for an app: its data paths and, if
// it has databases, the dump directory plus a hook that dumps the databases
// right before every backup and fails the backup if a dump fails.
func (s *Service) CreateAppPlan(ctx context.Context, req CreateAppPlanRequest, actor string) (*AppPlan, error) {
	p, err := s.ProposeAppPlan(ctx, req.AppID)
	if err != nil {
		return nil, err
	}
	if req.PlanID != "" {
		p.PlanID = req.PlanID
	}
	if err := p.choose(req.Paths); err != nil {
		return nil, err
	}
	if !planIDRe.MatchString(p.PlanID) {
		return nil, fmt.Errorf("invalid plan ID %q", p.PlanID)
	}
	p.RepoID = req.RepoID
	sched, ret := req.Schedule, req.Retention
	if sched == nil {
		sched = DefaultSchedule()
	}
	if ret == nil {
		ret = DefaultRetention()
	}
	// The inventory directory must exist before the first backup; the
	// report in it is refreshed hourly.
	if _, err := os.Stat(filepath.Join(s.InventoryDir(), InventoryReportName)); err != nil {
		if err := s.WriteInventory(ctx); err != nil {
			return nil, fmt.Errorf("writing the recovery inventory: %w", err)
		}
	}
	plan := &v1.Plan{Id: p.PlanID, Repo: req.RepoID, Paths: p.Paths, Schedule: sched, Retention: ret, Excludes: append(append([]string{}, p.Excludes...), req.Excludes...), BackupFlags: req.BackupFlags}
	if p.HookCommand != "" {
		if err := os.MkdirAll(p.DumpDir, 0o700); err != nil {
			return nil, fmt.Errorf("creating the dump directory: %w", err)
		}
		plan.Hooks = []*v1.Hook{{
			Conditions: []v1.Hook_Condition{v1.Hook_CONDITION_SNAPSHOT_START},
			OnError:    v1.Hook_ON_ERROR_FATAL,
			Action:     &v1.Hook_ActionCommand{ActionCommand: &v1.Hook_Command{Command: p.HookCommand}},
		}}
	}
	err = s.d.Config.Transform(func(cfg *v1.Config) (*v1.Config, error) {
		for _, x := range cfg.Plans {
			if x.Id == plan.Id {
				return nil, fmt.Errorf("a plan with ID %q already exists", plan.Id)
			}
		}
		found := false
		for _, r := range cfg.Repos {
			if r.Id == req.RepoID {
				found = true
				if r.OriginInstanceId != "" {
					return nil, errors.New("the repository is managed by another instance")
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("repository %q not found", req.RepoID)
		}
		cfg.Plans = append(cfg.Plans, proto.Clone(plan).(*v1.Plan))
		cfg.Modno++
		return cfg, nil
	})
	if err != nil {
		s.record(actor, "app_plan.create", "error", req.AppID+": "+err.Error())
		return nil, err
	}
	s.record(actor, "app_plan.create", "ok", fmt.Sprintf("%s -> plan %s, repository %s, %d paths", req.AppID, plan.Id, req.RepoID, len(plan.Paths)))
	return p, nil
}

// DumpNow dumps the databases of an app into the dump directory, outside a
// backup (e.g. to test that dumps work).
func (s *Service) DumpNow(ctx context.Context, appID, actor string) (*dbdump.Manifest, error) {
	if s.docker == nil {
		return nil, errors.New("docker is not available")
	}
	inv := s.Inventory(ctx, true)
	app := inv.Find(appID)
	if app == nil {
		return nil, fmt.Errorf("app %q not found", appID)
	}
	if !s.lock("dump:" + appID) {
		return nil, errors.New("a dump of this app is already running")
	}
	defer s.unlock("dump:" + appID)
	m, err := dbdump.DumpApp(ctx, s.docker, app, s.s.DumpDir, nil)
	if err != nil {
		s.record(actor, "dump", "error", appID+": "+err.Error())
		return nil, err
	}
	s.record(actor, "dump", "ok", fmt.Sprintf("%s: %d dumps", appID, len(m.Dumps)))
	return m, nil
}
