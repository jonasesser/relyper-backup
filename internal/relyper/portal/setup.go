package portal

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/orchestrator"
	"github.com/garethgeorge/backrest/internal/orchestrator/tasks"
	"github.com/garethgeorge/backrest/internal/relyper/audit"
	"github.com/garethgeorge/backrest/internal/relyper/identity"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
	"go.uber.org/zap"
)

// Wiring is created before the identity service (which needs the gate) and
// finished after it (the portal needs the session verifier).
type Wiring struct {
	Store  *tenant.Store
	Gate   *tenant.Gate
	Portal tenant.PortalConfig

	cfg      *config.ConfigManager
	orch     *orchestrator.Orchestrator
	oplog    *oplog.OpLog
	audit    *audit.Log
	dataDir  string
	notifier *Notifier
	smtpErr  error
	stale    time.Duration
}

// Prepare opens the tenant store. A nil gate in IdentityGate means the
// portal is off.
func Prepare(cfg *config.ConfigManager, orch *orchestrator.Orchestrator, ol *oplog.OpLog, auditLog *audit.Log, dataDir string) (*Wiring, error) {
	store, err := tenant.Open(filepath.Join(dataDir, "tenants.json"))
	if err != nil {
		return nil, err
	}
	w := &Wiring{Store: store, Portal: tenant.PortalConfigFromEnv(nil), cfg: cfg, orch: orch, oplog: ol, audit: auditLog, dataDir: dataDir, stale: 36 * time.Hour}
	w.Gate = &tenant.Gate{Store: store, Cfg: w.Portal}
	if h := strings.TrimSpace(os.Getenv("RELYPER_TENANT_STALE_HOURS")); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n >= 1 && n <= 24*30 {
			w.stale = time.Duration(n) * time.Hour
		} else {
			zap.S().Warnw("RELYPER_TENANT_STALE_HOURS ignored: must be 1 to 720", "value", h)
		}
	}
	return w, nil
}

// IdentityGate is passed to identity.Options.Portal.
func (w *Wiring) IdentityGate() identity.PortalGate {
	if w == nil || !w.Portal.Enabled {
		return nil
	}
	return w.Gate
}

func (w *Wiring) getConfig() (*v1.Config, error) { return w.cfg.Get() }

func (w *Wiring) startBackup(p *v1.Plan) error {
	repo, err := w.orch.GetRepo(p.GetRepo())
	if err != nil {
		return err
	}
	_, err = w.orch.ScheduleTask(tasks.NewOneoffBackupTask(repo, p, time.Now(), false), tasks.TaskPriorityInteractive)
	return err
}

// Finish starts the notifier and returns the routes: authed (operator API)
// and unauthed (portal API with its own authentication).
func (w *Wiring) Finish(ctx context.Context, idp *identity.Service, isAdmin func(context.Context) bool) (authed, unauthed map[string]http.Handler) {
	w.pruneLoop(ctx)

	smtpCfg, err := SMTPConfigFromEnv(nil)
	if err != nil {
		w.smtpErr = err
		zap.S().Errorw("customer e-mail disabled: invalid SMTP configuration", "error", err)
	} else if smtpCfg != nil {
		portalURL := ""
		if u := strings.TrimRight(os.Getenv("RELYPER_PUBLIC_URL"), "/"); u != "" {
			portalURL = u + "/#/portal"
		}
		w.notifier = &Notifier{Store: w.Store, Config: w.getConfig, OpLog: w.oplog, Sender: &SMTPSender{Cfg: smtpCfg}, StaleAfter: w.stale, PortalURL: portalURL}
		w.notifier.Start(ctx)
		zap.S().Infow("customer e-mail notifications enabled", "smtp_host", smtpCfg.Host, "port", smtpCfg.Port)
	}

	op := &Operator{Store: w.Store, Config: w.getConfig, Audit: w.audit, IsAdmin: isAdmin, Status: func() map[string]any {
		st := map[string]any{
			"portal":          w.Portal.Enabled && idp.PortalEnabled(),
			"portalRequested": w.Portal.Enabled,
			"identity":        idp != nil,
			"email":           w.notifier != nil,
			"tenantClaim":     w.Portal.Claim,
			"customerRole":    w.Portal.Role,
			"customerAdmin":   w.Portal.AdminRole,
			"staleHours":      int(w.stale.Hours()),
		}
		if w.smtpErr != nil {
			st["emailError"] = w.smtpErr.Error()
		}
		return st
	}}
	authed = map[string]http.Handler{OperatorPrefix: op}
	unauthed = map[string]http.Handler{}
	if w.Portal.Enabled && idp != nil {
		h := New(Deps{Store: w.Store, Gate: w.Gate, Verify: idp.VerifyPortal, Config: w.getConfig, OpLog: w.oplog, Audit: w.audit,
			StartBackup: w.startBackup, Notifier: w.notifier, StaleAfter: w.stale})
		unauthed[APIPrefix] = h
		zap.S().Infow("customer portal enabled", "tenant_claim", w.Portal.Claim, "role", w.Portal.Role, "admin_role", w.Portal.AdminRole)
	} else if w.Portal.Enabled {
		zap.S().Errorw("customer portal disabled: it needs Relyper Identity (RELYPER_OIDC_*)")
	}
	return authed, unauthed
}

// pruneLoop removes assignments of deleted plans and repositories at start
// and after every configuration change.
func (w *Wiring) pruneLoop(ctx context.Context) {
	prune := func() {
		cfg, err := w.cfg.Get()
		if err != nil {
			return
		}
		plans, repos := map[string]bool{}, map[string]bool{}
		for _, p := range cfg.GetPlans() {
			plans[p.GetId()] = true
		}
		for _, r := range cfg.GetRepos() {
			repos[r.GetId()] = true
		}
		if n, err := w.Store.Prune(plans, repos); err != nil {
			zap.S().Warnw("tenant assignments could not be pruned", "error", err)
		} else if n > 0 {
			zap.S().Infow("removed tenant assignments of deleted plans or repositories", "count", n)
		}
	}
	prune()
	ch := w.cfg.OnChange.Subscribe()
	go func() {
		defer w.cfg.OnChange.Unsubscribe(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				prune()
			}
		}
	}()
}
