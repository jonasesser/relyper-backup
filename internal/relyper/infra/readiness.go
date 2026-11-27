package infra

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
)

// Readiness levels.
const (
	Ready    = "ready"
	AtRisk   = "at_risk"
	NotReady = "not_ready"
)

// Check is one readiness criterion.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warning, failed
	Detail string `json:"detail"`
}

// AppReadiness is the recovery readiness of an app.
type AppReadiness struct {
	App    string  `json:"app"`
	Name   string  `json:"name"`
	Level  string  `json:"level"`
	Checks []Check `json:"checks"`
}

// RepoReadiness describes a repository.
type RepoReadiness struct {
	Repo      string `json:"repo"`
	Location  string `json:"location"` // URI without credentials
	Type      string `json:"type"`
	Offsite   bool   `json:"offsite"`
	LastCheck string `json:"last_check,omitempty"`
	CheckOK   bool   `json:"check_ok"`
	Detail    string `json:"detail,omitempty"`
}

// ReadinessReport is the overall recovery readiness.
type ReadinessReport struct {
	GeneratedAt string          `json:"generated_at"`
	Level       string          `json:"level"`
	Apps        []AppReadiness  `json:"apps"`
	Repos       []RepoReadiness `json:"repos"`
	Coverage    *CoverageReport `json:"coverage"`
}

const (
	verifyMaxAge = 14 * 24 * time.Hour
	checkMaxAge  = 35 * 24 * time.Hour
)

// RedactURI removes user info (credentials) from a repository URI such as
// rest:https://user:password@host/.
func RedactURI(uri string) string {
	return userInfoRe.ReplaceAllString(uri, "://")
}

var userInfoRe = regexp.MustCompile(`://[^/@\s]+@`)

func repoType(uri string) string {
	if strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "local:") {
		return "local"
	}
	if i := strings.Index(uri, ":"); i > 0 {
		return uri[:i]
	}
	return "local"
}

func (s *Service) repoReadiness(cfg *v1.Config, r *v1.Repo, now time.Time) RepoReadiness {
	rr := RepoReadiness{Repo: r.Id, Location: RedactURI(r.Uri), Type: repoType(r.Uri)}
	rr.Offsite = rr.Type != "local"
	if s.d.OpLog == nil || r.Guid == "" {
		return rr
	}
	stop := errors.New("stop")
	_ = s.d.OpLog.Query(oplog.Query{}.SetInstanceID(cfg.Instance).SetRepoGUID(r.Guid).SetReversed(true), func(op *v1.Operation) error {
		if op.GetOperationCheck() == nil || op.Status == v1.OperationStatus_STATUS_PENDING || op.Status == v1.OperationStatus_STATUS_INPROGRESS {
			return nil
		}
		rr.LastCheck = fmtTime(time.UnixMilli(op.UnixTimeEndMs))
		rr.CheckOK = op.Status == v1.OperationStatus_STATUS_SUCCESS
		if !rr.CheckOK {
			rr.Detail = firstLine(op.DisplayMessage)
		}
		return stop
	})
	return rr
}

