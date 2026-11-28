package portal

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/relyper/audit"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
)

// APIPrefix is the path of the portal API. It is mounted without the web UI
// authentication; the handler accepts portal sessions only.
const APIPrefix = "/relyper/portal/api/"

// Deps are what the portal needs. Functions keep it testable.
type Deps struct {
	Store  *tenant.Store
	Gate   *tenant.Gate
	Verify func(token string) (*identity.PortalSession, bool)
	Config func() (*v1.Config, error)
	OpLog  *oplog.OpLog
	Audit  *audit.Log
	// StartBackup queues a one-off backup and returns at once.
	StartBackup func(plan *v1.Plan) error
	// Notifier sends test mails; nil when SMTP is not configured.
	Notifier *Notifier
	// StaleAfter marks a plan stale without a successful backup (default 36h).
	StaleAfter time.Duration
	Now        func() time.Time
}

// Handler serves the portal API.
type Handler struct {
	d Deps

	mu     sync.Mutex
	recent map[string][]time.Time // tenant -> times of portal-started actions
}

// New returns the portal handler.
func New(d Deps) *Handler {
	if d.StaleAfter <= 0 {
		d.StaleAfter = 36 * time.Hour
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Handler{d: d, recent: map[string][]time.Time{}}
}

// member is the verified caller of a request.
type member struct {
	session *identity.PortalSession
	tenant  *tenant.Tenant
	admin   bool
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (h *Handler) authenticate(r *http.Request) (*member, bool) {
	if h.d.Verify == nil || h.d.Gate == nil {
		return nil, false
	}
	p, ok := h.d.Verify(bearer(r))
	if !ok {
		return nil, false
	}
	t, admin, ok := h.d.Gate.Check(p)
	if !ok {
		return nil, false
	}
	return &member{session: p, tenant: t, admin: admin}, true
}

func (h *Handler) record(r *http.Request, m *member, action, outcome, msg string) {
	if h.d.Audit == nil {
		return
	}
	actor := "anonymous"
	if m != nil {
		actor = "user:" + m.session.UserName() + " (tenant " + m.tenant.ID + ")"
	}
	h.d.Audit.Record(audit.Event{Actor: actor, Source: "portal", Action: action, Outcome: outcome, Message: msg, RemoteAddr: remoteAddr(r)})
}

func remoteAddr(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return strings.Trim(r.RemoteAddr[:i], "[]")
	}
	return r.RemoteAddr
}

// allowAction limits portal-started backups, restores and storage tests per
// tenant: at most 6 per hour.
func (h *Handler) allowAction(tenantID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.d.Now()
	var keep []time.Time
	for _, t := range h.recent[tenantID] {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 6 {
		h.recent[tenantID] = keep
		return false
	}
	h.recent[tenantID] = append(keep, now)
	return true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	m, ok := h.authenticate(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "sign in to the customer portal")
		return
	}
	route := strings.Trim(strings.TrimPrefix(r.URL.Path, APIPrefix), "/")
	parts := strings.Split(route, "/")
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
		// Requests with a body must be JSON: a cross-site form post cannot
		// set this type without a CORS preflight, which is refused.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			fail(w, http.StatusUnsupportedMediaType, "content type must be application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	}
	switch {
	case r.Method == http.MethodGet && route == "me":
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant": TenantInfo{ID: m.tenant.ID, Name: m.tenant.Name, Escrow: m.tenant.Escrow},
			"user":   map[string]string{"email": m.session.Email, "name": m.session.Name},
			"admin":  m.admin,
			"features": map[string]bool{
				"email":   h.d.Notifier != nil,
			},
		})
	case r.Method == http.MethodGet && route == "overview":
		ov, err := h.overview(m.tenant)
		respond(w, ov, err)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "plans" && parts[2] == "snapshots":
		v, err := h.snapshots(m.tenant, parts[1])
		respond(w, v, err)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "plans" && parts[2] == "runs":
		v, err := h.runs(m.tenant, parts[1], r.URL.Query().Get("limit"))
		respond(w, v, err)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "plans" && parts[2] == "backup":
		if !h.requireAdmin(w, r, m, "portal.backup") {
			return
		}
		h.backup(w, r, m, parts[1])
	case r.Method == http.MethodGet && route == "notifications":
		writeJSON(w, http.StatusOK, map[string]any{"notify": m.tenant.Notify, "events": tenant.AllEvents, "available": h.d.Notifier != nil})
	case r.Method == http.MethodPut && route == "notifications":
		if !h.requireAdmin(w, r, m, "portal.notify.set") {
			return
		}
		h.setNotify(w, r, m)
	case r.Method == http.MethodPost && route == "notifications/test":
		if !h.requireAdmin(w, r, m, "portal.notify.test") {
			return
		}
		h.testNotify(w, r, m)
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request, m *member, action string) bool {
	if m.admin {
		return true
	}
	h.record(r, m, action, "forbidden", "customer admin role required")
	fail(w, http.StatusForbidden, "this needs the customer admin role")
	return false
}

