// Package portal is Relyper-owned: the customer portal API at
// /relyper/portal/api/ and the operator API for tenants at /relyper/tenants/.
// Design and isolation rules: docs/CONCEPT-MULTITENANT.md.
package portal

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
)

// Plan states shown to customers.
const (
	StateOK       = "ok"
	StateWarning  = "warning"
	StateFailing  = "failing"
	StateStale    = "stale"
	StateNever    = "never_run"
	StateDisabled = "disabled"
	StateRunning  = "running"
)

// RunView is one backup run.
type RunView struct {
	OperationID  int64  `json:"operationId"`
	Status       string `json:"status"`
	StartedAt    int64  `json:"startedAt"` // unix ms
	EndedAt      int64  `json:"endedAt,omitempty"`
	SnapshotID   string `json:"snapshotId,omitempty"`
	BytesAdded   int64  `json:"bytesAdded,omitempty"`
	BytesTotal   int64  `json:"bytesTotal,omitempty"`
	FilesNew     int64  `json:"filesNew,omitempty"`
	FilesChanged int64  `json:"filesChanged,omitempty"`
	Errors       int    `json:"errors,omitempty"` // unreadable files
	Message      string `json:"message,omitempty"`
}

// PlanView is a plan as a customer sees it.
type PlanView struct {
	ID            string   `json:"id"`
	Repo          string   `json:"repo"`
	Paths         []string `json:"paths"`
	Schedule      string   `json:"schedule"`
	Retention     string   `json:"retention,omitempty"`
	State         string   `json:"state"`
	OwnStorage    bool     `json:"ownStorage"`
	LastRun       *RunView `json:"lastRun,omitempty"`
	LastSuccessAt int64    `json:"lastSuccessAt,omitempty"`
	Snapshots     int      `json:"snapshots"`
	// App is set for plans that back up one user of an app ("budget");
	// their snapshots can be restored from the portal.
	App     string `json:"app,omitempty"`
	AppUser string `json:"appUser,omitempty"`
}

// KitView describes a recovery kit without the key.
type KitView struct {
	KeyID     string    `json:"keyId"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy"`
	Escrowed  bool      `json:"escrowed"`
}

// RepoView is a repository as a customer sees it.
type RepoView struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Location string   `json:"location"`
	Own      bool     `json:"own"`
	Kit      *KitView `json:"kit,omitempty"`
}

// Problem is something the customer should know about.
type Problem struct {
	Plan    string `json:"plan"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// Overview is the start page of the portal.
type Overview struct {
	Tenant   TenantInfo `json:"tenant"`
	Plans    []PlanView `json:"plans"`
	Repos    []RepoView `json:"repos"`
	Problems []Problem  `json:"problems"`
}

// TenantInfo is the tenant as members see it.
type TenantInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Escrow bool   `json:"escrow"`
}

// SnapshotView is one snapshot.
type SnapshotView struct {
	ID       string   `json:"id"`
	Time     int64    `json:"time"` // unix ms
	Hostname string   `json:"hostname,omitempty"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags,omitempty"`
	Bytes    int64    `json:"bytes,omitempty"`
	Files    int64    `json:"files,omitempty"`
}

// errNotFound is returned for foreign and unknown objects alike.
var errNotFound = errors.New("not found")

// scope is the part of the configuration a tenant may see. A plan is
// visible only when its repository is assigned to the same tenant.
type scope struct {
	cfg   *v1.Config
	t     *tenant.Tenant
	plans []*v1.Plan
	repos []*v1.Repo
}

func newScope(cfg *v1.Config, t *tenant.Tenant) *scope {
	s := &scope{cfg: cfg, t: t}
	for _, r := range cfg.GetRepos() {
		if t.HasRepo(r.GetId()) {
			s.repos = append(s.repos, r)
		}
	}
	for _, p := range cfg.GetPlans() {
		if t.HasPlan(p.GetId()) && t.HasRepo(p.GetRepo()) && s.repo(p.GetRepo()) != nil {
			s.plans = append(s.plans, p)
		}
	}
	return s
}

func (s *scope) plan(id string) *v1.Plan {
	for _, p := range s.plans {
		if p.GetId() == id {
			return p
		}
	}
	return nil
}

func (s *scope) repo(id string) *v1.Repo {
	for _, r := range s.repos {
		if r.GetId() == id {
			return r
		}
	}
	return nil
}

// planQuery selects the operations of an assigned plan in its assigned
// repository on this instance.
func (s *scope) planQuery(p *v1.Plan) oplog.Query {
	q := oplog.Query{}.SetInstanceID(s.cfg.GetInstance()).SetPlanID(p.GetId())
	if r := s.repo(p.GetRepo()); r != nil && r.GetGuid() != "" {
		q = q.SetRepoGUID(r.GetGuid())
	}
	return q
}

