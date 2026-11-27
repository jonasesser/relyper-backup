// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
)

// Schedule describes when something runs. It is used in tool inputs and
// outputs.
type Schedule struct {
	Kind  string `json:"kind" jsonschema:"one of: disabled, cron, every_days, every_hours"`
	Cron  string `json:"cron,omitempty" jsonschema:"cron expression with 5 fields (minute hour day month weekday), e.g. '0 2 * * *' for 02:00 daily; for kind=cron"`
	Days  int32  `json:"days,omitempty" jsonschema:"for kind=every_days: run at most every N days"`
	Hours int32  `json:"hours,omitempty" jsonschema:"for kind=every_hours: run at most every N hours"`
	Clock string `json:"clock,omitempty" jsonschema:"time base: local (default), utc, or last_run (interval counted from the last run)"`
}

func scheduleToProto(s *Schedule) (*v1.Schedule, error) {
	if s == nil {
		return nil, nil
	}
	out := &v1.Schedule{}
	switch strings.ToLower(strings.TrimSpace(s.Clock)) {
	case "", "local":
		out.Clock = v1.Schedule_CLOCK_LOCAL
	case "utc":
		out.Clock = v1.Schedule_CLOCK_UTC
	case "last_run":
		out.Clock = v1.Schedule_CLOCK_LAST_RUN_TIME
	default:
		return nil, fmt.Errorf("schedule clock %q must be local, utc or last_run", s.Clock)
	}
	switch s.Kind {
	case "disabled":
		out.Schedule = &v1.Schedule_Disabled{Disabled: true}
	case "cron":
		if strings.TrimSpace(s.Cron) == "" {
			return nil, errors.New("schedule kind cron needs a cron expression")
		}
		out.Schedule = &v1.Schedule_Cron{Cron: strings.TrimSpace(s.Cron)}
	case "every_days":
		if s.Days <= 0 {
			return nil, errors.New("schedule kind every_days needs days > 0")
		}
		out.Schedule = &v1.Schedule_MaxFrequencyDays{MaxFrequencyDays: s.Days}
	case "every_hours":
		if s.Hours <= 0 {
			return nil, errors.New("schedule kind every_hours needs hours > 0")
		}
		out.Schedule = &v1.Schedule_MaxFrequencyHours{MaxFrequencyHours: s.Hours}
	default:
		return nil, fmt.Errorf("schedule kind %q must be disabled, cron, every_days or every_hours", s.Kind)
	}
	return out, nil
}

func scheduleFromProto(p *v1.Schedule) *Schedule {
	if p == nil {
		return nil
	}
	out := &Schedule{}
	switch p.Clock {
	case v1.Schedule_CLOCK_UTC:
		out.Clock = "utc"
	case v1.Schedule_CLOCK_LAST_RUN_TIME:
		out.Clock = "last_run"
	default:
		out.Clock = "local"
	}
	switch x := p.Schedule.(type) {
	case *v1.Schedule_Cron:
		out.Kind, out.Cron = "cron", x.Cron
	case *v1.Schedule_MaxFrequencyDays:
		out.Kind, out.Days = "every_days", x.MaxFrequencyDays
	case *v1.Schedule_MaxFrequencyHours:
		out.Kind, out.Hours = "every_hours", x.MaxFrequencyHours
	default:
		out.Kind = "disabled"
	}
	return out
}

// Retention says which snapshots of a plan are kept. Either keep_all, or
// keep_last_n alone, or any of the time buckets (optionally with
// keep_last_n).
type Retention struct {
	KeepAll   bool  `json:"keep_all,omitempty" jsonschema:"keep every snapshot (no automatic deletion)"`
	KeepLastN int32 `json:"keep_last_n,omitempty" jsonschema:"keep the N most recent snapshots"`
	Hourly    int32 `json:"hourly,omitempty" jsonschema:"keep the last N hourly snapshots"`
	Daily     int32 `json:"daily,omitempty" jsonschema:"keep the last N daily snapshots"`
	Weekly    int32 `json:"weekly,omitempty" jsonschema:"keep the last N weekly snapshots"`
	Monthly   int32 `json:"monthly,omitempty" jsonschema:"keep the last N monthly snapshots"`
	Yearly    int32 `json:"yearly,omitempty" jsonschema:"keep the last N yearly snapshots"`
}

