// Package audit writes an append-only JSON Lines audit log of MCP tool calls,
// token changes and approval decisions. Parameters are redacted before they
// are written; secrets never reach the file.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Event is one audit record.
type Event struct {
	Time       time.Time `json:"time"`
	Actor      string    `json:"actor"`   // "token:<id> (<name>)" or "user:<name>"
	Source     string    `json:"source"`  // "mcp", "ui", "oauth"
	Action     string    `json:"action"`  // tool name or e.g. "token.create"
	Outcome    string    `json:"outcome"` // "ok", "error", "denied", "approval_requested", "forbidden"
	Params     string    `json:"params,omitempty"`
	Message    string    `json:"message,omitempty"`
	ApprovalID string    `json:"approvalId,omitempty"`
	RemoteAddr string    `json:"remoteAddr,omitempty"`
}

// Log is safe for concurrent use. The file is rotated once at maxSize; one
// previous generation (<path>.1) is kept.
type Log struct {
	path    string
	maxSize int64
	mu      sync.Mutex
}

// Open returns a log writing to path.
func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &Log{path: path, maxSize: 10 << 20}, nil
}

// Path returns the file the log is written to.
func (l *Log) Path() string { return l.path }

// Record appends an event. Failures are logged, not returned: auditing must
// not make an otherwise valid call fail silently, so callers record before
// and after executing.
func (l *Log) Record(e Event) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		zap.S().Errorf("audit: marshal: %v", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, err := os.Stat(l.path); err == nil && st.Size() > l.maxSize {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		zap.S().Errorf("audit: open %s: %v", l.path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		zap.S().Errorf("audit: write: %v", err)
	}
}

// Recent returns up to limit events, newest first.
func (l *Log) Recent(limit int) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []Event
	for _, p := range []string{l.path + ".1", l.path} {
		f, err := os.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				all = append(all, e)
			}
		}
		f.Close()
	}
	slices.Reverse(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
