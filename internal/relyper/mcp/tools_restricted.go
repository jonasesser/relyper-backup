// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/orchestrator/tasks"
	"github.com/garethgeorge/backrest/internal/relyper/mcpauth"
	"github.com/garethgeorge/backrest/internal/relyper/sshdiag"
)

type TrustHostKeyIn struct {
	ApprovalField
	Host              string `json:"host"`
	Port              int    `json:"port,omitempty"`
	KeyType           string `json:"key_type" jsonschema:"key type as returned by scan_sftp_host_keys, e.g. ssh-ed25519"`
	FingerprintSHA256 string `json:"fingerprint_sha256" jsonschema:"fingerprint as returned by scan_sftp_host_keys, e.g. SHA256:..."`
}

type RestoreIn struct {
	ApprovalField
	RepoID     string `json:"repo_id"`
	SnapshotID string `json:"snapshot_id" jsonschema:"full snapshot ID"`
	Path       string `json:"path,omitempty" jsonschema:"file or directory inside the snapshot (default / = everything)"`
	Target     string `json:"target" jsonschema:"absolute directory inside the container that does not exist yet, e.g. /restore/2026-11-27-nextcloud; existing files are never overwritten"`
}

type SnapshotRefIn struct {
	ApprovalField
	RepoID     string `json:"repo_id"`
	SnapshotID string `json:"snapshot_id" jsonschema:"full snapshot ID"`
}

type RepoRefIn struct {
	ApprovalField
	RepoID string `json:"repo_id"`
}

type PlanRefIn struct {
	ApprovalField
	PlanID string `json:"plan_id"`
}

type RetentionIn struct {
	ApprovalField
	PlanID    string     `json:"plan_id,omitempty" jsonschema:"change the retention of this plan (applied after each of its backups)"`
	RepoID    string     `json:"repo_id,omitempty" jsonschema:"instead: set a repository-wide retention that overrides all plans of the repository"`
	Retention *Retention `json:"retention,omitempty" jsonschema:"new policy; for repo_id omit it to remove the repository-wide policy"`
	Schedule  *Schedule  `json:"schedule,omitempty" jsonschema:"repo_id only: when the repository-wide retention runs (default daily 01:00)"`
}

type snapshotPreview struct {
	Repo     string       `json:"repo"`
	Snapshot SnapshotView `json:"snapshot"`
}