func retentionToProto(r *Retention) (*v1.RetentionPolicy, error) {
	if r == nil {
		return nil, nil
	}
	for _, n := range []int32{r.KeepLastN, r.Hourly, r.Daily, r.Weekly, r.Monthly, r.Yearly} {
		if n < 0 {
			return nil, errors.New("retention counts must not be negative")
		}
	}
	buckets := r.Hourly > 0 || r.Daily > 0 || r.Weekly > 0 || r.Monthly > 0 || r.Yearly > 0
	switch {
	case r.KeepAll && (buckets || r.KeepLastN > 0):
		return nil, errors.New("retention keep_all cannot be combined with counts")
	case r.KeepAll:
		return &v1.RetentionPolicy{Policy: &v1.RetentionPolicy_PolicyKeepAll{PolicyKeepAll: true}}, nil
	case buckets:
		return &v1.RetentionPolicy{Policy: &v1.RetentionPolicy_PolicyTimeBucketed{PolicyTimeBucketed: &v1.RetentionPolicy_TimeBucketedCounts{
			Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, Yearly: r.Yearly, KeepLastN: r.KeepLastN,
		}}}, nil
	case r.KeepLastN > 0:
		return &v1.RetentionPolicy{Policy: &v1.RetentionPolicy_PolicyKeepLastN{PolicyKeepLastN: r.KeepLastN}}, nil
	default:
		return nil, errors.New("retention needs keep_all, keep_last_n or at least one time bucket")
	}
}

func retentionFromProto(p *v1.RetentionPolicy) *Retention {
	if p == nil {
		return nil
	}
	switch x := p.Policy.(type) {
	case *v1.RetentionPolicy_PolicyKeepAll:
		return &Retention{KeepAll: true}
	case *v1.RetentionPolicy_PolicyKeepLastN:
		return &Retention{KeepLastN: x.PolicyKeepLastN}
	case *v1.RetentionPolicy_PolicyTimeBucketed:
		b := x.PolicyTimeBucketed
		return &Retention{KeepLastN: b.KeepLastN, Hourly: b.Hourly, Daily: b.Daily, Weekly: b.Weekly, Monthly: b.Monthly, Yearly: b.Yearly}
	}
	return nil
}

// SFTPInfo is parsed from a repository's sftp.args flag.
type SFTPInfo struct {
	IdentityFile   string `json:"identity_file,omitempty"`
	Port           int    `json:"port,omitempty"`
	KnownHostsFile string `json:"known_hosts_file,omitempty"`
	BatchMode      bool   `json:"batch_mode"`
}

var (
	sftpIdentityRe = regexp.MustCompile(`-i\s+["']?([^\s"']+)`)
	sftpPortRe     = regexp.MustCompile(`-p\s+(\d+)`)
	sftpKnownRe    = regexp.MustCompile(`-oUserKnownHostsFile=["']?([^\s"']+)`)
)

func parseSFTPArgs(flags []string) *SFTPInfo {
	for _, f := range flags {
		if !strings.Contains(f, "sftp.args") {
			continue
		}
		info := &SFTPInfo{BatchMode: strings.Contains(f, "BatchMode=yes")}
		if m := sftpIdentityRe.FindStringSubmatch(f); m != nil {
			info.IdentityFile = m[1]
		}
		if m := sftpPortRe.FindStringSubmatch(f); m != nil {
			fmt.Sscan(m[1], &info.Port)
		}
		if m := sftpKnownRe.FindStringSubmatch(f); m != nil {
			info.KnownHostsFile = m[1]
		}
		return info
	}
	return nil
}

