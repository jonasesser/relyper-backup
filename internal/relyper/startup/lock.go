// Package startup is Relyper-owned: start-up helpers for the server.
package startup

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/garethgeorge/backrest/internal/oplog/sqlitestore"
	"github.com/garethgeorge/backrest/internal/relyper/health"
	"go.uber.org/zap"
)

// LockWait is how long the server waits for the operation log lock
// (RELYPER_LOCK_WAIT_SECONDS, default 300, 0 = do not wait).
func LockWait() time.Duration {
	if v, err := strconv.Atoi(os.Getenv("RELYPER_LOCK_WAIT_SECONDS")); err == nil && v >= 0 {
		return time.Duration(v) * time.Second
	}
	return 5 * time.Minute
}

// WaitForOplogLock retries opening the operation log while another process
// holds its lock. The lock is a kernel file lock: it is released as soon as
// the other process exits, so it is never stale on a local disk. A held lock
// means a second Relyper Backup uses the same data directory, typically the
// old container during a rolling ("zero downtime") deploy.
//
// While it waits, a temporary server on bindAddr answers the health check
// (and every other request with 503 "starting"). Swarm and Easypanel then
// see the new container as healthy and stop the old one, which releases the
// lock; without this a start-first deploy would wait forever.
func WaitForOplogLock(store *sqlitestore.SqliteStore, err error, open func() (*sqlitestore.SqliteStore, error), dataDir, bindAddr string) (*sqlitestore.SqliteStore, error) {
	if errors.Is(err, sqlitestore.ErrLocked) && LockWait() > 0 && bindAddr != "" {
		stop := serveStarting(bindAddr)
		defer stop()
	}
	return waitFor(store, err, open, dataDir, LockWait(), 2*time.Second)
}

func serveStarting(addr string) func() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		zap.S().Warnw("cannot answer health checks while waiting for the lock", "error", err)
		return func() {}
	}
	mux := http.NewServeMux()
	mux.Handle(health.Path, health.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Relyper Backup is starting: waiting for the previous instance to stop.", http.StatusServiceUnavailable)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func waitFor[T any](v T, err error, open func() (T, error), dataDir string, wait, every time.Duration) (T, error) {
	if !errors.Is(err, sqlitestore.ErrLocked) || wait <= 0 {
		return v, err
	}
	deadline := time.Now().Add(wait)
	lastLog := time.Time{}
	for errors.Is(err, sqlitestore.ErrLocked) && time.Now().Before(deadline) {
		if time.Since(lastLog) >= 15*time.Second {
			zap.S().Warnw("another Relyper Backup process uses this data directory; waiting for it to stop. "+
				"Only one instance may use a data directory at a time. On Easypanel turn off \"Zero Downtime\" in the service's deploy settings; "+
				"elsewhere check that no second container mounts the same /data.",
				"data_dir", dataDir, "gives_up_in", time.Until(deadline).Round(time.Second).String())
			lastLog = time.Now()
		}
		time.Sleep(every)
		v, err = open()
	}
	if err == nil {
		zap.S().Infow("operation log lock acquired", "data_dir", dataDir)
	}
	return v, err
}
