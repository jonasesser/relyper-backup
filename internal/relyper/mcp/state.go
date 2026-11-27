// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"
)

// state is small Relyper-owned state kept next to the token store, so the
// upstream config format stays unchanged. Today it only remembers the
// schedule of a plan disabled through MCP.
type state struct {
	DisabledSchedules map[string]*Schedule `json:"disabledSchedules"`
}

var stateMu sync.Mutex

func (s *Server) loadState() state {
	st := state{DisabledSchedules: map[string]*Schedule{}}
	if s.d.StatePath == "" {
		return st
	}
	b, err := os.ReadFile(s.d.StatePath)
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.DisabledSchedules == nil {
		st.DisabledSchedules = map[string]*Schedule{}
	}
	return st
}

func (s *Server) rememberSchedule(planID string, sch *Schedule) {
	stateMu.Lock()
	defer stateMu.Unlock()
	st := s.loadState()
	if sch == nil {
		delete(st.DisabledSchedules, planID)
	} else {
		st.DisabledSchedules[planID] = sch
	}
	if s.d.StatePath == "" {
		return
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.d.StatePath), 0o700)
	if err := os.WriteFile(s.d.StatePath, b, 0o600); err != nil {
		zap.S().Warnf("mcp: save state: %v", err)
	}
}

func (s *Server) rememberedSchedule(planID string) *Schedule {
	stateMu.Lock()
	defer stateMu.Unlock()
	return s.loadState().DisabledSchedules[planID]
}