// Readiness evaluates every app.
func (s *Service) Readiness(ctx context.Context) (*ReadinessReport, error) {
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	cov, err := s.Coverage(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rep := &ReadinessReport{GeneratedAt: fmtTime(now), Coverage: cov, Level: Ready}
	repos := map[string]RepoReadiness{}
	for _, r := range cfg.Repos {
		rr := s.repoReadiness(cfg, r, now)
		repos[r.Id] = rr
		rep.Repos = append(rep.Repos, rr)
	}
	inv := s.Inventory(ctx, false)
	for _, ac := range cov.Apps {
		ar := AppReadiness{App: ac.App, Name: ac.Name}
		add := func(name, status, detail string) { ar.Checks = append(ar.Checks, Check{name, status, detail}) }
		// 1. Coverage.
		switch ac.State {
		case StateProtected, StateViaDump, StateRemote:
			add("backup", "ok", fmt.Sprintf("all data is in a snapshot younger than %d hours", cov.MaxAgeHours))
		case StateStale, StateNotInSnapshot, StateDisabled:
			add("backup", "warning", "some data is not in a recent snapshot ("+ac.State+")")
		default:
			add("backup", "failed", "some data is not protected ("+ac.State+")")
		}
		// 2. Database verification.
		app := inv.Find(ac.App)
		hasDB := false
		if app != nil {
			for _, svc := range app.Services {
				if svc.Database != nil {
					hasDB = true
				}
			}
		}
		if hasDB {
			v := s.store.verifications(ac.App)
			switch {
			case len(v) == 0:
				add("database_restore", "failed", "the database dumps were never restored as a test")
			case !v[0].OK:
				add("database_restore", "failed", "the last database restore test failed: "+verifySummary(&v[0]))
			case olderThan(v[0].Time, now, verifyMaxAge):
				add("database_restore", "warning", "the last successful database restore test is older than 14 days ("+v[0].Time+")")
			default:
				add("database_restore", "ok", verifySummary(&v[0])+" ("+v[0].Time+")")
			}
		}
		// 3. File restore test and repository health of the app's plans.
		for _, planID := range ac.Plans {
			t := s.store.restoreTests(planID)
			switch {
			case len(t) == 0:
				add("restore_test:"+planID, "warning", "no file restore test yet")
			case !t[0].OK:
				add("restore_test:"+planID, "failed", "the last restore test failed: "+restoreSummary(&t[0]))
			case olderThan(t[0].Time, now, verifyMaxAge):
				add("restore_test:"+planID, "warning", "the last restore test is older than 14 days")
			default:
				add("restore_test:"+planID, "ok", restoreSummary(&t[0]))
			}
			if p := s.plan(cfg, planID); p != nil {
				rr := repos[p.Repo]
				switch {
				case rr.LastCheck == "":
					add("repository_check:"+p.Repo, "warning", "the repository was never checked (restic check)")
				case !rr.CheckOK:
					add("repository_check:"+p.Repo, "failed", "the last repository check failed: "+rr.Detail)
				case olderThan(rr.LastCheck, now, checkMaxAge):
					add("repository_check:"+p.Repo, "warning", "the last repository check is older than 35 days")
				default:
					add("repository_check:"+p.Repo, "ok", "last check "+rr.LastCheck)
				}
				if !rr.Offsite {
					add("offsite:"+p.Repo, "warning", "the repository is on a local path; a copy on other hardware or at another site is missing")
				}
			}
		}
		ar.Level = Ready
		for _, c := range ar.Checks {
			if c.Status == "failed" {
				ar.Level = NotReady
			} else if c.Status == "warning" && ar.Level == Ready {
				ar.Level = AtRisk
			}
		}
		rep.Apps = append(rep.Apps, ar)
		if ar.Level == NotReady || (ar.Level == AtRisk && rep.Level == Ready) {
			rep.Level = ar.Level
		}
	}
	return rep, nil
}

func olderThan(rfc string, now time.Time, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, rfc)
	return err != nil || now.Sub(t) > d
}

