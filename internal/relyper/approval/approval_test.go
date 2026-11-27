// relyper: Relyper Backup addition, not part of upstream Backrest.

package approval

import (
	"errors"
	"testing"
	"time"
)

func newReq(t *testing.T, s *Store, params any) (*Request, string) {
	t.Helper()
	h, err := HashParams("delete_snapshot", params)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Create("delete_snapshot", h, "{}", "Delete snapshot abc", nil, "tok_a", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	return r, h
}

func TestApprovalBinding(t *testing.T) {
	s := NewStore()
	params := map[string]any{"repo_id": "r", "snapshot_id": "abc"}
	r, h := newReq(t, s, params)

	if _, err := s.Consume(r.ID, "delete_snapshot", h, "tok_a"); !errors.Is(err, ErrPending) {
		t.Fatalf("pending request executed: %v", err)
	}
	if _, err := s.Decide(r.ID, true, "jonas", ""); err != nil {
		t.Fatal(err)
	}
	other, _ := HashParams("delete_snapshot", map[string]any{"repo_id": "r", "snapshot_id": "xyz"})
	if _, err := s.Consume(r.ID, "delete_snapshot", other, "tok_a"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("changed parameters accepted: %v", err)
	}
	if _, err := s.Consume(r.ID, "prune_repository", h, "tok_a"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("other tool accepted: %v", err)
	}
	if _, err := s.Consume(r.ID, "delete_snapshot", h, "tok_b"); !errors.Is(err, ErrOtherPrincipal) {
		t.Fatalf("other token accepted: %v", err)
	}
	got, err := s.Consume(r.ID, "delete_snapshot", h, "tok_a")
	if err != nil {
		t.Fatalf("approved request refused: %v", err)
	}
	if got.DecidedBy != "jonas" {
		t.Errorf("decided by %q", got.DecidedBy)
	}
	if _, err := s.Consume(r.ID, "delete_snapshot", h, "tok_a"); !errors.Is(err, ErrUsed) {
		t.Fatalf("approval reused: %v", err)
	}
}

func TestApprovalDeniedAndExpired(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }

	r1, h := newReq(t, s, 1)
	if _, err := s.Decide(r1.ID, false, "jonas", "no"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(r1.ID, "delete_snapshot", h, "tok_a"); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied request executed: %v", err)
	}
	if _, err := s.Decide(r1.ID, true, "jonas", ""); err == nil {
		t.Fatal("decision changed after deny")
	}

	r2, h2 := newReq(t, s, 2)
	if _, err := s.Decide(r2.ID, true, "jonas", ""); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(DefaultTTL + time.Second) }
	if _, err := s.Consume(r2.ID, "delete_snapshot", h2, "tok_a"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired approval executed: %v", err)
	}
	r3, _ := newReq(t, s, 3)
	s.now = func() time.Time { return now.Add(3 * DefaultTTL) }
	if _, err := s.Decide(r3.ID, true, "jonas", ""); err == nil {
		t.Fatal("expired request approved")
	}
}

func TestHashParamsCanonical(t *testing.T) {
	a, _ := HashParams("t", map[string]any{"a": 1, "b": "x"})
	b, _ := HashParams("t", map[string]any{"b": "x", "a": 1})
	c, _ := HashParams("u", map[string]any{"b": "x", "a": 1})
	if a != b {
		t.Error("map order changes hash")
	}
	if a == c {
		t.Error("tool name not part of hash")
	}
}
