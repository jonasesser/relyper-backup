// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
)

// Finding is one result of the configuration validation.
type Finding struct {
	Severity      string `json:"severity" jsonschema:"error, warning or info"`
	Code          string `json:"code"`
	Subject       string `json:"subject" jsonschema:"e.g. plan:<id> or repo:<id>"`
	Message       string `json:"message"`
	Fix           string `json:"fix"`
	SuggestedTool string `json:"suggested_tool,omitempty"`
}

type FindingsOut struct {
	OK       bool      `json:"ok" jsonschema:"true when there are no errors"`
	Errors   int       `json:"errors"`
	Warnings int       `json:"warnings"`
	Findings []Finding `json:"findings"`
	Checked  []string  `json:"checked" jsonschema:"what was checked; anything not listed was not checked"`
}

var checkedRules = []string{
	"login enabled for the web UI",
	"every repository has a password",
	"SFTP repositories use BatchMode and a known_hosts file that exists",
	"repositories have a check schedule",
	"repositories have a prune schedule",
	"every plan has a schedule, retention and paths that exist inside the container",
	"last backup of each enabled plan succeeded and is not older than twice its interval (or 48 hours)",
	"at least two repositories exist (second copy)",
}

func (s *Server) validate(ctx context.Context, recommendations bool) (*FindingsOut, error) {
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	out := &FindingsOut{Findings: []Finding{}, Checked: checkedRules}
	add := func(sev, code, subject, msg, fix, tool string) {
		if sev == "info" && !recommendations {
			return
		}
		out.Findings = append(out.Findings, Finding{sev, code, subject, msg, fix, tool})
		switch sev {
		case "error":
			out.Errors++
		case "warning":
			out.Warnings++
		}
	}

	if a := cfg.GetAuth(); a == nil || a.GetDisabled() || len(a.GetUsers()) == 0 {
		add("error", "auth_disabled", "instance", "The web UI can be used without login.", "Create a user in the web UI settings (only a human can do this).", "")
	}

	for _, r := range cfg.Repos {
		subj := "repo:" + r.Id
		if r.Password == "" && !strings.Contains(strings.Join(r.Env, " "), "RESTIC_PASSWORD") {
			add("error", "repo_no_password", subj, "No repository password is configured.", "Set a password with update_repository and store it in a password manager.", "update_repository")
		}
		if repoType(r.Uri) == "sftp" {
			info := parseSFTPArgs(r.Flags)
			switch {
			case info == nil:
				add("warning", "sftp_no_args", subj, "SFTP repository without sftp.args: ssh may prompt and uses the default known_hosts.", "Set the identity file with update_repository (sftp settings).", "update_repository")
			default:
				if !info.BatchMode {
					add("warning", "sftp_no_batchmode", subj, "ssh is not started with BatchMode=yes; it may hang waiting for input.", "Re-save the SFTP settings with update_repository.", "update_repository")
				}
				if info.KnownHostsFile == "" {
					add("warning", "sftp_no_known_hosts", subj, "No dedicated known_hosts file is configured for host key verification.", "Re-save the SFTP settings with update_repository; then trust the host key with scan_sftp_host_keys and trust_sftp_host_key.", "update_repository")
				} else if _, err := os.Stat(info.KnownHostsFile); err != nil {
					add("error", "sftp_known_hosts_missing", subj, "The known_hosts file does not exist, so the host key cannot be verified.", "Trust the host key with scan_sftp_host_keys and trust_sftp_host_key.", "scan_sftp_host_keys")
				}
				if info.IdentityFile != "" {
					if _, err := os.Stat(info.IdentityFile); err != nil {
						add("error", "sftp_identity_missing", subj, "The SSH identity file does not exist inside the container.", "Create a key with create_sftp_key and install the public key on the server.", "create_sftp_key")
					}
				}
			}
		}
		if r.OriginInstanceId != "" {
			continue
		}
		if !scheduleEnabled(r.GetCheckPolicy().GetSchedule()) {
			add("warning", "repo_no_check", subj, "No integrity check is scheduled.", "Set a check schedule (e.g. weekly) with update_repository.", "update_repository")
		}
		if !scheduleEnabled(r.GetPrunePolicy().GetSchedule()) {
			add("info", "repo_no_prune", subj, "No prune is scheduled; space of forgotten snapshots is not reclaimed.", "Set a prune schedule (e.g. monthly) with update_repository.", "update_repository")
		}
	}

	if len(cfg.Repos) == 1 {
		add("info", "single_copy", "instance", "All backups go to one repository. A second copy (another provider or location) protects against loss of that storage and against deletion by a compromised server.", "Create a second repository and a plan that backs up the same paths.", "create_repository")
	}
	if len(cfg.Plans) == 0 {
		add("warning", "no_plans", "instance", "No backup plan exists, nothing is backed up.", "Create a plan with create_backup_plan.", "create_backup_plan")
	}

	for _, p := range cfg.Plans {
		subj := "plan:" + p.Id
		view := planView(p)
		if !view.Enabled {
			add("warning", "plan_disabled", subj, "The plan has no active schedule and only runs manually.", "Enable it with enable_backup_plan or set a schedule with update_backup_plan.", "enable_backup_plan")
		}
		if p.Retention == nil {
			add("warning", "plan_no_retention", subj, "No retention policy; snapshots accumulate forever.", "Set a retention policy with modify_retention_policy (needs approval).", "modify_retention_policy")
		} else if p.Retention.GetPolicyKeepAll() {
			add("info", "plan_keep_all", subj, "The plan keeps all snapshots; the repository grows without limit.", "Consider a time-based retention with modify_retention_policy.", "modify_retention_policy")
		}
		for _, path := range p.Paths {
			if _, err := os.Stat(path); err != nil {
				add("error", "plan_path_missing", subj, fmt.Sprintf("Path %q does not exist inside the container (not mounted?).", path), "Mount the directory into the container or fix the path with update_backup_plan.", "update_backup_plan")
			}
		}
		if !view.Enabled {
			continue
		}
		ops, _ := s.queryOps(oplog.Query{}.SetInstanceID(cfg.Instance).SetPlanID(p.Id), 50, func(op *v1.Operation) bool {
			return op.GetOperationBackup() != nil && !op.GetOperationBackup().DryRun && op.Status != v1.OperationStatus_STATUS_PENDING
		})
		if len(ops) == 0 {
			add("warning", "plan_never_run", subj, "The plan has never run.", "Start a first backup with run_backup.", "run_backup")
			continue
		}
		if ops[0].Status == v1.OperationStatus_STATUS_ERROR {
			add("error", "plan_last_failed", subj, "The last backup failed: "+truncate(ops[0].DisplayMessage, 300), "Look at get_backup_operation for details, fix the cause, then run_backup.", "get_backup_operation")
		}
		var lastOK time.Time
		for _, op := range ops {
			if op.Status == v1.OperationStatus_STATUS_SUCCESS || op.Status == v1.OperationStatus_STATUS_WARNING {
				lastOK = time.UnixMilli(op.UnixTimeEndMs)
				break
			}
		}
		maxAge := 48 * time.Hour
		switch x := p.GetSchedule().GetSchedule().(type) {
		case *v1.Schedule_MaxFrequencyDays:
			maxAge = 2 * time.Duration(x.MaxFrequencyDays) * 24 * time.Hour
		case *v1.Schedule_MaxFrequencyHours:
			maxAge = max(2*time.Duration(x.MaxFrequencyHours)*time.Hour, 6*time.Hour)
		}
		if lastOK.IsZero() {
			add("error", "plan_no_success", subj, "No successful backup in the recent history.", "Fix the errors shown by get_backup_errors.", "get_backup_errors")
		} else if time.Since(lastOK) > maxAge {
			add("warning", "plan_stale", subj, fmt.Sprintf("Last successful backup was %s ago.", time.Since(lastOK).Round(time.Hour)), "Check get_backup_status and the schedule.", "get_backup_status")
		}
	}
	out.OK = out.Errors == 0
	return out, nil
}

func scheduleEnabled(s *v1.Schedule) bool {
	if s == nil {
		return false
	}
	_, disabled := s.Schedule.(*v1.Schedule_Disabled)
	return s.Schedule != nil && !disabled
}
