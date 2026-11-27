package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
)

// VerifyRecord is a database verification from a snapshot.
type VerifyRecord struct {
	App          string                `json:"app"`
	Plan         string                `json:"plan"`
	Repo         string                `json:"repo"`
	SnapshotID   string                `json:"snapshot_id"`
	SnapshotTime string                `json:"snapshot_time"`
	Time         string                `json:"time"`
	OK           bool                  `json:"ok"`
	Error        string                `json:"error,omitempty"`
	Results      []dbdump.VerifyResult `json:"results,omitempty"`
	Trigger      string                `json:"trigger"` // manual, schedule
}

// RestoreTestRecord is a file restore test from a snapshot.
type RestoreTestRecord struct {
	Plan         string         `json:"plan"`
	Repo         string         `json:"repo"`
	SnapshotID   string         `json:"snapshot_id"`
	SnapshotTime string         `json:"snapshot_time"`
	Time         string         `json:"time"`
	OK           bool           `json:"ok"`
	Error        string         `json:"error,omitempty"`
	Files        []RestoredFile `json:"files,omitempty"`
	Trigger      string         `json:"trigger"`
}

// RestoredFile is one sample file of a restore test.
type RestoredFile struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Service) tmpDir(prefix string) (string, error) {
	base := filepath.Join(s.d.DataDir, "tmp")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, prefix)
}

func (s *Service) plan(cfg *v1.Config, id string) *v1.Plan {
	for _, p := range cfg.Plans {
		if p.Id == id {
			return p
		}
	}
	return nil
}

// VerifyApp restores the database dumps of the app from the latest snapshot
// of its app plan into throwaway containers and checks their content.
func (s *Service) VerifyApp(ctx context.Context, appID, trigger, actor string) (*VerifyRecord, error) {
	if s.docker == nil {
		return nil, errors.New("docker is not available")
	}
	if !s.lock("verify:" + appID) {
		return nil, errors.New("a verification of this app is already running")
	}
	defer s.unlock("verify:" + appID)
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	rec := &VerifyRecord{App: appID, Time: fmtTime(time.Now()), Trigger: trigger}
	var plan *v1.Plan
	var run planRun
	var dumpBase string
	for _, p := range cfg.Plans {
		id, dir := HookApp(p)
		if id != appID {
			continue
		}
		r := s.planRun(cfg, p.Id)
		if r.snapshot != nil && (run.snapshot == nil || r.snapshot.UnixTimeMs > run.snapshot.UnixTimeMs) {
			plan, run, dumpBase = p, r, dir
		}
	}
	if plan == nil {
		return nil, fmt.Errorf("no snapshot with database dumps of %s yet: create an app plan and run a backup first", appID)
	}
	rec.Plan, rec.Repo, rec.SnapshotID = plan.Id, plan.Repo, run.snapshot.Id
	rec.SnapshotTime = fmtTime(time.UnixMilli(run.snapshot.UnixTimeMs))
	finish := func(err error) (*VerifyRecord, error) {
		if err != nil {
			rec.OK, rec.Error = false, err.Error()
		}
		if serr := s.store.addVerification(*rec); serr != nil {
			return rec, serr
		}
		outcome := "ok"
		if !rec.OK {
			outcome = "failed"
			s.notify(fmt.Sprintf("Relyper Backup: database restore test of %s FAILED", appID), verifySummary(rec))
		}
		s.record(actor, "verify.databases", outcome, fmt.Sprintf("%s snapshot %s: %s", appID, short(rec.SnapshotID), verifySummary(rec)))
		return rec, nil
	}
	repo, err := s.d.Orchestrator.GetRepoOrchestrator(plan.Repo)
	if err != nil {
		return finish(err)
	}
	dir := dbdump.Dir(dumpBase, appID)
	var mb bytes.Buffer
	if err := repo.Dump(ctx, run.snapshot.Id, path.Join(dir, dbdump.ManifestName), &mb); err != nil {
		return finish(fmt.Errorf("reading the dump manifest from the snapshot: %w", err))
	}
	var m dbdump.Manifest
	if err := json.Unmarshal(mb.Bytes(), &m); err != nil {
		return finish(fmt.Errorf("the dump manifest in the snapshot is unreadable: %w", err))
	}
	tmp, err := s.tmpDir("verify-")
	if err != nil {
		return finish(err)
	}
	defer os.RemoveAll(tmp)
	rec.OK = len(m.Dumps) > 0
	for _, d := range m.Dumps {
		local := filepath.Join(tmp, filepath.Base(d.File))
		f, err := os.OpenFile(local, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return finish(err)
		}
		err = repo.Dump(ctx, run.snapshot.Id, path.Join(dir, d.File), f)
		f.Close()
		if err != nil {
			rec.OK = false
			rec.Results = append(rec.Results, dbdump.VerifyResult{Service: d.Service, Engine: d.Engine, Image: d.Image, Problems: []string{"reading the dump from the snapshot failed: " + firstLine(err.Error())}})
			continue
		}
		vctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		r, err := dbdump.Verify(vctx, s.docker, d, local, nil)
		cancel()
		_ = os.Remove(local)
		if err != nil {
			rec.OK = false
			rec.Results = append(rec.Results, dbdump.VerifyResult{Service: d.Service, Engine: d.Engine, Image: d.Image, Problems: []string{firstLine(err.Error())}})
			continue
		}
		rec.Results = append(rec.Results, *r)
		if !r.OK {
			rec.OK = false
		}
	}
	return finish(nil)
}