// RepoPolicy is a schedule for prune/check.
type RepoMaintenance struct {
	PruneSchedule         *Schedule `json:"prune_schedule,omitempty"`
	PruneMaxUnusedPercent float64   `json:"prune_max_unused_percent,omitempty"`
	CheckSchedule         *Schedule `json:"check_schedule,omitempty"`
	CheckReadDataPercent  float64   `json:"check_read_data_percent,omitempty" jsonschema:"percentage of pack data read by check (0 = structure only)"`
}

// RepoForgetPolicy is a repository-wide retention that overrides plans.
type RepoForgetPolicy struct {
	Schedule  *Schedule  `json:"schedule,omitempty"`
	Retention *Retention `json:"retention,omitempty"`
}

// RepoView is a repository without secrets.
type RepoView struct {
	ID              string            `json:"id"`
	URI             string            `json:"uri"`
	Type            string            `json:"type"`
	GUID            string            `json:"guid,omitempty"`
	PasswordSet     bool              `json:"password_set"`
	EnvNames        []string          `json:"env_names,omitempty" jsonschema:"names of environment variables set for restic; values are never returned"`
	Flags           []string          `json:"flags,omitempty"`
	SFTP            *SFTPInfo         `json:"sftp,omitempty"`
	AutoUnlock      bool              `json:"auto_unlock"`
	AutoInitialize  bool              `json:"auto_initialize"`
	Maintenance     RepoMaintenance   `json:"maintenance"`
	ForgetPolicy    *RepoForgetPolicy `json:"forget_policy,omitempty"`
	HookCount       int               `json:"hook_count"`
	Plans           []string          `json:"plans"`
	ManagedRemotely bool              `json:"managed_remotely,omitempty" jsonschema:"true if the repo was pushed by another instance and cannot be edited here"`
}

func repoType(uri string) string {
	if strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "local:") || (len(uri) > 2 && uri[1] == ':') {
		return "local"
	}
	if t, _, ok := strings.Cut(uri, ":"); ok {
		return t
	}
	return "unknown"
}

func redactFlag(f string) string {
	if secretFieldName.MatchString(f) && !strings.Contains(f, "sftp.args") {
		if k, _, ok := strings.Cut(f, "="); ok {
			return k + "=" + redacted
		}
		return redacted
	}
	return f
}

func repoView(cfg *v1.Config, r *v1.Repo) RepoView {
	v := RepoView{
		ID:              r.Id,
		URI:             redactURI(r.Uri),
		Type:            repoType(r.Uri),
		GUID:            r.Guid,
		PasswordSet:     r.Password != "",
		SFTP:            parseSFTPArgs(r.Flags),
		AutoUnlock:      r.AutoUnlock,
		AutoInitialize:  r.AutoInitialize,
		HookCount:       len(r.Hooks),
		ManagedRemotely: r.OriginInstanceId != "",
		Plans:           []string{},
	}
	for _, e := range r.Env {
		k, _, _ := strings.Cut(e, "=")
		v.EnvNames = append(v.EnvNames, k)
	}
	for _, f := range r.Flags {
		v.Flags = append(v.Flags, redactFlag(f))
	}
	if pp := r.PrunePolicy; pp != nil {
		v.Maintenance.PruneSchedule = scheduleFromProto(pp.Schedule)
		v.Maintenance.PruneMaxUnusedPercent = pp.MaxUnusedPercent
	}
	if cp := r.CheckPolicy; cp != nil {
		v.Maintenance.CheckSchedule = scheduleFromProto(cp.Schedule)
		v.Maintenance.CheckReadDataPercent = cp.GetReadDataSubsetPercent()
	}
	if fp := r.ForgetPolicy; fp != nil {
		v.ForgetPolicy = &RepoForgetPolicy{Schedule: scheduleFromProto(fp.Schedule), Retention: retentionFromProto(fp.Retention)}
	}
	for _, p := range cfg.Plans {
		if p.Repo == r.Id {
			v.Plans = append(v.Plans, p.Id)
		}
	}
	return v
}