// RecoveryReport renders the disaster recovery inventory as Markdown: what
// exists, where it is backed up, how fresh and tested it is, and the steps to
// restore it with the plain restic CLI. It contains no passwords or keys.
func (s *Service) RecoveryReport(ctx context.Context) (string, error) {
	cfg, err := s.cfg()
	if err != nil {
		return "", err
	}
	rd, err := s.Readiness(ctx)
	if err != nil {
		return "", err
	}
	inv := s.Inventory(ctx, false)
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Disaster recovery report\n\n")
	w("Generated %s by Relyper Backup (instance `%s`). Overall readiness: **%s**.\n\n", rd.GeneratedAt, cfg.Instance, rd.Level)
	w("This report contains no passwords. To restore you need, in addition: the repository passwords and storage credentials (keep them in a password manager, not only in Relyper Backup), and for SFTP the private key.\n\n")

	w("## Repositories\n\n| Repository | Location | Type | Off-site | Last check |\n|---|---|---|---|---|\n")
	for _, r := range rd.Repos {
		check := r.LastCheck
		if check == "" {
			check = "never"
		} else if !r.CheckOK {
			check += " (failed)"
		}
		w("| `%s` | `%s` | %s | %v | %s |\n", r.Repo, r.Location, r.Type, r.Offsite, check)
	}
	w("\n")
	for _, r := range cfg.Repos {
		var envNames []string
		for _, e := range r.Env {
			k, _, _ := strings.Cut(e, "=")
			envNames = append(envNames, k)
		}
		if len(envNames) > 0 {
			sort.Strings(envNames)
			w("- `%s` needs these environment variables: %s\n", r.Id, "`"+strings.Join(envNames, "`, `")+"`")
		}
	}

	w("\n## Docker host\n\n")
	if inv.Docker.Available {
		w("Docker %s on `%s`, Swarm: %v, Easypanel: %v.\n\n", inv.Docker.Version, inv.Docker.Name, inv.Docker.Swarm, inv.Docker.Easypanel)
	} else {
		w("Docker was not reachable (%s); the app inventory is missing.\n\n", inv.Docker.Error)
	}

	w("## Applications\n\n")
	levels := map[string]string{}
	for _, a := range rd.Apps {
		levels[a.App] = a.Level
	}
	for _, ac := range rd.Coverage.Apps {
		w("### %s (`%s`) – %s\n\n", ac.Name, ac.App, levels[ac.App])
		if app := inv.Find(ac.App); app != nil {
			w("| Service | Image | Database |\n|---|---|---|\n")
			for _, svc := range app.Services {
				db := ""
				if svc.Database != nil {
					db = svc.Database.Engine
				}
				w("| %s | `%s` | %s |\n", svc.Name, svc.Image, db)
			}
			w("\n")
		}
		w("| Item | Path | State | Plan | Last snapshot |\n|---|---|---|---|---|\n")
		for _, it := range ac.Items {
			w("| %s %s (%s) | `%s` | %s | %s | %s %s |\n", it.Service, it.Name, it.Kind, it.Path, it.State, it.Plan, it.LastSnapshot, short(it.SnapshotID))
		}
		for _, a := range rd.Apps {
			if a.App != ac.App {
				continue
			}
			w("\nChecks:\n\n")
			for _, c := range a.Checks {
				w("- %s **%s**: %s\n", c.Name, c.Status, c.Detail)
			}
		}
		for _, n := range ac.Notes {
			w("\n> %s\n", n)
		}
		w("\n")
	}

	w("## Restore runbook\n\n")
	w("Everything below works with the plain restic CLI, without Relyper Backup.\n\n")
	w("1. Install restic and set `RESTIC_REPOSITORY` to the location above, `RESTIC_PASSWORD` to the repository password and the storage variables listed above.\n")
	w("2. List snapshots of a plan: `restic snapshots --tag plan:<plan id>`.\n")
	w("3. Restore files into an empty directory: `restic restore <snapshot>:<path> --target /restore/<name>`. Copy them back only after checking them; stop the app while you do.\n")
	w("4. Database dumps of an app are in the snapshot under the dump directory (`%s/<app>`), with `%s` describing engine, image and expected content.\n", s.s.DumpDir, dbdump.ManifestName)
	w("   - PostgreSQL (`*.sql` from pg_dumpall): start the same image, then `psql -U <user> -d postgres -f dump.sql`.\n")
	w("   - MySQL/MariaDB (`*.sql`): `mysql -u root -p < dump.sql` (or `mariadb`).\n")
	w("   - MongoDB (`*.archive.gz`): `mongorestore --archive=dump.archive.gz --gzip --drop`.\n")
	w("   - Redis (`*.rdb`): stop Redis, copy the file to `/data/dump.rdb`, start Redis.\n")
	w("5. Recreate the containers with their original images (listed above) and compose files, then restore volumes and databases.\n")
	if len(rd.Coverage.PlansWithout) > 0 {
		w("\n## Plans not linked to a discovered app\n\n")
		for _, p := range rd.Coverage.PlansWithout {
			w("- `%s`\n", p)
		}
	}
	return b.String(), nil
}
