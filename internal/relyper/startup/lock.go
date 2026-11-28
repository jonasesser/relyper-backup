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
// While it waits, it asks the old instance for a handover (see handover.go)
// and a temporary server on bindAddr answers the health check: 503 until the
// old instance is idle, then 200, so the deployment stops the old instance.
// Every other request gets 503 "starting".
func WaitForOplogLock(store *sqlitestore.SqliteStore, err error, open func() (*sqlitestore.SqliteStore, error), dataDir, bindAddr string) (*sqlitestore.SqliteStore, error) {
	if !errors.Is(err, sqlitestore.ErrLocked) || LockWait() <= 0 {
		return store, err
	}
	if rerr := requestHandover(dataDir); rerr != nil {
		zap.S().Warnw("handover: cannot write the request", "error", rerr)
	}
	w := &waiter{dataDir: dataDir, start: time.Now(), max: HandoverMaxWait()}
	if bindAddr != "" {
		stop := serveStarting(bindAddr, w.ready)
		defer stop()
	}
	store, err = waitFor(store, err, open, dataDir, LockWait(), 2*time.Second)
	if err == nil {
		ClearHandover(dataDir)
	}
	return store, err
}

func serveStarting(addr string, healthy func() bool) func() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		zap.S().Warnw("cannot answer health checks while waiting for the lock", "error", err)
		return func() {}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(health.Path, func(w http.ResponseWriter, r *http.Request) {
		if healthy() {
			health.Handler().ServeHTTP(w, r)
			return
		}
		w.Header().Set("Retry-After", "5")
		http.Error(w, `{"status":"waiting for the previous instance"}`, http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Relyper Backup is starting: waiting for the previous instance to hand over.", http.StatusServiceUnavailable)
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
			zap.S().Warnw("another Relyper Backup process uses this data directory; asked it to hand over. "+
				"This is normal during a zero-downtime deploy. If it does not end, check that no second container or replica mounts the same /data.",
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
