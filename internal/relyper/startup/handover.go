// Relyper-owned: handover between an old and a new instance during a
// rolling ("zero downtime") deploy that shares one data directory.
//
// Only one process may use the data directory (the operation log is locked).
// During a start-first deploy the orchestrator (Easypanel, Swarm) starts the
// new container and stops the old one only once the new one is healthy:
//
//  1. The new instance finds the lock held. It writes a handover request
//     file into the data directory and answers health checks with 503
//     ("starting") while it waits.
//  2. The old instance sees the request, waits until no backup, restore,
//     prune or check is running (at most HandoverMaxWait), then writes the
//     ready file. It keeps serving until it is stopped.
//  3. The new instance sees the ready file (or the maximum wait passes) and
//     reports healthy. The orchestrator stops the old instance, the lock is
//     released, the new instance opens the operation log and starts.
//
// Interrupting restic is safe for the repository (an interrupted backup
// leaves no snapshot and is redone), so after the maximum wait the handover
// goes on even if an operation is still running.
package startup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"go.uber.org/zap"
)

const (
	requestFile = "relyper-handover.request"
	readyFile   = "relyper-handover.ready"
)

// HandoverMaxWait bounds how long the new instance waits for running
// operations of the old one (RELYPER_HANDOVER_MAX_SECONDS, default 100). It
// must stay below the time after which the container health check gives up
// (start period 60 s + 3 × 30 s in the image).
func HandoverMaxWait() time.Duration {
	if v, err := strconv.Atoi(os.Getenv("RELYPER_HANDOVER_MAX_SECONDS")); err == nil && v >= 0 {
		return time.Duration(v) * time.Second
	}
	return 100 * time.Second
}

func requestHandover(dataDir string) error {
	host, _ := os.Hostname()
	body := fmt.Sprintf("%s %d %s\n", host, os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = os.Remove(filepath.Join(dataDir, readyFile))
	return os.WriteFile(filepath.Join(dataDir, requestFile), []byte(body), 0o600)
}

func handoverReady(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, readyFile))
	return err == nil
}

// ClearHandover removes the handover files once the new instance owns the
// data directory.
func ClearHandover(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, requestFile))
	_ = os.Remove(filepath.Join(dataDir, readyFile))
}

// waiter is the new instance's side: ready reports whether health checks
// may pass.
type waiter struct {
	dataDir string
	start   time.Time
	max     time.Duration
	logged  atomic.Bool
}

func (w *waiter) ready() bool {
	if handoverReady(w.dataDir) {
		if !w.logged.Swap(true) {
			zap.S().Infow("handover: the previous instance is idle; reporting healthy so it is stopped")
		}
		return true
	}
	if time.Since(w.start) >= w.max {
		if !w.logged.Swap(true) {
			zap.S().Warnw("handover: the previous instance did not confirm in time (old version or long operation); reporting healthy anyway",
				"waited", w.max.String())
		}
		return true
	}
	return false
}

// WatchHandover is the old instance's side. It runs until ctx ends. busy
// reports whether an operation is running.
func WatchHandover(ctx context.Context, dataDir string, busy func() bool) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	confirmed := false
	since := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b, err := os.ReadFile(filepath.Join(dataDir, requestFile))
		if err != nil {
			confirmed, since = false, time.Time{}
			continue
		}
		if confirmed {
			continue
		}
		if since.IsZero() {
			since = time.Now()
			zap.S().Infow("handover requested by a new instance; finishing running operations", "request", string(b))
		}
		if busy() && time.Since(since) < HandoverMaxWait() {
			continue
		}
		if err := os.WriteFile(filepath.Join(dataDir, readyFile), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
			zap.S().Warnw("handover: cannot confirm", "error", err)
			continue
		}
		confirmed = true
		zap.S().Infow("handover confirmed; this instance will be stopped by the deployment")
	}
}

// OpLogBusy reports whether the operation log has an operation in progress
// that started within the last two days (older ones are leftovers that the
// next start marks as failed).
func OpLogBusy(log *oplog.OpLog) func() bool {
	return func() bool {
		busy := false
		since := time.Now().Add(-48 * time.Hour).UnixMilli()
		stop := errors.New("stop")
		_ = log.Query(oplog.Query{}.SetReversed(true), func(op *v1.Operation) error {
			if op.UnixTimeStartMs < since {
				return stop
			}
			if op.Status == v1.OperationStatus_STATUS_INPROGRESS {
				busy = true
				return stop
			}
			return nil
		})
		return busy
	}
}