func registerRestrictedTools(r *registrar) {
	s := r.s

	addRestricted(r, restrictedSpec[TrustHostKeyIn]{
		toolSpec: toolSpec{Name: "trust_sftp_host_key", Title: "Trust SFTP host key", Scope: mcpauth.ScopeReposManage, Idempotent: true,
			Description: "Adds an SFTP server's host key to Relyper Backup's known_hosts so restic can connect. The key is fetched again and only added if its fingerprint still equals the given one; an existing different key is never replaced. The user must compare the fingerprint with the provider's published one before approving."},
		Preview: func(ctx context.Context, in TrustHostKeyIn) (string, any, error) {
			keys, d := sshdiag.ScanHostKeys(ctx, in.Host, in.Port, s.knownHostsPath(), 15*time.Second)
			if d != nil {
				return "", nil, fmt.Errorf("%s: %s", d.Category, d.Message)
			}
			for _, k := range keys {
				if k.Type == in.KeyType {
					if k.Fingerprint != in.FingerprintSHA256 {
						return "", nil, fmt.Errorf("the server now presents a different %s key (%s); do not trust it without checking with the provider", k.Type, k.Fingerprint)
					}
					if k.Status == sshdiag.KeyMismatch || k.Status == sshdiag.KeyRevoked {
						return "", nil, fmt.Errorf("a different key is already recorded for this host or the key is revoked (%s); this must be resolved by hand", k.Status)
					}
					addr := sshdiag.Address(in.Host, in.Port)
					return fmt.Sprintf("Trust SSH host key %s %s for %s", in.KeyType, in.FingerprintSHA256, addr),
						map[string]any{"host": addr, "key_type": k.Type, "fingerprint_sha256": k.Fingerprint, "current_status": k.Status.String(), "known_hosts": s.knownHostsPath(),
							"check": "Compare the fingerprint with the one your provider publishes before approving."}, nil
				}
			}
			return "", nil, fmt.Errorf("the server does not present a %s key", in.KeyType)
		},
		Execute: func(ctx context.Context, c caller, in TrustHostKeyIn) (int64, any, error) {
			already, err := sshdiag.TrustHostKey(ctx, in.Host, in.Port, in.KeyType, in.FingerprintSHA256, s.knownHostsPath(), 15*time.Second)
			if err != nil {
				return 0, nil, err
			}
			return 0, map[string]any{"already_trusted": already, "known_hosts": s.knownHostsPath()}, nil
		},
	})

	addRestricted(r, restrictedSpec[RestoreIn]{
		toolSpec: toolSpec{Name: "restore_snapshot", Title: "Restore snapshot", Scope: mcpauth.ScopeRestore,
			Description: "Restores a snapshot (or a path inside it) into a new directory inside the container. The target must not exist, so existing files are never overwritten; restoring over production data is not offered through MCP."},
		Preview: func(ctx context.Context, in RestoreIn) (string, any, error) {
			if !filepath.IsAbs(in.Target) || filepath.Clean(in.Target) != in.Target || in.Target == "/" {
				return "", nil, errors.New("target must be a clean absolute path, not /")
			}
			if _, err := os.Lstat(in.Target); err == nil {
				return "", nil, fmt.Errorf("target %q already exists; choose a new directory", in.Target)
			}
			if st, err := os.Stat(filepath.Dir(in.Target)); err != nil || !st.IsDir() {
				return "", nil, fmt.Errorf("parent directory of %q does not exist", in.Target)
			}
			sn, err := s.exactSnapshot(ctx, in.RepoID, in.SnapshotID)
			if err != nil {
				return "", nil, err
			}
			path := in.Path
			if path == "" {
				path = "/"
			}
			v := snapshotView(sn)
			return fmt.Sprintf("Restore %s of snapshot %s (%s, %s) from %s into %s", path, v.ShortID, v.Time, v.Hostname, in.RepoID, in.Target),
				map[string]any{"snapshot": v, "path": path, "target": in.Target, "overwrites": "nothing (target is a new directory)"}, nil
		},
		Execute: func(ctx context.Context, c caller, in RestoreIn) (int64, any, error) {
			sn, err := s.exactSnapshot(ctx, in.RepoID, in.SnapshotID)
			if err != nil {
				return 0, nil, err
			}
			resp, err := s.d.Backrest.Restore(ctx, connect.NewRequest(&v1.RestoreSnapshotRequest{RepoId: in.RepoID, PlanId: snapshotView(sn).PlanID, SnapshotId: sn.Id, Path: in.Path, Target: in.Target}))
			if err != nil {
				return 0, nil, err
			}
			return resp.Msg.OperationId, nil, nil
		},
	})

	addRestricted(r, restrictedSpec[SnapshotRefIn]{
		toolSpec: toolSpec{Name: "delete_snapshot", Title: "Delete snapshot", Scope: mcpauth.ScopeDataDelete, Destructive: true,
			Description: "Forgets one snapshot (restic forget). Its data is freed by the next prune. This cannot be undone."},
		Preview: func(ctx context.Context, in SnapshotRefIn) (string, any, error) {
			sn, err := s.exactSnapshot(ctx, in.RepoID, in.SnapshotID)
			if err != nil {
				return "", nil, err
			}
			v := snapshotView(sn)
			if v.PlanID == "" {
				return "", nil, errors.New("the snapshot has no plan tag; delete it in the web UI or with the restic CLI")
			}
			return fmt.Sprintf("Delete snapshot %s (%s, plan %s) from %s", v.ShortID, v.Time, v.PlanID, in.RepoID), snapshotPreview{Repo: in.RepoID, Snapshot: v}, nil
		},
		Execute: func(ctx context.Context, c caller, in SnapshotRefIn) (int64, any, error) {
			sn, err := s.exactSnapshot(ctx, in.RepoID, in.SnapshotID)
			if err != nil {
				return 0, nil, err
			}
			resp, err := s.d.Backrest.Forget(ctx, connect.NewRequest(&v1.ForgetRequest{RepoId: in.RepoID, PlanId: snapshotView(sn).PlanID, SnapshotId: sn.Id}))
			if err != nil {
				return 0, nil, err
			}
			return resp.Msg.OperationId, nil, nil
		},
	})

	addRestricted(r, restrictedSpec[RepoRefIn]{
		toolSpec: toolSpec{Name: "prune_repository", Title: "Prune repository", Scope: mcpauth.ScopeDataDelete, Destructive: true,
			Description: "Runs restic prune: permanently removes data that no snapshot references any more (after forget). Snapshots are not changed."},
		Preview: func(ctx context.Context, in RepoRefIn) (string, any, error) {
			cfg, err := s.cfg()
			if err != nil {
				return "", nil, err
			}
			rp, err := findRepo(cfg, in.RepoID)
			if err != nil {
				return "", nil, err
			}
			p := map[string]any{"repo": rp.Id, "max_unused_percent": rp.GetPrunePolicy().GetMaxUnusedPercent(), "effect": "permanently deletes unreferenced pack data; forgotten snapshots can no longer be recovered from the storage afterwards"}
			if ops, _ := s.queryOps(oplog.Query{}.SetRepoGUID(rp.Guid), 1, func(op *v1.Operation) bool { return op.GetOperationPrune() != nil }); len(ops) > 0 {
				p["last_prune"] = operationView(ops[0])
			}
			return "Prune repository " + rp.Id, p, nil
		},
		Execute: func(ctx context.Context, c caller, in RepoRefIn) (int64, any, error) {
			resp, err := s.d.Backrest.DoRepoTask(ctx, connect.NewRequest(&v1.DoRepoTaskRequest{RepoId: in.RepoID, Task: v1.DoRepoTaskRequest_TASK_PRUNE}))
			if err != nil {
				return 0, nil, err
			}
			return resp.Msg.OperationId, nil, nil
		},
	})

	addRestricted(r, restrictedSpec[RetentionIn]{
		toolSpec: toolSpec{Name: "modify_retention_policy", Title: "Change retention", Scope: mcpauth.ScopeDataDelete, Destructive: true,
			Description: "Sets how many snapshots a plan keeps (applied after its next backup), or a repository-wide retention that overrides the plans. Snapshots outside the new policy are deleted when it is applied."},
		Preview: func(ctx context.Context, in RetentionIn) (string, any, error) {
			cfg, err := s.cfg()
			if err != nil {
				return "", nil, err
			}
			if (in.PlanID == "") == (in.RepoID == "") {
				return "", nil, errors.New("give exactly one of plan_id or repo_id")
			}
			if _, err := retentionToProto(in.Retention); err != nil && in.Retention != nil {
				return "", nil, err
			}
			if in.PlanID != "" {
				if in.Retention == nil {
					return "", nil, errors.New("retention is required for a plan")
				}
				if in.Schedule != nil {
					return "", nil, errors.New("schedule is only valid with repo_id")
				}
				p, err := findPlan(cfg, in.PlanID)
				if err != nil {
					return "", nil, err
				}
				pv := map[string]any{"plan": p.Id, "current": retentionFromProto(p.Retention), "new": in.Retention, "applies": "after the next backup of the plan"}
				if dash, err := s.dashboard(ctx); err == nil {
					if sm := summaryByID(dash.PlanSummaries, p.Id); sm != nil {
						pv["snapshots_now"] = sm.TotalSnapshots
					}
				}
				pv["note"] = "The exact snapshots that will be removed are not computed in advance."
				return "Change retention of plan " + p.Id, pv, nil
			}
			rp, err := findRepo(cfg, in.RepoID)
			if err != nil {
				return "", nil, err
			}
			if _, err := scheduleToProto(in.Schedule); err != nil {
				return "", nil, err
			}
			var cur *RepoForgetPolicy
			if fp := rp.ForgetPolicy; fp != nil {
				cur = &RepoForgetPolicy{Schedule: scheduleFromProto(fp.Schedule), Retention: retentionFromProto(fp.Retention)}
			}
			action := "Set"
			if in.Retention == nil {
				action = "Remove"
			}
			return action + " repository-wide retention of " + rp.Id, map[string]any{"repo": rp.Id, "current": cur, "new_retention": in.Retention, "new_schedule": in.Schedule, "plans_affected": repoView(cfg, rp).Plans,
				"note": "A repository-wide policy overrides the retention of every plan in this repository."}, nil
		},
		Execute: func(ctx context.Context, c caller, in RetentionIn) (int64, any, error) {
			ret, _ := retentionToProto(in.Retention)
			_, err := s.transform(func(cfg *v1.Config) error {
				if in.PlanID != "" {
					p, err := findPlan(cfg, in.PlanID)
					if err != nil {
						return err
					}
					p.Retention = ret
					return nil
				}
				rp, err := findRepo(cfg, in.RepoID)
				if err != nil {
					return err
				}
				if ret == nil {
					rp.ForgetPolicy = nil
					return nil
				}
				sch := in.Schedule
				if sch == nil {
					sch = &Schedule{Kind: "cron", Cron: "0 1 * * *"}
				}
				ps, err := scheduleToProto(sch)
				if err != nil {
					return err
				}
				rp.ForgetPolicy = &v1.ForgetPolicy{Schedule: ps, Retention: ret}
				return nil
			})
			return 0, nil, err
		},
	})

	addRestricted(r, restrictedSpec[PlanRefIn]{
		toolSpec: toolSpec{Name: "delete_backup_plan", Title: "Delete backup plan", Scope: mcpauth.ScopePlansManage, Destructive: true,
			Description: "Removes a backup plan from the configuration. Its snapshots stay in the repository and can still be restored."},
		Preview: func(ctx context.Context, in PlanRefIn) (string, any, error) {
			cfg, err := s.cfg()
			if err != nil {
				return "", nil, err
			}
			p, err := findPlan(cfg, in.PlanID)
			if err != nil {
				return "", nil, err
			}
			return "Delete backup plan " + p.Id, map[string]any{"plan": planView(p), "snapshots": "kept in repository " + p.Repo}, nil
		},
		Execute: func(ctx context.Context, c caller, in PlanRefIn) (int64, any, error) {
			_, err := s.transform(func(cfg *v1.Config) error {
				n := len(cfg.Plans)
				cfg.Plans = slices.DeleteFunc(cfg.Plans, func(p *v1.Plan) bool { return p.Id == in.PlanID })
				if len(cfg.Plans) == n {
					return fmt.Errorf("backup plan %q not found", in.PlanID)
				}
				return nil
			})
			return 0, nil, err
		},
	})

	addRestricted(r, restrictedSpec[RepoRefIn]{
		toolSpec: toolSpec{Name: "delete_repository", Title: "Remove repository", Scope: mcpauth.ScopeDataDelete, Destructive: true,
			Description: "Removes a repository from Relyper Backup's configuration and deletes its operation history here. The backup data in the storage is not deleted and can be added again later with the password. Plans using it must be deleted first."},
		Preview: func(ctx context.Context, in RepoRefIn) (string, any, error) {
			cfg, err := s.cfg()
			if err != nil {
				return "", nil, err
			}
			rp, err := findRepo(cfg, in.RepoID)
			if err != nil {
				return "", nil, err
			}
			v := repoView(cfg, rp)
			if len(v.Plans) > 0 {
				return "", nil, fmt.Errorf("plans %s still use this repository; delete or move them first", strings.Join(v.Plans, ", "))
			}
			return "Remove repository " + rp.Id + " from the configuration", map[string]any{"repository": v, "storage_data": "not deleted", "operation_history": "deleted"}, nil
		},
		Execute: func(ctx context.Context, c caller, in RepoRefIn) (int64, any, error) {
			_, err := s.d.Backrest.RemoveRepo(ctx, connect.NewRequest(&v1.RemoveRepoRequest{RepoId: in.RepoID}))
			return 0, nil, err
		},
	})
}