func respond(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, errNotFound):
		fail(w, http.StatusNotFound, "not found")
	case err != nil:
		fail(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, v)
	}
}

func (h *Handler) scope(t *tenant.Tenant) (*scope, error) {
	cfg, err := h.d.Config()
	if err != nil {
		return nil, err
	}
	return newScope(cfg, t), nil
}

// summary of the runs of a plan.
type summary struct {
	last        *v1.Operation // latest finished backup
	running     bool
	lastSuccess int64
	snapshots   int
}

func (h *Handler) summarize(s *scope, p *v1.Plan) summary {
	var sum summary
	if h.d.OpLog == nil {
		return sum
	}
	_ = h.d.OpLog.Query(s.planQuery(p).SetReversed(true), func(op *v1.Operation) error {
		if !s.opBelongs(p, op) {
			return nil
		}
		if is := op.GetOperationIndexSnapshot(); is != nil && !is.GetForgot() && is.GetSnapshot() != nil {
			sum.snapshots++
		}
		b := op.GetOperationBackup()
		if b == nil || b.GetDryRun() {
			return nil
		}
		switch st := op.GetStatus(); {
		case st == v1.OperationStatus_STATUS_INPROGRESS:
			sum.running = true
		case finished(st):
			if sum.last == nil {
				sum.last = op
			}
			if sum.lastSuccess == 0 && st != v1.OperationStatus_STATUS_ERROR {
				sum.lastSuccess = op.GetUnixTimeEndMs()
				if sum.lastSuccess == 0 {
					sum.lastSuccess = op.GetUnixTimeStartMs()
				}
			}
		}
		return nil
	})
	return sum
}

// planState derives the state shown to customers.
func planState(p *v1.Plan, sum summary, now time.Time, staleAfter time.Duration) string {
	switch {
	case sum.running:
		return StateRunning
	case sum.last == nil:
		if p.GetSchedule() == nil || p.GetSchedule().GetDisabled() {
			return StateDisabled
		}
		return StateNever
	case sum.last.GetStatus() == v1.OperationStatus_STATUS_ERROR:
		return StateFailing
	case p.GetSchedule() != nil && !p.GetSchedule().GetDisabled() && sum.lastSuccess > 0 &&
		now.Sub(time.UnixMilli(sum.lastSuccess)) > staleAfter:
		return StateStale
	case sum.last.GetStatus() == v1.OperationStatus_STATUS_WARNING:
		return StateWarning
	case p.GetSchedule() == nil || p.GetSchedule().GetDisabled():
		return StateDisabled
	}
	return StateOK
}

func (h *Handler) overview(t *tenant.Tenant) (*Overview, error) {
	s, err := h.scope(t)
	if err != nil {
		return nil, err
	}
	sc := NewScrubber(s.cfg)
	now := h.d.Now()
	ov := &Overview{Tenant: TenantInfo{ID: t.ID, Name: t.Name, Escrow: t.Escrow}, Plans: []PlanView{}, Repos: []RepoView{}, Problems: []Problem{}}
	for _, p := range s.plans {
		sum := h.summarize(s, p)
		v := PlanView{ID: p.GetId(), Repo: p.GetRepo(), Paths: append([]string{}, p.GetPaths()...), Schedule: scheduleText(p.GetSchedule()),
			Retention: retentionText(p.GetRetention()), OwnStorage: t.IsOwnRepo(p.GetRepo()), Snapshots: sum.snapshots, LastSuccessAt: sum.lastSuccess}
		if sum.last != nil {
			v.LastRun = runView(sum.last, sc)
		}
		v.State = planState(p, sum, now, h.d.StaleAfter)
		ov.Plans = append(ov.Plans, v)
		switch v.State {
		case StateFailing:
			msg := "The last backup failed."
			if v.LastRun != nil && v.LastRun.Message != "" {
				msg += " " + v.LastRun.Message
			}
			ov.Problems = append(ov.Problems, Problem{Plan: v.ID, State: v.State, Message: msg})
		case StateStale:
			ov.Problems = append(ov.Problems, Problem{Plan: v.ID, State: v.State, Message: "No successful backup for more than " + strconv.Itoa(int(h.d.StaleAfter.Hours())) + " hours."})
		case StateNever:
			ov.Problems = append(ov.Problems, Problem{Plan: v.ID, State: v.State, Message: "This plan has not run yet."})
		case StateWarning:
			ov.Problems = append(ov.Problems, Problem{Plan: v.ID, State: v.State, Message: "The last backup finished with warnings: some files could not be read."})
		}
	}
	for _, r := range s.repos {
		rv := RepoView{ID: r.GetId(), Type: RepoType(r.GetUri()), Location: RedactURI(r.GetUri()), Own: t.IsOwnRepo(r.GetId())}
		if k := t.Kit(r.GetId()); k != nil {
			rv.Kit = &KitView{KeyID: k.KeyID, CreatedAt: k.CreatedAt, CreatedBy: k.CreatedBy, Escrowed: k.Escrowed}
		}
		ov.Repos = append(ov.Repos, rv)
	}
	return ov, nil
}

