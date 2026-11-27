package infra

import (
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
	"go.uber.org/zap"
)

// notify sends a message to every RELYPER_NOTIFY_URLS target (Shoutrrr URLs:
// e-mail via smtp://, Telegram, Discord, Slack, Microsoft Teams, Matrix,
// ntfy, Gotify, Pushover, generic webhooks and more). The same subject is
// sent at most once per hour so a broken backup does not flood the inbox.
func (s *Service) notify(subject, body string) {
	if len(s.s.NotifyURLs) == 0 {
		return
	}
	s.mu.Lock()
	if t, ok := s.lastNote[subject]; ok && time.Since(t) < time.Hour {
		s.mu.Unlock()
		return
	}
	s.lastNote[subject] = time.Now()
	s.mu.Unlock()
	msg := subject + "\n\n" + body
	for i, u := range s.s.NotifyURLs {
		if err := shoutrrr.Send(u, msg); err != nil {
			// The URL can contain credentials; log only its position.
			zap.S().Warnw("Relyper notification failed", "target", i+1, "error", redactURLs(err.Error(), s.s.NotifyURLs))
		}
	}
}

func redactURLs(msg string, urls []string) string {
	for _, u := range urls {
		if u != "" {
			msg = strings.ReplaceAll(msg, u, "<notification url>")
		}
	}
	return msg
}

// SendTestNotification sends a test message to all targets and returns the
// number of failures.
func (s *Service) SendTestNotification() (targets, failures int) {
	for i, u := range s.s.NotifyURLs {
		if err := shoutrrr.Send(u, "Relyper Backup: test notification"); err != nil {
			failures++
			zap.S().Warnw("Relyper test notification failed", "target", i+1, "error", redactURLs(err.Error(), s.s.NotifyURLs))
		}
	}
	return len(s.s.NotifyURLs), failures
}

// coverageChange returns the notification for an app whose coverage became
// a problem or recovered since the last call, or "" when nothing changed.
// The state is kept in memory, so a problem is reported once more after a
// restart.
func (s *Service) coverageChange(appID, name, state string) (subject, body string) {
	s.mu.Lock()
	prev, seen := s.notified[appID]
	s.notified[appID] = state
	s.mu.Unlock()
	bad := func(st string) bool { return stateRank[st] >= stateRank[StateStale] }
	switch {
	case bad(state) && state != prev:
		return "Relyper Backup: " + name + " is " + state, "App " + appID + " coverage: " + state + ". Open the Infrastructure page for details."
	case seen && bad(prev) && !bad(state):
		return "Relyper Backup: " + name + " is protected again", "App " + appID + " coverage: " + state + "."
	}
	return "", ""
}