// exactSnapshot resolves a full snapshot ID (restricted tools do not accept
// short IDs, so the approval names exactly one snapshot).
func (s *Server) exactSnapshot(ctx context.Context, repoID, id string) (*v1.ResticSnapshot, error) {
	if len(id) != 64 {
		return nil, errors.New("snapshot_id must be the full 64-character ID (see list_snapshots)")
	}
	return s.findSnapshot(ctx, repoID, id)
}

// protectedTarget reports whether dir is Relyper Backup's own state or a
// system directory that a restore must never write into.
func protectedTarget(dir string) bool {
	roots := []string{"/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc", "/proc", "/sys", "/dev", "/boot", "/root"}
	for _, env := range []string{"BACKREST_CONFIG", "BACKREST_DATA", "XDG_CACHE_HOME"} {
		if v := os.Getenv(env); v != "" {
			if env == "BACKREST_CONFIG" {
				v = filepath.Dir(v)
			}
			roots = append(roots, filepath.Clean(v))
		}
	}
	if dir == "/" {
		return true
	}
	for _, r := range roots {
		if dir == r || strings.HasPrefix(dir, r+"/") || strings.HasPrefix(r, dir+"/") {
			return true
		}
	}
	return false
}

func registerOverwriteTool(r *registrar) {
	s := r.s
	addRestricted(r, restrictedSpec[RestoreIn]{
		toolSpec: toolSpec{Name: "overwrite_existing_files", Title: "Restore over existing files", Scope: mcpauth.ScopeRestore, Destructive: true,
			Description: "Restores a snapshot (or a path inside it) into an EXISTING directory and overwrites files with the same name; other files stay. Use it only to roll back production data after checking a restore into a new directory with restore_snapshot. Needs an approval for every call; the target must be writable for Relyper Backup (sources are normally mounted read-only) and must not be Relyper Backup's own state or a system directory. Stop the app first."},
		Preview: func(ctx context.Context, in RestoreIn) (string, any, error) {
			if !filepath.IsAbs(in.Target) || filepath.Clean(in.Target) != in.Target {
				return "", nil, errors.New("target must be a clean absolute path")
			}
			if protectedTarget(in.Target) {
				return "", nil, fmt.Errorf("%s is Relyper Backup's own state or a system directory", in.Target)
			}
			st, err := os.Stat(in.Target)
			if err != nil || !st.IsDir() {
				return "", nil, fmt.Errorf("target %q is not an existing directory; use restore_snapshot for a new directory", in.Target)
			}
			entries, err := os.ReadDir(in.Target)
			if err != nil {
				return "", nil, fmt.Errorf("cannot read %s: %v", in.Target, err)
			}
			sn, err := s.exactSnapshot(ctx, in.RepoID, in.SnapshotID)
			if err != nil {
				return "", nil, err
			}
			path := in.Path
			if path == "" {
				path = "/"
			}
			v := snapshotView(sn)
			return fmt.Sprintf("OVERWRITE files in %s with %s of snapshot %s (%s) from %s", in.Target, path, v.ShortID, v.Time, in.RepoID),
				map[string]any{"snapshot": v, "path": path, "target": in.Target, "existing_entries_in_target": len(entries),
					"overwrites": "files in the target that also exist in the snapshot; files only in the target stay", "advice": "Stop the app before approving. This cannot be undone except by restoring an older snapshot."}, nil
		},
		Execute: func(ctx context.Context, c caller, in RestoreIn) (int64, any, error) {
			if protectedTarget(in.Target) {
				return 0, nil, errors.New("protected target")
			}
			sn, err := s.exactSnapshot(ctx, in.RepoID, in.SnapshotID)
			if err != nil {
				return 0, nil, err
			}
			repo, err := s.d.Orchestrator.GetRepo(in.RepoID)
			if err != nil {
				return 0, nil, err
			}
			id, err := s.d.Orchestrator.ScheduleTask(tasks.NewOneoffRestoreTask(repo, snapshotView(sn).PlanID, 0, time.Now(), sn.Id, in.Path, in.Target), tasks.TaskPriorityInteractive+tasks.TaskPriorityDefault)
			if err != nil {
				return 0, nil, err
			}
			return id, nil, nil
		},
	})
}