// HookView names a hook without its command, URL or token.
type HookView struct {
	Type       string   `json:"type"`
	Conditions []string `json:"conditions"`
}

func hookViews(hooks []*v1.Hook) []HookView {
	var out []HookView
	for _, h := range hooks {
		hv := HookView{Type: "unknown"}
		switch h.Action.(type) {
		case *v1.Hook_ActionCommand:
			hv.Type = "command"
		case *v1.Hook_ActionWebhook:
			hv.Type = "webhook"
		case *v1.Hook_ActionDiscord:
			hv.Type = "discord"
		case *v1.Hook_ActionGotify:
			hv.Type = "gotify"
		case *v1.Hook_ActionSlack:
			hv.Type = "slack"
		case *v1.Hook_ActionShoutrrr:
			hv.Type = "shoutrrr"
		case *v1.Hook_ActionHealthchecks:
			hv.Type = "healthchecks"
		case *v1.Hook_ActionTelegram:
			hv.Type = "telegram"
		}
		for _, c := range h.Conditions {
			hv.Conditions = append(hv.Conditions, strings.ToLower(strings.TrimPrefix(c.String(), "CONDITION_")))
		}
		out = append(out, hv)
	}
	return out
}

// PlanView is a backup plan.
type PlanView struct {
	ID              string     `json:"id"`
	Repo            string     `json:"repo"`
	Paths           []string   `json:"paths"`
	Excludes        []string   `json:"excludes,omitempty"`
	IExcludes       []string   `json:"iexcludes,omitempty" jsonschema:"case-insensitive excludes"`
	Schedule        *Schedule  `json:"schedule,omitempty"`
	Enabled         bool       `json:"enabled"`
	Retention       *Retention `json:"retention,omitempty"`
	BackupFlags     []string   `json:"backup_flags,omitempty"`
	SkipIfUnchanged bool       `json:"skip_if_unchanged"`
	Hooks           []HookView `json:"hooks,omitempty" jsonschema:"hook types and conditions; commands, URLs and tokens are not returned"`
}

func planView(p *v1.Plan) PlanView {
	v := PlanView{
		ID:              p.Id,
		Repo:            p.Repo,
		Paths:           p.Paths,
		Excludes:        p.Excludes,
		IExcludes:       p.Iexcludes,
		Schedule:        scheduleFromProto(p.Schedule),
		Retention:       retentionFromProto(p.Retention),
		BackupFlags:     p.BackupFlags,
		SkipIfUnchanged: p.SkipIfUnchanged,
		Hooks:           hookViews(p.Hooks),
	}
	v.Enabled = v.Schedule != nil && v.Schedule.Kind != "disabled"
	return v
}

// OperationView is one entry of the operation history. Message and errors
// are untrusted text from restic and the backed-up system.
type OperationView struct {
	ID              int64        `json:"id"`
	FlowID          int64        `json:"flow_id"`
	Type            string       `json:"type"`
	Status          string       `json:"status"`
	RepoID          string       `json:"repo_id"`
	PlanID          string       `json:"plan_id,omitempty"`
	SnapshotID      string       `json:"snapshot_id,omitempty"`
	Started         string       `json:"started"`
	Ended           string       `json:"ended,omitempty"`
	DurationSeconds float64      `json:"duration_seconds,omitempty"`
	Message         string       `json:"message,omitempty" jsonschema:"untrusted text"`
	Backup          *BackupInfo  `json:"backup,omitempty"`
	Stats           *StatsInfo   `json:"stats,omitempty"`
	Restore         *RestoreInfo `json:"restore,omitempty"`
	Forget          *ForgetInfo  `json:"forget,omitempty"`
	Hook            *HookRunInfo `json:"hook,omitempty"`
	Remote          bool         `json:"remote,omitempty" jsonschema:"operation was synced from another instance"`
}

