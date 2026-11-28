// Relyper-owned: monitoring that feeds notifications and the dashboard:
// failed (and optionally successful) operations of every plan and
// repository, overdue plans and free space on the backup storage.
package infra

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/relyper/sshdiag"
)

// opKind names the type of an operation.
func opKind(op *v1.Operation) string {
	switch {
	case op.GetOperationBackup() != nil:
		return "backup"
	case op.GetOperationCheck() != nil:
		return "check"
	case op.GetOperationPrune() != nil:
		return "prune"
	case op.GetOperationForget() != nil:
		return "forget"
	case op.GetOperationRestore() != nil:
		return "restore"
	case op.GetOperationRunHook() != nil:
		return "hook"
	}
	return ""
}

// operationMessage returns the notification for a finished operation, or
// "" when it is not worth one.
func operationMessage(op *v1.Operation, notifySuccess bool) (subject, body string) {
	kind := opKind(op)
	if kind == "" || kind == "hook" {
		return "", "" // hook failures surface as failed backups
	}
	where := "repository " + op.RepoId
	if op.PlanId != "" && op.PlanId != "_system_" && op.PlanId != "_unassociated_" {
		where = "plan " + op.PlanId + " (" + op.RepoId + ")"
	}
	switch op.Status {
	case v1.OperationStatus_STATUS_ERROR:
		return fmt.Sprintf("Relyper Backup: %s of %s FAILED", kind, where), firstLine(op.DisplayMessage)
	case v1.OperationStatus_STATUS_WARNING:
		if kind == "backup" {
			return fmt.Sprintf("Relyper Backup: backup of %s finished with warnings", where), firstLine(op.DisplayMessage)
		}
	case v1.OperationStatus_STATUS_SUCCESS:
		if notifySuccess && kind == "backup" {
			b := op.GetOperationBackup().GetLastStatus().GetSummary()
			msg := "Backup completed."
			if b != nil {
				msg = fmt.Sprintf("Snapshot %s: %d files, %s added.", short(b.SnapshotId), b.TotalFilesProcessed, humanSize(b.DataAdded))
			}
			return fmt.Sprintf("Relyper Backup: backup of %s succeeded", where), msg
		}
	}
	return "", ""
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// watchOperations notifies about finished operations as they finish.
func (s *Service) watchOperations(ctx context.Context) {
	if s.d.OpLog == nil || len(s.s.NotifyURLs) == 0 {
		return
	}
	sent := map[int64]bool{}
	ch := make(chan *v1.Operation, 64)
	sub := oplog.Subscription(func(ops []*v1.Operation, ev oplog.OperationEvent) {
		if ev == oplog.OPERATION_DELETED {
			return
		}
		for _, op := range ops {
			switch op.Status {
			case v1.OperationStatus_STATUS_ERROR, v1.OperationStatus_STATUS_WARNING, v1.OperationStatus_STATUS_SUCCESS:
				select {
				case ch <- op:
				default: // never block the operation log
				}
			}
		}
	})
	s.d.OpLog.Subscribe(oplog.Query{}, &sub)
	defer s.d.OpLog.Unsubscribe(&sub)
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-ch:
			if sent[op.Id] {
				continue
			}
			if subject, body := operationMessage(op, s.s.NotifySuccess); subject != "" {
				sent[op.Id] = true
				s.notify(subject, body)
			}
			if len(sent) > 10000 {
				sent = map[int64]bool{}
			}
		}
	}
}

// StorageInfo is the free space of a repository's storage.
type StorageInfo struct {
	FreeBytes  int64  `json:"free_bytes"`
	TotalBytes int64  `json:"total_bytes"`
	CheckedAt  string `json:"checked_at"`
	Source     string `json:"source"` // filesystem or sftp
	Low        bool   `json:"low"`
}

// localRepoPath returns the directory of a local repository.
func localRepoPath(uri string) string {
	p := strings.TrimPrefix(uri, "local:")
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return ""
}

// statfs reports free and total bytes of the filesystem holding p (or its
// nearest existing parent).
func statfs(p string) (free, total int64, err error) {
	for p != "/" {
		if _, err := os.Stat(p); err == nil {
			break
		}
		p = path.Dir(p)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), nil
}

