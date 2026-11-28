package startup

import (
	"errors"
	"testing"
	"time"

	"github.com/garethgeorge/backrest/internal/oplog/sqlitestore"
)

func TestWaitFor(t *testing.T) {
	n := 0
	open := func() (int, error) {
		n++
		if n < 3 {
			return 0, sqlitestore.ErrLocked
		}
		return 42, nil
	}
	v, err := waitFor(0, sqlitestore.ErrLocked, open, "/data", time.Second, time.Millisecond)
	if err != nil || v != 42 || n != 3 {
		t.Fatalf("%v %v %d", v, err, n)
	}
	// Gives up after the wait time.
	_, err = waitFor(0, sqlitestore.ErrLocked, func() (int, error) { return 0, sqlitestore.ErrLocked }, "/data", 20*time.Millisecond, time.Millisecond)
	if !errors.Is(err, sqlitestore.ErrLocked) {
		t.Fatal(err)
	}
	// Other errors are returned at once.
	other := errors.New("corrupt")
	if _, err := waitFor(0, other, open, "/data", time.Second, time.Millisecond); err != other {
		t.Fatal(err)
	}
}
