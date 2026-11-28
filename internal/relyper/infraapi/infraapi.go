// Package infraapi is Relyper-owned: the JSON API of the Infrastructure page
// (discovery, coverage, app plans, verification, readiness, recovery
// report). It is mounted behind the web UI authentication at /relyper/infra/.
package infraapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/auth"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
	"github.com/garethgeorge/backrest/internal/relyper/infra"
	"go.uber.org/zap"
)

// Prefix of all routes.
const Prefix = "/relyper/infra/"

// Handler serves the API.
type Handler struct {
	Infra *infra.Service
	// PlanExists reports whether a plan ID is configured.
	PlanExists func(id string) bool
}

func user(r *http.Request) string {
	if u, ok := r.Context().Value(auth.UserContextKey).(*v1.User); ok && u != nil {
		return "user:" + u.GetName()
	}
	return "user:anonymous (authentication disabled)"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

type body struct {
	App    string `json:"app"`
	Repo   string `json:"repo"`
	PlanID string `json:"plan_id"`
	Plan   string `json:"plan"`
	// Paths chooses the items of an app plan (nil: the defaults).
	Paths []string `json:"paths"`
	// Tags are the Relyper Backup tags of an app (tags/set).
	Tags []string `json:"tags"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := strings.TrimPrefix(r.URL.Path, Prefix)
	ctx := r.Context()
	var b body
	var raw json.RawMessage
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil || json.Unmarshal(raw, &b) != nil {
			fail(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
	} else if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	get := r.Method == http.MethodGet
	switch {
	case get && route == "inventory":
		writeJSON(w, http.StatusOK, h.Infra.Inventory(ctx, r.URL.Query().Get("refresh") == "1"))
	case get && route == "coverage":
		if r.URL.Query().Get("refresh") == "1" {
			h.Infra.Inventory(ctx, true)
		}
		rep, err := h.Infra.Coverage(ctx)
		respond(w, rep, err)
	case get && route == "usage":
		u, err := h.Infra.Usage(ctx)
		respond(w, u, err)
	case get && route == "overview":
		o, err := h.Infra.Overview(ctx)
		respond(w, o, err)
	case get && route == "readiness":
		rep, err := h.Infra.Readiness(ctx)
		respond(w, rep, err)
	case get && route == "report":
		md, err := h.Infra.RecoveryReport(ctx)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="relyper-recovery-report-`+time.Now().Format("2006-01-02")+`.md"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(md))
	case get && route == "results":
		q := r.URL.Query()
		writeJSON(w, http.StatusOK, map[string]any{
			"database_restore_tests": h.Infra.Verifications(q.Get("app")),
			"file_restore_tests":     h.Infra.RestoreTests(q.Get("plan")),
		})
	case get && route == "settings":
		st := h.Infra.Settings()
		writeJSON(w, http.StatusOK, map[string]any{
			"dump_dir": st.DumpDir, "coverage_max_age_hours": int(st.CoverageMaxAge.Hours()),
			"verify_interval_days": int(st.VerifyInterval.Hours() / 24), "notification_targets": len(st.NotifyURLs),
			"docker_endpoint": st.DockerEndpoint,
		})
	case !get && route == "plan/propose":
		p, err := h.Infra.ProposeAppPlan(ctx, b.App)
		respond(w, p, err)
	case !get && route == "plan/create":
		p, err := h.Infra.CreateAppPlan(ctx, infra.CreateAppPlanRequest{AppID: b.App, RepoID: b.Repo, PlanID: b.PlanID, Paths: b.Paths}, user(r))
		respond(w, p, err)
	case !get && route == "tags/set":
		t, err := h.Infra.SetTags(ctx, b.App, b.Tags, user(r))
		respond(w, map[string]any{"app": b.App, "tags": t}, err)
	case !get && route == "restore/stage":
		res, err := h.Infra.StageAppRestore(ctx, b.App, user(r))
		respond(w, res, err)
	case get && route == "restore/guide":
		g, err := h.Infra.AppRecoveryGuide(ctx, r.URL.Query().Get("app"), r.URL.Query().Get("staging"))
		respond(w, g, err)
	case !get && route == "setup/propose":
		var sr infra.SetupRequest
		if err := json.Unmarshal(raw, &sr); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		p, err := h.Infra.ProposeSetup(ctx, sr)
		respond(w, p, err)
	case !get && route == "setup/create":
		var sr infra.SetupCreateRequest
		if err := json.Unmarshal(raw, &sr); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		res, err := h.Infra.CreateSetup(ctx, sr, user(r))
		respond(w, res, err)
	case !get && route == "dump":
		m, err := h.Infra.DumpNow(ctx, b.App, user(r))
		respond(w, m, err)
	case !get && route == "verify":
		if !discovery.ValidAppID(b.App) {
			fail(w, http.StatusBadRequest, errors.New("invalid app"))
			return
		}
		actor := user(r)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
			defer cancel()
			if _, err := h.Infra.VerifyApp(ctx, b.App, "manual", actor); err != nil {
				zap.S().Warnw("database verification", "app", b.App, "error", err)
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"message": "started"})
	case !get && route == "restore-test":
		if h.PlanExists != nil && !h.PlanExists(b.Plan) {
			fail(w, http.StatusNotFound, errors.New("plan not found"))
			return
		}
		actor := user(r)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
			defer cancel()
			if _, err := h.Infra.RestoreTest(ctx, b.Plan, "manual", actor); err != nil {
				zap.S().Warnw("restore test", "plan", b.Plan, "error", err)
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"message": "started"})
	case !get && route == "notify-test":
		n, failed := h.Infra.SendTestNotification()
		writeJSON(w, http.StatusOK, map[string]int{"targets": n, "failed": failed})
	default:
		fail(w, http.StatusNotFound, errors.New("not found"))
	}
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