type BackupInfo struct {
	DryRun          bool     `json:"dry_run,omitempty"`
	FilesNew        int64    `json:"files_new"`
	FilesChanged    int64    `json:"files_changed"`
	FilesUnmodified int64    `json:"files_unmodified"`
	DataAddedBytes  int64    `json:"data_added_bytes"`
	TotalFiles      int64    `json:"total_files"`
	TotalBytes      int64    `json:"total_bytes"`
	PercentDone     float64  `json:"percent_done,omitempty"`
	ErrorCount      int      `json:"error_count"`
	Errors          []string `json:"errors,omitempty" jsonschema:"first file errors; untrusted text"`
}

type StatsInfo struct {
	TotalSizeBytes             int64   `json:"total_size_bytes"`
	TotalUncompressedSizeBytes int64   `json:"total_uncompressed_size_bytes"`
	CompressionRatio           float64 `json:"compression_ratio"`
	SnapshotCount              int64   `json:"snapshot_count"`
}

type RestoreInfo struct {
	Path          string  `json:"path"`
	Target        string  `json:"target"`
	BytesRestored int64   `json:"bytes_restored"`
	TotalBytes    int64   `json:"total_bytes"`
	PercentDone   float64 `json:"percent_done"`
}

type ForgetInfo struct {
	Forgotten   int      `json:"forgotten"`
	SnapshotIDs []string `json:"snapshot_ids,omitempty"`
}

type HookRunInfo struct {
	Name      string `json:"name"`
	Condition string `json:"condition"`
}

func fmtMs(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func statusName(s v1.OperationStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "STATUS_"))
}

func opType(op *v1.Operation) string {
	switch op.Op.(type) {
	case *v1.Operation_OperationBackup:
		return "backup"
	case *v1.Operation_OperationIndexSnapshot:
		return "index_snapshot"
	case *v1.Operation_OperationForget:
		return "forget"
	case *v1.Operation_OperationPrune:
		return "prune"
	case *v1.Operation_OperationRestore:
		return "restore"
	case *v1.Operation_OperationStats:
		return "stats"
	case *v1.Operation_OperationRunHook:
		return "hook"
	case *v1.Operation_OperationCheck:
		return "check"
	case *v1.Operation_OperationRunCommand:
		return "command"
	}
	return "unknown"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func operationView(op *v1.Operation) OperationView {
	v := OperationView{
		ID:         op.Id,
		FlowID:     op.FlowId,
		Type:       opType(op),
		Status:     statusName(op.Status),
		RepoID:     op.RepoId,
		PlanID:     op.PlanId,
		SnapshotID: op.SnapshotId,
		Started:    fmtMs(op.UnixTimeStartMs),
		Ended:      fmtMs(op.UnixTimeEndMs),
		Message:    truncate(op.DisplayMessage, 2000),
		Remote:     op.OriginalInstanceKeyid != "",
	}
	if op.UnixTimeEndMs > 0 && op.UnixTimeStartMs > 0 {
		v.DurationSeconds = float64(op.UnixTimeEndMs-op.UnixTimeStartMs) / 1000
	}
	switch x := op.Op.(type) {
	case *v1.Operation_OperationBackup:
		b := &BackupInfo{DryRun: x.OperationBackup.DryRun, ErrorCount: len(x.OperationBackup.Errors)}
		if s := x.OperationBackup.GetLastStatus().GetSummary(); s != nil {
			b.FilesNew, b.FilesChanged, b.FilesUnmodified = s.FilesNew, s.FilesChanged, s.FilesUnmodified
			b.DataAddedBytes, b.TotalFiles, b.TotalBytes = s.DataAdded, s.TotalFilesProcessed, s.TotalBytesProcessed
			b.PercentDone = 1
		} else if st := x.OperationBackup.GetLastStatus().GetStatus(); st != nil {
			b.TotalFiles, b.TotalBytes, b.PercentDone = st.TotalFiles, st.TotalBytes, st.PercentDone
		}
		for i, e := range x.OperationBackup.Errors {
			if i >= 10 {
				break
			}
			b.Errors = append(b.Errors, truncate(fmt.Sprintf("%s: %s", e.Item, e.Message), 300))
		}
		v.Backup = b
	case *v1.Operation_OperationStats:
		if s := x.OperationStats.Stats; s != nil {
			v.Stats = &StatsInfo{s.TotalSize, s.TotalUncompressedSize, s.CompressionRatio, s.SnapshotCount}
		}
	case *v1.Operation_OperationRestore:
		r := &RestoreInfo{Path: x.OperationRestore.Path, Target: x.OperationRestore.Target}
		if st := x.OperationRestore.LastStatus; st != nil {
			r.BytesRestored, r.TotalBytes, r.PercentDone = st.BytesRestored, st.TotalBytes, st.PercentDone
		}
		v.Restore = r
	case *v1.Operation_OperationForget:
		f := &ForgetInfo{Forgotten: len(x.OperationForget.Forget)}
		for _, s := range x.OperationForget.Forget {
			f.SnapshotIDs = append(f.SnapshotIDs, s.Id)
		}
		v.Forget = f
	case *v1.Operation_OperationRunHook:
		v.Hook = &HookRunInfo{Name: x.OperationRunHook.Name, Condition: strings.ToLower(strings.TrimPrefix(x.OperationRunHook.Condition.String(), "CONDITION_"))}
	}
	return v
}

// SnapshotView is a restic snapshot. Host names, paths and tags come from
// the backed-up system.
type SnapshotView struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"short_id"`
	Time     string   `json:"time"`
	Hostname string   `json:"hostname"`
	Username string   `json:"username,omitempty"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags,omitempty"`
	PlanID   string   `json:"plan_id,omitempty"`
	Parent   string   `json:"parent,omitempty"`
	Summary  *struct {
		FilesNew       int64 `json:"files_new"`
		FilesChanged   int64 `json:"files_changed"`
		DataAddedBytes int64 `json:"data_added_bytes"`
		TotalFiles     int64 `json:"total_files"`
		TotalBytes     int64 `json:"total_bytes"`
	} `json:"summary,omitempty"`
}

