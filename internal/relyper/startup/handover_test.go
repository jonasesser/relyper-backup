package startup

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandover(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RELYPER_HANDOVER_MAX_SECONDS", "30")
	var busy atomic.Bool
	busy.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchHandover(ctx, dir, busy.Load)

	if err := requestHandover(dir); err != nil {
		t.Fatal(err)
	}
	w := &waiter{dataDir: dir, start: time.Now(), max: HandoverMaxWait()}
	time.Sleep(2500 * time.Millisecond)
	if w.ready() {
		t.Fatal("ready while the old instance is busy")
	}
	busy.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for !w.ready() {
		if time.Now().After(deadline) {
			t.Fatal("old instance did not confirm")
		}
		time.Sleep(100 * time.Millisecond)
	}
	ClearHandover(dir)
	if _, err := os.Stat(filepath.Join(dir, requestFile)); !os.IsNotExist(err) {
		t.Fatal("request not cleared")
	}
	// An old instance without handover support: the waiter gives up.
	w = &waiter{dataDir: dir, start: time.Now().Add(-time.Minute), max: time.Second}
	if !w.ready() {
		t.Fatal("waiter must report healthy after the maximum wait")
	}
}
