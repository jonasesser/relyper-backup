// Relyper-owned: pinned snapshots. A snapshot tagged PinnedTag is kept by
// every retention run (restic forget --keep-tag) and cannot be forgotten
// one by one until the tag is removed again.
package restic

import (
	"context"
	"errors"
	"strings"
)

// PinnedTag marks a snapshot that must never be removed by retention.
const PinnedTag = "relyper-pinned"

// ErrPinned is returned when a pinned snapshot should be forgotten.
var ErrPinned = errors.New("the snapshot is pinned; unpin it first")

// RemoveTags removes tags from the specified snapshots.
func (r *Repo) RemoveTags(ctx context.Context, snapshotIDs []string, tags []string, opts ...GenericOption) error {
	args := []string{"tag", "--remove", strings.Join(tags, ",")}
	args = append(args, snapshotIDs...)
	errorCollector := errorMessageCollector{}
	cmd := r.commandWithContext(ctx, args, opts...)
	r.handleOutput(cmd, withAllTo(&errorCollector), withLogWriterFromContext(ctx))
	if err := cmd.Run(); err != nil {
		return errorCollector.AddCmdOutputToError(cmd, err)
	}
	return nil
}

// IsPinned reports whether the snapshot (full or short ID) carries PinnedTag.
func (r *Repo) IsPinned(ctx context.Context, snapshotID string, opts ...GenericOption) (bool, error) {
	if snapshotID == "" {
		return false, nil
	}
	snaps, err := r.Snapshots(ctx, append(opts, WithFlags("--tag", PinnedTag))...)
	if err != nil {
		return false, err
	}
	for _, s := range snaps {
		if strings.HasPrefix(s.Id, snapshotID) || strings.HasPrefix(snapshotID, s.Id) {
			return true, nil
		}
	}
	return false, nil
}

// pinFlags keeps pinned snapshots in a forget run. Without a keep rule restic
// removes nothing, so the flag is only added next to other rules: alone it
// would remove every snapshot that is not pinned.
func pinFlags(flags []string) []string {
	if len(flags) == 0 {
		return flags
	}
	return append(flags, "--keep-tag", PinnedTag)
}
