package infra

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	for _, svc := range app.Services {
		if svc.State == "remote" {
			p.Skipped = append(p.Skipped, svc.Name+": runs on another Swarm node")
			continue
		}
		if svc.Database != nil {
			p.Databases = append(p.Databases, svc.Name+" ("+svc.Database.Engine+")")
			if svc.State != "running" {
				p.Warnings = append(p.Warnings, svc.Name+" is not running; the dump before each backup will fail until it runs")
			}
		}
		for _, d := range svc.Data {
			switch {
			case d.DatabaseFiles:
				continue
			case d.BackupPath == "":
				p.Skipped = append(p.Skipped, fmt.Sprintf("%s: %s is not mounted into Relyper Backup", svc.Name, d.HostPath))
			case !seen[d.BackupPath]:
				seen[d.BackupPath] = true
				p.Paths = append(p.Paths, d.BackupPath)
			}
		}
	}
	if len(p.Databases) > 0 {
		if strings.ContainsAny(s.s.DumpDir, `'"\$`+"`") {
			return nil, errors.New("the dump directory contains characters that are not allowed")
		}
		p.DumpDir = dbdump.Dir(s.s.DumpDir, appID)
		p.Paths = append([]string{p.DumpDir}, p.Paths...)
		p.HookCommand = fmt.Sprintf("%s dump --dir '%s' '%s'", s.s.DumpCommand, s.s.DumpDir, appID)
	}
	if len(p.Paths) == 0 {
		return p, errors.New("nothing of this app can be backed up from here: mount its data into Relyper Backup first")
	}
	return p, nil
}

// CreateAppPlanRequest creates a plan for an app.
type CreateAppPlanRequest struct {
	AppID     string
	RepoID    string
	PlanID    string              // optional
	Schedule  *v1.Schedule        // optional, default daily 02:30 local time
	Retention *v1.RetentionPolicy // optional, default 7 daily, 4 weekly, 6 monthly
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
	plan := &v1.Plan{Id: p.PlanID, Repo: req.RepoID, Paths: p.Paths, Schedule: sched, Retention: ret}
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