func (h *Handler) snapshots(t *tenant.Tenant, planID string) ([]SnapshotView, error) {
	s, err := h.scope(t)
	if err != nil {
		return nil, err
	}
	p := s.plan(planID)
	if p == nil {
		return nil, errNotFound
	}
	out := []SnapshotView{}
	if h.d.OpLog == nil {
		return out, nil
	}
	err = h.d.OpLog.Query(s.planQuery(p).SetReversed(true), func(op *v1.Operation) error {
		is := op.GetOperationIndexSnapshot()
		if is == nil || is.GetForgot() || is.GetSnapshot() == nil || !s.opBelongs(p, op) {
			return nil
		}
		sn := is.GetSnapshot()
		v := SnapshotView{ID: sn.GetId(), Time: sn.GetUnixTimeMs(), Hostname: sn.GetHostname(), Paths: append([]string{}, sn.GetPaths()...), Tags: append([]string{}, sn.GetTags()...)}
		if sm := sn.GetSummary(); sm != nil {
			v.Bytes = sm.GetTotalBytesProcessed()
			v.Files = sm.GetTotalFilesProcessed()
		}
		out = append(out, v)
		if len(out) >= 500 {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return out, err
}

var errStop = errors.New("stop")

func (h *Handler) runs(t *tenant.Tenant, planID, limit string) ([]*RunView, error) {
	s, err := h.scope(t)
	if err != nil {
		return nil, err
	}
	p := s.plan(planID)
	if p == nil {
		return nil, errNotFound
	}
	n, _ := strconv.Atoi(limit)
	if n <= 0 || n > 200 {
		n = 50
	}
	sc := NewScrubber(s.cfg)
	out := []*RunView{}
	if h.d.OpLog == nil {
		return out, nil
	}
	err = h.d.OpLog.Query(s.planQuery(p).SetReversed(true), func(op *v1.Operation) error {
		b := op.GetOperationBackup()
		if b == nil || b.GetDryRun() || !s.opBelongs(p, op) {
			return nil
		}
		out = append(out, runView(op, sc))
		if len(out) >= n {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return out, err
}

func (h *Handler) backup(w http.ResponseWriter, r *http.Request, m *member, planID string) {
	s, err := h.scope(m.tenant)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal error")
		return
	}
	p := s.plan(planID)
	if p == nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if h.d.StartBackup == nil {
		fail(w, http.StatusServiceUnavailable, "backups cannot be started from the portal")
		return
	}
	if sum := h.summarize(s, p); sum.running {
		fail(w, http.StatusConflict, "a backup of this plan is already running")
		return
	}
	if !h.allowAction(m.tenant.ID) {
		h.record(r, m, "portal.backup", "denied", "rate limit")
		fail(w, http.StatusTooManyRequests, "too many actions in the last hour, try again later")
		return
	}
	if err := h.d.StartBackup(p); err != nil {
		h.record(r, m, "portal.backup", "error", "plan "+p.GetId())
		fail(w, http.StatusInternalServerError, "the backup could not be started")
		return
	}
	h.record(r, m, "portal.backup", "ok", "plan "+p.GetId())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (h *Handler) setNotify(w http.ResponseWriter, r *http.Request, m *member) {
	var in tenant.Notify
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	t, err := h.d.Store.SetNotify(m.tenant.ID, in)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	h.record(r, m, "portal.notify.set", "ok", strconv.Itoa(len(t.Notify.Recipients))+" recipient(s), events "+strings.Join(t.Notify.Events, ","))
	writeJSON(w, http.StatusOK, map[string]any{"notify": t.Notify})
}

func (h *Handler) testNotify(w http.ResponseWriter, r *http.Request, m *member) {
	if h.d.Notifier == nil {
		fail(w, http.StatusServiceUnavailable, "e-mail is not configured on this server")
		return
	}
	if len(m.tenant.Notify.Recipients) == 0 {
		fail(w, http.StatusBadRequest, "add a recipient first")
		return
	}
	if !h.allowAction(m.tenant.ID) {
		fail(w, http.StatusTooManyRequests, "too many actions in the last hour, try again later")
		return
	}
	err := h.d.Notifier.SendTest(m.tenant)
	if err != nil {
		h.record(r, m, "portal.notify.test", "error", "")
		fail(w, http.StatusBadGateway, "the test mail could not be sent")
		return
	}
	h.record(r, m, "portal.notify.test", "ok", "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
