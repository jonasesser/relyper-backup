package infra

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/oplog/sqlitestore"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

func TestUsage(t *testing.T) {
	st, err := sqlitestore.NewMemorySqliteStore(t)
	if err != nil {
		t.Fatal(err)
	}
	ol, err := oplog.NewOpLog(st)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &v1.Config{Instance: "i", Repos: []*v1.Repo{{Id: "r", Uri: "sftp:u@h:/x", Guid: "g"}},
		Plans: []*v1.Plan{{Id: "app-ep", Repo: "r"}, {Id: "empty", Repo: "r"}}}
	add := func(op *v1.Operation) {
		op.InstanceId, op.RepoId, op.RepoGuid, op.UnixTimeStartMs = "i", "r", "g", op.UnixTimeStartMs+1
		if op.Status == v1.OperationStatus_STATUS_UNKNOWN {
			op.Status = v1.OperationStatus_STATUS_SUCCESS
		}
		if err := ol.Add(op); err != nil {
			t.Fatal(err)
		}
	}
	snap := func(id string, ms, size, added int64, forgot bool) {
		id = strings.Repeat(id, 64)
		add(&v1.Operation{PlanId: "app-ep", SnapshotId: id, UnixTimeStartMs: ms, Op: &v1.Operation_OperationIndexSnapshot{OperationIndexSnapshot: &v1.OperationIndexSnapshot{
			Forgot: forgot, Snapshot: &v1.ResticSnapshot{Id: id, UnixTimeMs: ms, Summary: &v1.SnapshotSummary{TotalBytesProcessed: size, DataAdded: added}}}}})
	}
	snap("a", 1000, 100, 90, false)
	snap("b", 2000, 120, 30, false)
	snap("c", 500, 80, 80, true) // forgotten: no longer stored
	add(&v1.Operation{PlanId: "_system_", UnixTimeStartMs: 3000, Op: &v1.Operation_OperationStats{OperationStats: &v1.OperationStats{Stats: &v1.RepoStats{TotalSize: 99, SnapshotCount: 2}}}})

	s := &Service{d: Deps{Config: &config.MemoryStore{Config: cfg}, OpLog: ol}}
	s.inv, s.invAt = &discovery.Inventory{}, time.Now()
	u, err := s.Usage(context.Background())
	if err != nil || len(u) != 1 {
		t.Fatalf("%v %v", u, err)
	}
	r := u[0]
	if r.Stored != 99 || r.Snapshots != 2 || r.Location != "sftp:u@h:/x" || len(r.Plans) != 2 {
		t.Fatalf("repo %+v", r)
	}
	p := r.Plans[0]
	if p.Plan != "app-ep" || p.Snapshots != 2 || p.Added != 120 || p.LatestSize != 120 || p.LatestAdded != 30 {
		t.Fatalf("plan %+v", p)
	}
	if r.Plans[1].Plan != "empty" || r.Plans[1].Snapshots != 0 {
		t.Fatalf("empty plan %+v", r.Plans[1])
	}
}
