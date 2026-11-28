// Relyper-owned: pin and unpin snapshots (see pkg/restic/relyper_pin.go).
package repo

import (
	"context"
	"fmt"

	"github.com/garethgeorge/backrest/pkg/restic"
)

// SetPinned adds or removes the pinned tag of a snapshot. restic rewrites
// the snapshot, so it gets a new ID; index the snapshots afterwards.
func (r *RepoOrchestrator) SetPinned(ctx context.Context, snapshotID string, pinned bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, flush := forwardResticLogs(ctx)
	defer flush()
	// Tagging needs an exclusive lock; wait for a running backup or check.
	wait := restic.WithFlags("--retry-lock", "2m")
	var err error
	if pinned {
		err = r.repo.AddTags(ctx, []string{snapshotID}, []string{restic.PinnedTag}, wait)
	} else {
		err = r.repo.RemoveTags(ctx, []string{snapshotID}, []string{restic.PinnedTag}, wait)
	}
	if err != nil {
		return fmt.Errorf("repo %v: set pinned: %w", r.repoConfig.Id, err)
	}
	return nil
}
