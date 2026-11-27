// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/orchestrator/tasks"
	"github.com/garethgeorge/backrest/internal/relyper/mcpauth"
)

type RunBackupIn struct {
	PlanID string `json:"plan_id"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"simulate: report what would be backed up without creating a snapshot"`
}

type StartedOut struct {
	OperationID int64  `json:"operation_id" jsonschema:"follow progress with get_backup_operation"`
	Message     string `json:"message"`
}

type CancelOut struct {
	Message string `json:"message"`
}

func registerRunTools(r *registrar) {
	s := r.s
	spec := func(name, title, desc string) toolSpec {
		return toolSpec{Name: name, Title: title, Description: desc, Scope: mcpauth.ScopeBackupsRun}
	}

	addTool(r, spec("run_backup", "Run backup", "Starts a backup of a plan now (in addition to its schedule). Returns at once with the operation ID; the retention policy of the plan is applied afterwards as usual."),
		func(ctx context.Context, c caller, in RunBackupIn) (*StartedOut, error) {
			plan, err := s.d.Orchestrator.GetPlan(in.PlanID)
			if err != nil {
				return nil, err
			}
			repo, err := s.d.Orchestrator.GetRepo(plan.Repo)
			if err != nil {
				return nil, err
			}
			id, err := s.d.Orchestrator.ScheduleTask(tasks.NewOneoffBackupTask(repo, plan, time.Now(), in.DryRun), tasks.TaskPriorityInteractive)
			if err != nil {
				return nil, err
			}
			return &StartedOut{OperationID: id, Message: "Backup queued. It runs after operations already running on the same repository."}, nil
		})

	repoTask := func(task v1.DoRepoTaskRequest_Task, repoID string, ctx context.Context) (*StartedOut, error) {
		resp, err := s.d.Backrest.DoRepoTask(ctx, connect.NewRequest(&v1.DoRepoTaskRequest{RepoId: repoID, Task: task}))
		if err != nil {
			return nil, err
		}
		return &StartedOut{OperationID: resp.Msg.OperationId, Message: "Queued."}, nil
	}

	addTool(r, spec("run_repository_check", "Check repository", "Starts 'restic check' on a repository (structure, plus the data subset configured for the repository). Read-only for the repository data."),
		func(ctx context.Context, c caller, in RepoIDIn) (*StartedOut, error) {
			return repoTask(v1.DoRepoTaskRequest_TASK_CHECK, in.RepoID, ctx)
		})

	addTool(r, spec("run_repository_stats", "Refresh statistics", "Starts 'restic stats' to refresh the repository size shown by get_storage_usage."),
		func(ctx context.Context, c caller, in RepoIDIn) (*StartedOut, error) {
			return repoTask(v1.DoRepoTaskRequest_TASK_STATS, in.RepoID, ctx)
		})

	addTool(r, spec("cancel_backup_operation", "Cancel operation", "Cancels a pending or running operation (backup, check, prune, restore …). A cancelled backup leaves no snapshot; data already uploaded is cleaned up by the next prune."),
		func(ctx context.Context, c caller, in OpIDIn) (*CancelOut, error) {
			op, err := s.d.OpLog.Get(in.OperationID)
			if err != nil {
				return nil, errors.New("operation not found")
			}
			if op.Status != v1.OperationStatus_STATUS_PENDING && op.Status != v1.OperationStatus_STATUS_INPROGRESS {
				return nil, errors.New("operation is not pending or running (status " + statusName(op.Status) + ")")
			}
			if _, err := s.d.Backrest.Cancel(ctx, connect.NewRequest(&v1.CancelOperationRequest{OperationId: in.OperationID})); err != nil {
				return nil, err
			}
			return &CancelOut{Message: "Cancellation requested."}, nil
		})
}