func snapshotView(s *v1.ResticSnapshot) SnapshotView {
	v := SnapshotView{
		ID:       s.Id,
		ShortID:  truncateID(s.Id),
		Time:     fmtMs(s.UnixTimeMs),
		Hostname: s.Hostname,
		Username: s.Username,
		Paths:    s.Paths,
		Tags:     s.Tags,
		Parent:   s.Parent,
	}
	for _, t := range s.Tags {
		if p, ok := strings.CutPrefix(t, "plan:"); ok {
			v.PlanID = p
		}
	}
	if sm := s.Summary; sm != nil {
		v.Summary = &struct {
			FilesNew       int64 `json:"files_new"`
			FilesChanged   int64 `json:"files_changed"`
			DataAddedBytes int64 `json:"data_added_bytes"`
			TotalFiles     int64 `json:"total_files"`
			TotalBytes     int64 `json:"total_bytes"`
		}{sm.FilesNew, sm.FilesChanged, sm.DataAdded, sm.TotalFilesProcessed, sm.TotalBytesProcessed}
	}
	return v
}

func truncateID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func findRepo(cfg *v1.Config, id string) (*v1.Repo, error) {
	idx := slices.IndexFunc(cfg.Repos, func(r *v1.Repo) bool { return r.Id == id })
	if idx < 0 {
		return nil, fmt.Errorf("repository %q not found; use list_repositories", id)
	}
	return cfg.Repos[idx], nil
}

func findPlan(cfg *v1.Config, id string) (*v1.Plan, error) {
	idx := slices.IndexFunc(cfg.Plans, func(p *v1.Plan) bool { return p.Id == id })
	if idx < 0 {
		return nil, fmt.Errorf("backup plan %q not found; use list_backup_plans", id)
	}
	return cfg.Plans[idx], nil
}