// opBelongs double-checks an operation against the scope.
func (s *scope) opBelongs(p *v1.Plan, op *v1.Operation) bool {
	if op.GetPlanId() != p.GetId() || op.GetInstanceId() != s.cfg.GetInstance() {
		return false
	}
	r := s.repo(p.GetRepo())
	if r == nil {
		return false
	}
	if r.GetGuid() != "" && op.GetRepoGuid() != "" {
		return op.GetRepoGuid() == r.GetGuid()
	}
	return op.GetRepoId() == r.GetId()
}

var userInfo = regexp.MustCompile(`://[^/\s@]+@`)

// RedactURI removes user info (rest:https://user:pass@host/…) from a
// repository URI.
func RedactURI(uri string) string {
	return userInfo.ReplaceAllString(uri, "://")
}

// RepoType returns the restic backend of a URI.
func RepoType(uri string) string {
	if i := strings.Index(uri, ":"); i > 0 && !strings.HasPrefix(uri, "/") {
		t := uri[:i]
		if len(t) > 1 { // not a Windows drive letter
			return t
		}
	}
	return "local"
}

// Scrubber removes secrets of the configuration from text.
type Scrubber struct{ secrets []string }

// NewScrubber collects repository passwords and environment values.
func NewScrubber(cfg *v1.Config) *Scrubber {
	sc := &Scrubber{}
	add := func(v string) {
		if v = strings.TrimSpace(v); len(v) >= 4 {
			sc.secrets = append(sc.secrets, v)
		}
	}
	for _, r := range cfg.GetRepos() {
		add(r.GetPassword())
		for _, e := range r.GetEnv() {
			if i := strings.Index(e, "="); i >= 0 {
				add(e[i+1:])
			}
		}
	}
	sort.Slice(sc.secrets, func(i, j int) bool { return len(sc.secrets[i]) > len(sc.secrets[j]) })
	return sc
}

// Scrub returns text without secrets and URI user info, cut to 500 bytes.
func (sc *Scrubber) Scrub(s string) string {
	for _, v := range sc.secrets {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	s = userInfo.ReplaceAllString(s, "://[REDACTED]@")
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

func scheduleText(s *v1.Schedule) string {
	switch {
	case s == nil || s.GetDisabled():
		return "disabled"
	case s.GetCron() != "":
		return "cron " + s.GetCron()
	case s.GetMaxFrequencyDays() > 0:
		return fmt.Sprintf("every %d day(s)", s.GetMaxFrequencyDays())
	case s.GetMaxFrequencyHours() > 0:
		return fmt.Sprintf("every %d hour(s)", s.GetMaxFrequencyHours())
	}
	return "disabled"
}

func retentionText(r *v1.RetentionPolicy) string {
	if r == nil {
		return ""
	}
	if n := r.GetPolicyKeepLastN(); n > 0 {
		return fmt.Sprintf("keep last %d", n)
	}
	if b := r.GetPolicyTimeBucketed(); b != nil {
		var parts []string
		add := func(n int32, unit string) {
			if n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, unit))
			}
		}
		add(b.GetHourly(), "hourly")
		add(b.GetDaily(), "daily")
		add(b.GetWeekly(), "weekly")
		add(b.GetMonthly(), "monthly")
		add(b.GetYearly(), "yearly")
		if b.GetKeepLastN() > 0 {
			parts = append(parts, fmt.Sprintf("last %d", b.GetKeepLastN()))
		}
		return "keep " + strings.Join(parts, ", ")
	}
	if r.GetPolicyKeepAll() {
		return "keep all"
	}
	return ""
}

// StatusText is the lower-case name of an operation status.
func StatusText(st v1.OperationStatus) string {
	return strings.ToLower(strings.TrimPrefix(st.String(), "STATUS_"))
}

func finished(st v1.OperationStatus) bool {
	switch st {
	case v1.OperationStatus_STATUS_SUCCESS, v1.OperationStatus_STATUS_WARNING, v1.OperationStatus_STATUS_ERROR:
		return true
	}
	return false
}

func runView(op *v1.Operation, sc *Scrubber) *RunView {
	b := op.GetOperationBackup()
	v := &RunView{OperationID: op.GetId(), Status: StatusText(op.GetStatus()), StartedAt: op.GetUnixTimeStartMs(),
		EndedAt: op.GetUnixTimeEndMs(), SnapshotID: op.GetSnapshotId(), Message: sc.Scrub(op.GetDisplayMessage())}
	if b != nil {
		v.Errors = len(b.GetErrors())
		if sum := b.GetLastStatus().GetSummary(); sum != nil {
			v.BytesAdded = sum.GetDataAdded()
			v.BytesTotal = sum.GetTotalBytesProcessed()
			v.FilesNew = sum.GetFilesNew()
			v.FilesChanged = sum.GetFilesChanged()
			if v.SnapshotID == "" {
				v.SnapshotID = sum.GetSnapshotId()
			}
		}
	}
	return v
}
