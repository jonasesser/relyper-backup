package infra

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Run performs periodic verification until ctx ends: for every app plan, a
// file restore test and (for apps with databases) a database restore test
// whenever the last one is older than RELYPER_VERIFY_INTERVAL_DAYS. It also
// sends a notification when the coverage of an app gets worse than
// "protected". A failure never touches backups.
func (s *Service) Run(ctx context.Context) {
	go s.watchOperations(ctx)
	// Give the server time to start before the first round.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			zap.S().Errorw("relyper verification round failed", "panic", r)
		}
	}()
	cfg, err := s.cfg()
	if err != nil {
		return
	}
	now := time.Now()
	s.checkStorage(ctx, cfg, now)
	if len(s.s.NotifyURLs) > 0 {
		s.checkOverduePlans(cfg, now)
	}
	if s.s.VerifyInterval > 0 {
		for _, p := range cfg.Plans {
			if p.GetSchedule().GetDisabled() || s.planRun(cfg, p.Id).snapshot == nil {
				continue
			}
			appID, _ := HookApp(p)
			if appID == "" && !s.coversDiscoveredApp(ctx, p.Id) {
				continue
			}
			if t := s.store.restoreTests(p.Id); len(t) == 0 || olderThan(t[0].Time, now, s.s.VerifyInterval) {
				if _, err := s.RestoreTest(ctx, p.Id, "schedule", "system"); err != nil {
					zap.S().Warnw("scheduled restore test", "plan", p.Id, "error", err)
				}
			}
			if appID != "" && s.docker != nil {
				if v := s.store.verifications(appID); len(v) == 0 || olderThan(v[0].Time, now, s.s.VerifyInterval) {
					if _, err := s.VerifyApp(ctx, appID, "schedule", "system"); err != nil {
						zap.S().Warnw("scheduled database verification", "app", appID, "error", err)
					}
				}
			}
		}
	}
	if len(s.s.NotifyURLs) > 0 {
		if cov, err := s.Coverage(ctx); err == nil {
			for _, a := range cov.Apps {
				if len(a.Plans) == 0 {
					continue
				}
				if subject, body := s.coverageChange(a.App, a.Name, a.State); subject != "" {
					s.notify(subject, body)
				}
			}
		}
	}
}

func (s *Service) coversDiscoveredApp(ctx context.Context, planID string) bool {
	cov, err := s.Coverage(ctx)
	if err != nil {
		return false
	}
	for _, a := range cov.Apps {
		for _, p := range a.Plans {
			if p == planID {
				return true
			}
		}
	}
	return false
}
