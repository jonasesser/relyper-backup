// Relyper-owned: how much storage each repository and each plan uses.
//
// The numbers come from restic itself, as recorded in the operation log:
// the repository statistics of the latest "stats" run (size on the
// storage after deduplication and compression) and, per snapshot, restic's
// snapshot summary (size of the backed-up files and the data that snapshot
// added to the repository). Nothing is estimated.
package infra

import (
	"context"
	"sort"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
)

// RepoUsage is the storage use of one repository.
type RepoUsage struct {
	Repo     string `json:"repo"`
	Location string `json:"location"`
	// Stored is the size on the storage (deduplicated and compressed) from
	// the latest stats run; 0 when there was none.
	Stored       int64        `json:"stored_bytes"`
	Uncompressed int64        `json:"uncompressed_bytes,omitempty"`
	Snapshots    int64        `json:"snapshots"`
	StatsAt      string       `json:"stats_at,omitempty"`
	Storage      *StorageInfo `json:"storage,omitempty"`
	Plans        []PlanUsage  `json:"plans"`
	// Pinned snapshots are kept until they are unpinned, newest first.
	Pinned []PinnedSnapshot `json:"pinned"`
}

// PlanUsage is the storage use of one plan in a repository.
type PlanUsage struct {
	Plan string   `json:"plan"`
	Apps []string `json:"apps,omitempty"`
	// Snapshots kept in the repository (not forgotten).
	Snapshots int `json:"snapshots"`
	// LatestSize is the size of the files in the newest snapshot (what a
	// restore writes); LatestAdded what that snapshot added.
	LatestSize  int64  `json:"latest_size_bytes"`
	LatestAdded int64  `json:"latest_added_bytes"`
	LatestAt    string `json:"latest_at,omitempty"`
	// Added is what the kept snapshots added when they were made (restic's
	// data_added, before compression): about the
	// share of the repository this plan occupies (data also used by other
	// plans is counted for the plan that added it first).
	Added int64 `json:"added_bytes"`
	// WithoutSummary counts snapshots made by restic before 0.17, which
	// carry no size summary.
	WithoutSummary int `json:"without_summary,omitempty"`
}

// Usage reports the storage use per repository and plan.
func (s *Service) Usage(ctx context.Context) ([]RepoUsage, error) {
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	apps := map[string][]string{} // plan -> app names
	if cov, err := s.Coverage(ctx); err == nil {
		for _, a := range cov.Apps {
			for _, p := range a.Plans {
				apps[p] = appendUnique(apps[p], a.Name)
			}
		}
	}
	now := time.Now()
	out := []RepoUsage{}
	for _, r := range cfg.Repos {
		ru := RepoUsage{Repo: r.Id, Location: RedactURI(r.Uri), Storage: s.storageNow(r, now), Plans: []PlanUsage{}, Pinned: []PinnedSnapshot{}}
		plans := map[string]*PlanUsage{}
		latest := map[string]int64{}
		for _, p := range cfg.Plans {
			if p.Repo == r.Id {
				plans[p.Id] = &PlanUsage{Plan: p.Id}
			}
		}
		if s.d.OpLog != nil && r.Guid != "" {
			var statsAt int64
			_ = s.d.OpLog.Query(oplog.Query{}.SetRepoGUID(r.Guid), func(op *v1.Operation) error {
				if st := op.GetOperationStats(); st != nil && st.Stats != nil && op.UnixTimeStartMs >= statsAt {
					statsAt = op.UnixTimeStartMs
					ru.Stored, ru.Uncompressed, ru.Snapshots = st.Stats.TotalSize, st.Stats.TotalUncompressedSize, st.Stats.SnapshotCount
					ru.StatsAt = time.UnixMilli(op.UnixTimeStartMs).UTC().Format(time.RFC3339)
				}
				is := op.GetOperationIndexSnapshot()
				if is == nil || is.Forgot || is.Snapshot == nil {
					return nil
				}
				id := op.PlanId
				if id == "" {
					id = "(no plan)"
				}
				pu := plans[id]
				if pu == nil {
					pu = &PlanUsage{Plan: id}
					plans[id] = pu
				}
				pu.Snapshots++
				if IsPinned(is.Snapshot) {
					ps := PinnedSnapshot{ID: is.Snapshot.Id, Plan: op.PlanId, Time: time.UnixMilli(is.Snapshot.UnixTimeMs).UTC().Format(time.RFC3339)}
					if is.Snapshot.Summary != nil {
						ps.Size = is.Snapshot.Summary.TotalBytesProcessed
					}
					ru.Pinned = append(ru.Pinned, ps)
				}
				sum := is.Snapshot.Summary
				if sum == nil {
					pu.WithoutSummary++
					return nil
				}
				pu.Added += sum.DataAdded
				if t := is.Snapshot.UnixTimeMs; t >= latest[id] {
					latest[id] = t
					pu.LatestSize, pu.LatestAdded = sum.TotalBytesProcessed, sum.DataAdded
					pu.LatestAt = time.UnixMilli(t).UTC().Format(time.RFC3339)
				}
				return nil
			})
		}
		for id, pu := range plans {
			pu.Apps = apps[id]
			ru.Plans = append(ru.Plans, *pu)
		}
		sort.Slice(ru.Plans, func(i, j int) bool {
			if ru.Plans[i].Added != ru.Plans[j].Added {
				return ru.Plans[i].Added > ru.Plans[j].Added
			}
			return ru.Plans[i].Plan < ru.Plans[j].Plan
		})
		sort.Slice(ru.Pinned, func(i, j int) bool { return ru.Pinned[i].Time > ru.Pinned[j].Time })
		out = append(out, ru)
	}
	return out, nil
}
