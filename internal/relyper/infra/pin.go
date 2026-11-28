// Relyper-owned: pinned snapshots. A pinned snapshot carries the restic tag
// restic.PinnedTag; every retention run keeps it and it cannot be forgotten
// until it is unpinned (see pkg/restic/relyper_pin.go). The tag lives in the
// repository, so the plain restic CLI sees it too.
package infra

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/orchestrator/tasks"
	"github.com/garethgeorge/backrest/pkg/restic"
)

var snapshotIDRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// PinnedSnapshot is a snapshot kept until it is unpinned.
type PinnedSnapshot struct {
	ID   string `json:"id"`
	Plan string `json:"plan,omitempty"`
	Time string `json:"time"`
	// Size of the backed-up files (0 when restic recorded no summary).
	Size int64 `json:"size_bytes,omitempty"`
}

// IsPinned reports whether a snapshot carries the pinned tag.
func IsPinned(sn *v1.ResticSnapshot) bool {
	return sn != nil && slices.Contains(sn.Tags, restic.PinnedTag)
}

// SetPinned pins or unpins a snapshot. restic rewrites the snapshot with a
// new ID; the snapshot index is refreshed right afterwards.
func (s *Service) SetPinned(ctx context.Context, repoID, snapshotID string, pinned bool, actor string) error {
	if !snapshotIDRe.MatchString(snapshotID) {
		return errors.New("invalid snapshot ID")
	}
	if s.d.Orchestrator == nil {
		return errors.New("no orchestrator")
	}
	cfg, err := s.cfg()
	if err != nil {
		return err
	}
	var repo *v1.Repo
	for _, r := range cfg.Repos {
		if r.Id == repoID {
			repo = r
		}
	}
	if repo == nil {
		return fmt.Errorf("repository %q not found", repoID)
	}
	ro, err := s.d.Orchestrator.GetRepoOrchestrator(repoID)
	if err != nil {
		return err
	}
	if err := ro.UnlockIfAutoEnabled(ctx); err != nil {
		return err
	}
	action := map[bool]string{true: "pin", false: "unpin"}[pinned]
	if err := ro.SetPinned(ctx, snapshotID, pinned); err != nil {
		s.record(actor, action, "error", fmt.Sprintf("%s %s: %v", repoID, snapshotID, err))
		return err
	}
	s.record(actor, action, "ok", repoID+" "+snapshotID)
	if _, err := s.d.Orchestrator.ScheduleTask(tasks.NewOneoffIndexSnapshotsTask(repo, time.Now()), tasks.TaskPriorityInteractive); err != nil {
		return fmt.Errorf("snapshot %s; refreshing the snapshot list failed: %w", action+"ned", err)
	}
	return nil
}
