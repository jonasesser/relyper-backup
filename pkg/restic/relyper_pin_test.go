// Relyper-owned: tests for pinned snapshots.
package restic

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/garethgeorge/backrest/test/helpers"
)

func TestPinnedSnapshots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewRepo(helpers.ResticBinary(t), t.TempDir(), WithFlags("--no-cache"), WithEnv("RESTIC_PASSWORD=test"))
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	data := helpers.CreateTestData(t)
	var ids []string
	for i := 0; i < 4; i++ {
		out, err := r.Backup(ctx, []string{data}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, out.SnapshotId)
	}
	// Pin the oldest snapshot; restic gives it a new ID.
	if err := r.AddTags(ctx, []string{ids[0]}, []string{PinnedTag}); err != nil {
		t.Fatal(err)
	}
	pinned, err := r.Snapshots(ctx, WithFlags("--tag", PinnedTag))
	if err != nil || len(pinned) != 1 {
		t.Fatalf("pinned snapshots %v %v", pinned, err)
	}
	pinID := pinned[0].Id

	// Retention keeps the newest one and the pinned one.
	res, err := r.Forget(ctx, &RetentionPolicy{KeepLastN: 1})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, s := range res.Keep {
		kept = append(kept, s.Id)
	}
	if len(kept) != 2 || !slices.Contains(kept, pinID) || !slices.Contains(kept, ids[3]) || len(res.Remove) != 2 {
		t.Fatalf("kept %v, removed %d", kept, len(res.Remove))
	}

	// A pinned snapshot cannot be forgotten, also by its short ID.
	if err := r.ForgetSnapshot(ctx, pinID[:8]); !errors.Is(err, ErrPinned) {
		t.Fatalf("forget pinned: %v", err)
	}
	// Unpinned it can.
	if err := r.RemoveTags(ctx, []string{pinID}, []string{PinnedTag}); err != nil {
		t.Fatal(err)
	}
	snaps, _ := r.Snapshots(ctx)
	if len(snaps) != 2 {
		t.Fatalf("snapshots after unpin: %d", len(snaps))
	}
	for _, s := range snaps {
		if s.Id != ids[3] {
			if err := r.ForgetSnapshot(ctx, s.Id); err != nil {
				t.Fatalf("forget unpinned: %v", err)
			}
		}
	}
}

func TestPinFlagsNeedARule(t *testing.T) {
	if f := (&RetentionPolicy{}).toForgetFlags(); len(f) != 0 {
		t.Fatalf("no rule must stay without flags (keep-tag alone would remove everything else): %v", f)
	}
	f := (&RetentionPolicy{KeepDaily: 7}).toForgetFlags()
	if !slices.Equal(f, []string{"--keep-daily", "7", "--keep-tag", PinnedTag}) {
		t.Fatalf("flags %v", f)
	}
}