func verifySummary(r *VerifyRecord) string {
	if r.Error != "" {
		return r.Error
	}
	ok := 0
	var problems []string
	for _, x := range r.Results {
		if x.OK {
			ok++
		} else {
			for _, p := range x.Problems {
				problems = append(problems, x.Service+": "+p)
			}
		}
	}
	msg := fmt.Sprintf("%d of %d databases restored and checked", ok, len(r.Results))
	if len(problems) > 0 {
		msg += "; " + fmt.Sprint(problems)
	}
	return msg
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

const (
	sampleFiles    = 5
	sampleMaxBytes = 256 << 20
	listBudget     = 40 // directory listings per test
)

// RestoreTest restores a few sample files of the latest snapshot of a plan
// into a temporary directory and checks their sizes. It proves that the
// repository, its password and the data are usable for a restore.
func (s *Service) RestoreTest(ctx context.Context, planID, trigger, actor string) (*RestoreTestRecord, error) {
	if !s.lock("restoretest:" + planID) {
		return nil, errors.New("a restore test of this plan is already running")
	}
	defer s.unlock("restoretest:" + planID)
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	plan := s.plan(cfg, planID)
	if plan == nil {
		return nil, fmt.Errorf("plan %q not found", planID)
	}
	run := s.planRun(cfg, planID)
	if run.snapshot == nil {
		return nil, fmt.Errorf("plan %q has no snapshot yet", planID)
	}
	rec := &RestoreTestRecord{Plan: planID, Repo: plan.Repo, SnapshotID: run.snapshot.Id, SnapshotTime: fmtTime(time.UnixMilli(run.snapshot.UnixTimeMs)), Time: fmtTime(time.Now()), Trigger: trigger}
	finish := func(err error) (*RestoreTestRecord, error) {
		if err != nil {
			rec.OK, rec.Error = false, err.Error()
		}
		if serr := s.store.addRestoreTest(*rec); serr != nil {
			return rec, serr
		}
		outcome := "ok"
		if !rec.OK {
			outcome = "failed"
			s.notify(fmt.Sprintf("Relyper Backup: restore test of plan %s FAILED", planID), restoreSummary(rec))
		}
		s.record(actor, "restore_test", outcome, fmt.Sprintf("plan %s snapshot %s: %s", planID, short(rec.SnapshotID), restoreSummary(rec)))
		return rec, nil
	}
	repo, err := s.d.Orchestrator.GetRepoOrchestrator(plan.Repo)
	if err != nil {
		return finish(err)
	}
	// Pick sample files: walk the snapshot breadth-first, take files from
	// different directories.
	type cand struct {
		path string
		size int64
	}
	var picked []cand
	var total int64
	queue := append([]string(nil), run.snapshot.Paths...)
	sort.Strings(queue)
	listings := 0
	for len(queue) > 0 && len(picked) < sampleFiles && listings < listBudget {
		dir := queue[0]
		queue = queue[1:]
		listings++
		entries, err := repo.ListSnapshotFiles(ctx, run.snapshot.Id, dir)
		if err != nil {
			return finish(fmt.Errorf("listing %s in the snapshot: %w", dir, err))
		}
		took := false
		for _, e := range entries {
			if e.Path == dir {
				continue
			}
			switch e.Type {
			case "dir":
				queue = append(queue, e.Path)
			case "file":
				if !took && e.Size > 0 && total+e.Size <= sampleMaxBytes && len(picked) < sampleFiles {
					picked = append(picked, cand{e.Path, e.Size})
					total += e.Size
					took = true
				}
			}
		}
	}
	if len(picked) == 0 {
		return finish(errors.New("the snapshot contains no regular file to test with"))
	}
	tmp, err := s.tmpDir("restoretest-")
	if err != nil {
		return finish(err)
	}
	defer os.RemoveAll(tmp)
	rec.OK = true
	for i, c := range picked {
		target := filepath.Join(tmp, fmt.Sprint(i))
		rf := RestoredFile{Path: c.path, Size: c.size}
		if _, err := repo.Restore(ctx, run.snapshot.Id, c.path, target, nil); err != nil {
			rf.Error = firstLine(err.Error())
		} else if fi, err := os.Stat(filepath.Join(target, path.Base(c.path))); err != nil {
			rf.Error = "restored file not found"
		} else if fi.Size() != c.size {
			rf.Error = fmt.Sprintf("restored size %d, expected %d", fi.Size(), c.size)
		} else {
			rf.OK = true
		}
		if !rf.OK {
			rec.OK = false
		}
		rec.Files = append(rec.Files, rf)
	}
	return finish(nil)
}

func restoreSummary(r *RestoreTestRecord) string {
	if r.Error != "" {
		return r.Error
	}
	ok := 0
	for _, f := range r.Files {
		if f.OK {
			ok++
		}
	}
	return fmt.Sprintf("%d of %d sample files restored with the expected size", ok, len(r.Files))
}

// Verifications returns the stored database verifications of an app, newest
// first.
func (s *Service) Verifications(appID string) []VerifyRecord { return s.store.verifications(appID) }

// RestoreTests returns the stored restore tests of a plan, newest first.
func (s *Service) RestoreTests(planID string) []RestoreTestRecord {
	return s.store.restoreTests(planID)
}
