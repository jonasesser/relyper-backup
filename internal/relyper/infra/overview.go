// Relyper-owned: the health overview shown at the top of the dashboard. It
// combines app protection, recent failures, running operations, the latest
// verified restores, repository checks and free storage. Every value is
// measured; nothing is estimated.
package infra

import (
	"context"
	"errors"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
)

// OpSummary is a short view of an operation.
type OpSummary struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Plan    string `json:"plan,omitempty"`
	Repo    string `json:"repo"`
	Time    string `json:"time"`
	Message string `json:"message,omitempty"`
}

// Overview is the dashboard health summary.
type Overview struct {
	GeneratedAt     string           `json:"generated_at"`
	Level           string           `json:"level"` // ready, at_risk, not_ready
	DockerAvailable bool             `json:"docker_available"`
	Apps            map[string]int   `json:"apps"` // by protection
	AppsTotal       int              `json:"apps_total"`
	Failed24h       []OpSummary      `json:"failed_24h"`
	FailedCount24h  int              `json:"failed_count_24h"`
	Running         []OpSummary      `json:"running"`
	LastBackup      *OpSummary       `json:"last_successful_backup,omitempty"`
	LastCheck       *OpSummary       `json:"last_successful_check,omitempty"`
	LastDBRestore   *VerifiedRestore `json:"last_verified_database_restore,omitempty"`
	LastFileRestore *VerifiedRestore `json:"last_verified_file_restore,omitempty"`
	Repos           []RepoReadiness  `json:"repos"`
	Attention       []string         `json:"attention,omitempty"` // most important problems
	Backups24h      map[string]int   `json:"backups_24h"`         // success, warning, error
}

// VerifiedRestore is the latest successful restore test.
type VerifiedRestore struct {
	Subject string `json:"subject"` // app or plan
	Time    string `json:"time"`
}

func summarize(op *v1.Operation) OpSummary {
	t := op.UnixTimeEndMs
	if t == 0 {
		t = op.UnixTimeStartMs
	}
	return OpSummary{ID: op.Id, Kind: opKind(op), Plan: op.PlanId, Repo: op.RepoId, Time: fmtTime(time.UnixMilli(t)), Message: firstLine(op.DisplayMessage)}
}

// Overview builds the dashboard summary.
func (s *Service) Overview(ctx context.Context) (*Overview, error) {
	rd, err := s.Readiness(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	o := &Overview{GeneratedAt: fmtTime(now), Level: rd.Level, Apps: map[string]int{}, Repos: rd.Repos, Backups24h: map[string]int{},
		Failed24h: []OpSummary{}, Running: []OpSummary{}}
	if rd.Coverage != nil {
		o.DockerAvailable = rd.Coverage.Docker.Available
		for _, a := range rd.Coverage.Apps {
			o.Apps[a.Protection]++
			o.AppsTotal++
			if a.Protection == ProtFailed || a.Protection == ProtNone {
				o.Attention = append(o.Attention, a.Name+": "+strings.ReplaceAll(a.Protection, "_", " "))
			}
		}
	}
	if s.d.OpLog != nil {
		cfg, err := s.cfg()
		if err != nil {
			return nil, err
		}
		since := now.Add(-24 * time.Hour).UnixMilli()
		stop := errors.New("stop")
		n := 0
		_ = s.d.OpLog.Query(oplog.Query{}.SetInstanceID(cfg.Instance).SetReversed(true), func(op *v1.Operation) error {
			n++
			kind := opKind(op)
			if kind == "" || kind == "hook" {
				return nil
			}
			switch op.Status {
			case v1.OperationStatus_STATUS_INPROGRESS:
				if op.UnixTimeStartMs >= since || len(o.Running) < 5 {
					o.Running = append(o.Running, summarize(op))
				}
			case v1.OperationStatus_STATUS_ERROR:
				if op.UnixTimeStartMs >= since {
					o.FailedCount24h++
					if len(o.Failed24h) < 5 {
						o.Failed24h = append(o.Failed24h, summarize(op))
					}
				}
			case v1.OperationStatus_STATUS_SUCCESS, v1.OperationStatus_STATUS_WARNING:
				sum := summarize(op)
				if kind == "backup" && o.LastBackup == nil {
					o.LastBackup = &sum
				}
				if kind == "check" && op.Status == v1.OperationStatus_STATUS_SUCCESS && o.LastCheck == nil {
					o.LastCheck = &sum
				}
			}
			if kind == "backup" && op.UnixTimeStartMs >= since {
				switch op.Status {
				case v1.OperationStatus_STATUS_SUCCESS:
					o.Backups24h["success"]++
				case v1.OperationStatus_STATUS_WARNING:
					o.Backups24h["warning"]++
				case v1.OperationStatus_STATUS_ERROR:
					o.Backups24h["error"]++
				}
			}
			// Operations are returned newest first; stop once everything
			// recent and the latest successes are known.
			if (op.UnixTimeStartMs < since && o.LastBackup != nil && o.LastCheck != nil) || n > 20000 {
				return stop
			}
			return nil
		})
	}
	if o.FailedCount24h > 0 {
		o.Attention = append(o.Attention, "failed operations in the last 24 hours")
	}
	for _, r := range o.Repos {
		if r.Storage != nil && r.Storage.Low {
			o.Attention = append(o.Attention, "storage of "+r.Repo+" is almost full")
		}
	}
	if s.store != nil {
		o.LastDBRestore, o.LastFileRestore = s.store.lastVerified()
	}
	return o, nil
}