// sftpTarget parses an sftp repository URI and its sftp.args flag.
func sftpTarget(r *v1.Repo) (host string, port int, user, dir, identity string, ok bool) {
	uri := strings.TrimPrefix(r.Uri, "sftp:")
	if uri == r.Uri {
		return
	}
	if strings.HasPrefix(uri, "//") {
		u, err := url.Parse("sftp:" + uri)
		if err != nil {
			return
		}
		host, user, dir = u.Hostname(), u.User.Username(), u.Path
		port, _ = strconv.Atoi(u.Port())
		dir = strings.TrimPrefix(dir, "/") // sftp://host//abs or sftp://host/rel
	} else {
		hp, d, found := strings.Cut(uri, ":")
		if !found {
			return
		}
		dir = d
		if u, h, has := strings.Cut(hp, "@"); has {
			user, host = u, h
		} else {
			host = hp
		}
	}
	for _, f := range r.Flags {
		if !strings.Contains(f, "sftp.args=") {
			continue
		}
		args := strings.Fields(strings.Trim(f[strings.Index(f, "sftp.args=")+len("sftp.args="):], `'"`))
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "-i":
				identity = strings.Trim(args[i+1], `'"`)
			case "-p":
				if port == 0 {
					port, _ = strconv.Atoi(args[i+1])
				}
			}
		}
	}
	return host, port, user, dir, identity, host != ""
}

// checkStorage measures free space of every repository. Local repositories
// are measured every round, SFTP storage at most once a day (it needs a
// login). Other backends do not report free space.
func (s *Service) checkStorage(ctx context.Context, cfg *v1.Config, now time.Time) {
	for _, r := range cfg.Repos {
		s.mu.Lock()
		prev, had := s.storage[r.Id]
		s.mu.Unlock()
		var info *StorageInfo
		switch repoType(r.Uri) {
		case "local":
			if p := localRepoPath(r.Uri); p != "" {
				if free, total, err := statfs(p); err == nil {
					info = &StorageInfo{FreeBytes: free, TotalBytes: total, Source: "filesystem"}
				}
			}
		case "sftp":
			if had && prev.Source == "sftp" && !olderThan(prev.CheckedAt, now, 24*time.Hour) {
				continue
			}
			host, port, user, dir, identity, ok := sftpTarget(r)
			if !ok || identity == "" {
				continue // password logins are not stored outside restic's own config
			}
			rep := sshdiag.TestConnection(ctx, sshdiag.Options{Host: host, Port: port, User: user, IdentityFile: identity, RemotePath: dir,
				KnownHostsPath: s.s.KnownHostsPath, Timeout: 30 * time.Second})
			if rep.TotalBytes > 0 && rep.FreeBytes >= 0 {
				info = &StorageInfo{FreeBytes: rep.FreeBytes, TotalBytes: rep.TotalBytes, Source: "sftp"}
			}
		}
		if info == nil {
			continue
		}
		info.CheckedAt = fmtTime(now)
		info.Low = s.s.LowStoragePercent > 0 && float64(info.FreeBytes) < float64(info.TotalBytes)*float64(s.s.LowStoragePercent)/100
		s.mu.Lock()
		s.storage[r.Id] = *info
		s.mu.Unlock()
		if info.Low && (!had || !prev.Low) {
			s.notify("Relyper Backup: storage of repository "+r.Id+" is almost full",
				fmt.Sprintf("%s free of %s (less than %d%%). Backups fail when it is full.", humanSize(info.FreeBytes), humanSize(info.TotalBytes), s.s.LowStoragePercent))
		} else if !info.Low && had && prev.Low {
			s.notify("Relyper Backup: storage of repository "+r.Id+" has enough space again", humanSize(info.FreeBytes)+" free.")
		}
	}
}

// Storage returns the last measured free space of a repository.
func (s *Service) Storage(repoID string) (StorageInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.storage[repoID]
	return i, ok
}

// checkOverduePlans notifies when a scheduled plan's latest snapshot gets
// older than the maximum age, also for plans that belong to no app.
func (s *Service) checkOverduePlans(cfg *v1.Config, now time.Time) {
	for _, p := range cfg.Plans {
		if p.GetSchedule() == nil || p.GetSchedule().GetDisabled() {
			continue
		}
		run := s.planRun(cfg, p.Id)
		overdue := run.snapshot != nil && now.Sub(time.UnixMilli(run.snapshot.UnixTimeMs)) > s.s.CoverageMaxAge
		key := "plan:" + p.Id
		state := "ok"
		if overdue {
			state = StateStale
		}
		s.mu.Lock()
		prev, seen := s.notified[key]
		s.notified[key] = state
		s.mu.Unlock()
		switch {
		case overdue && prev != StateStale:
			s.notify("Relyper Backup: plan "+p.Id+" is overdue",
				fmt.Sprintf("The latest snapshot is from %s, older than %d hours.", fmtTime(time.UnixMilli(run.snapshot.UnixTimeMs)), int(s.s.CoverageMaxAge.Hours())))
		case !overdue && seen && prev == StateStale:
			s.notify("Relyper Backup: plan "+p.Id+" is up to date again", "")
		}
	}
}
