package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/relyper/tenant"
	"go.uber.org/zap"
)

// SMTPConfig is read from RELYPER_SMTP_* variables.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	TLS      string // starttls, tls, none
}

// SMTPConfigFromEnv returns nil when RELYPER_SMTP_HOST is not set.
func SMTPConfigFromEnv(getenv func(string) string) (*SMTPConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	host := strings.TrimSpace(getenv("RELYPER_SMTP_HOST"))
	if host == "" {
		return nil, nil
	}
	c := &SMTPConfig{Host: host, Username: strings.TrimSpace(getenv("RELYPER_SMTP_USERNAME")), Password: getenv("RELYPER_SMTP_PASSWORD"),
		From: strings.TrimSpace(getenv("RELYPER_SMTP_FROM")), TLS: strings.ToLower(strings.TrimSpace(getenv("RELYPER_SMTP_TLS")))}
	if f := strings.TrimSpace(getenv("RELYPER_SMTP_PASSWORD_FILE")); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("RELYPER_SMTP_PASSWORD_FILE: %w", err)
		}
		c.Password = strings.TrimSpace(string(b))
	}
	if c.TLS == "" {
		c.TLS = "starttls"
	}
	switch c.TLS {
	case "starttls", "tls":
	case "none":
		if !isLoopbackHost(host) {
			return nil, errors.New("RELYPER_SMTP_TLS=none is only allowed for localhost")
		}
	default:
		return nil, errors.New("RELYPER_SMTP_TLS must be starttls, tls or none")
	}
	c.Port = 587
	if c.TLS == "tls" {
		c.Port = 465
	}
	if p := strings.TrimSpace(getenv("RELYPER_SMTP_PORT")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("RELYPER_SMTP_PORT must be a port number")
		}
		c.Port = n
	}
	a, err := mail.ParseAddress(c.From)
	if err != nil || strings.ContainsAny(c.From, "\r\n") {
		return nil, errors.New("RELYPER_SMTP_FROM must be an e-mail address")
	}
	c.From = a.String()
	return c, nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Mail is one message.
type Mail struct {
	To      []string
	Subject string
	Body    string
}

// Sender delivers mail.
type Sender interface {
	Send(ctx context.Context, m Mail) error
}

// SMTPSender sends through an SMTP server.
type SMTPSender struct{ Cfg *SMTPConfig }

func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// Build returns the RFC 5322 message.
func (m Mail) Build(from string, now time.Time) []byte {
	var b strings.Builder
	b.WriteString("From: " + headerSafe(from) + "\r\n")
	b.WriteString("To: " + headerSafe(strings.Join(m.To, ", ")) + "\r\n")
	b.WriteString("Subject: " + mime(headerSafe(m.Subject)) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n\r\n")
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	// Dot-stuffing is done by net/smtp's data writer.
	b.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

func mime(s string) string {
	for _, r := range s {
		if r > 0x7e {
			return "=?utf-8?q?" + qEncode(s) + "?="
		}
	}
	return s
}

func qEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c == ' ':
			b.WriteByte('_')
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	return b.String()
}

// Send delivers one message.
func (s *SMTPSender) Send(ctx context.Context, m Mail) error {
	c := s.Cfg
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d := &net.Dialer{}
	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.TLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer cl.Close()
	if c.TLS == "starttls" {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if c.Username != "" {
		// PlainAuth refuses to send the password without TLS, except to localhost.
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return errors.New("authentication failed")
		}
	}
	from, _ := mail.ParseAddress(c.From)
	if err := cl.Mail(from.Address); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, to := range m.To {
		if err := cl.Rcpt(to); err != nil {
			return fmt.Errorf("rcpt: %w", err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(m.Build(c.From, time.Now())); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	return cl.Quit()
}

// Notifier mails tenants about their backups. The operation log is the
// source of truth: only final states of backup operations of assigned plans
// are reported.
type Notifier struct {
	Store      *tenant.Store
	Config     func() (*v1.Config, error)
	OpLog      *oplog.OpLog
	Sender     Sender
	StaleAfter time.Duration
	// PortalURL is put into mails ("open the portal"); may be empty.
	PortalURL string
	Now       func() time.Time

	mu    sync.Mutex
	sent  map[int64]time.Time // operation ID -> sent
	stale map[string]bool     // tenant/plan -> stale mail sent
	queue chan Mail
	once  sync.Once
	sub   oplog.Subscription
}

func (n *Notifier) init() {
	n.once.Do(func() {
		if n.Now == nil {
			n.Now = time.Now
		}
		if n.StaleAfter <= 0 {
			n.StaleAfter = 36 * time.Hour
		}
		n.sent = map[int64]time.Time{}
		n.stale = map[string]bool{}
		n.queue = make(chan Mail, 200)
	})
}

// Start subscribes to the operation log and runs the sender and the hourly
// stale check until ctx ends.
func (n *Notifier) Start(ctx context.Context) {
	n.init()
	n.sub = func(ops []*v1.Operation, ev oplog.OperationEvent) {
		if ev == oplog.OPERATION_DELETED {
			return
		}
		for _, op := range ops {
			n.HandleOperation(op)
		}
	}
	if n.OpLog != nil {
		n.OpLog.Subscribe(oplog.Query{}, &n.sub)
	}
	go func() {
		<-ctx.Done()
		if n.OpLog != nil {
			_ = n.OpLog.Unsubscribe(&n.sub)
		}
	}()
	go n.run(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		n.CheckStale()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n.CheckStale()
			}
		}
	}()
}

func (n *Notifier) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			n.deliver(ctx, m)
		}
	}
}

func (n *Notifier) deliver(ctx context.Context, m Mail) {
	if err := n.Sender.Send(ctx, m); err != nil {
		// The error never contains the SMTP password (auth errors are generic).
		zap.S().Warnw("customer notification failed", "recipients", len(m.To), "error", err)
	}
}

func (n *Notifier) enqueue(m Mail) {
	select {
	case n.queue <- m:
	default:
		zap.S().Warnw("customer notification queue full, mail dropped", "subject", m.Subject)
	}
}

// eventFor maps a final backup operation to a notification event.
func eventFor(op *v1.Operation) string {
	b := op.GetOperationBackup()
	if b == nil || b.GetDryRun() {
		return ""
	}
	switch op.GetStatus() {
	case v1.OperationStatus_STATUS_ERROR:
		return tenant.EventFailure
	case v1.OperationStatus_STATUS_WARNING:
		return tenant.EventWarning
	case v1.OperationStatus_STATUS_SUCCESS:
		return tenant.EventSuccess
	}
	return ""
}

// HandleOperation queues a mail for a finished backup of a tenant's plan.
func (n *Notifier) HandleOperation(op *v1.Operation) {
	n.init()
	event := eventFor(op)
	if event == "" {
		return
	}
	t, ok := n.Store.OwnerOfPlan(op.GetPlanId())
	if !ok || t.Suspended || !t.Notify.Has(event) || len(t.Notify.Recipients) == 0 {
		return
	}
	cfg, err := n.Config()
	if err != nil {
		return
	}
	s := newScope(cfg, t)
	p := s.plan(op.GetPlanId())
	if p == nil || !s.opBelongs(p, op) {
		return
	}
	n.mu.Lock()
	if _, done := n.sent[op.GetId()]; done {
		n.mu.Unlock()
		return
	}
	now := n.Now()
	n.sent[op.GetId()] = now
	for id, at := range n.sent {
		if now.Sub(at) > 48*time.Hour {
			delete(n.sent, id)
		}
	}
	if event != tenant.EventFailure {
		delete(n.stale, t.ID+"/"+p.GetId())
	}
	n.mu.Unlock()
	n.enqueue(n.runMail(t, p, op, event, NewScrubber(cfg)))
}

func (n *Notifier) footer() string {
	s := "\n--\nRelyper Backup. You receive this mail because your address is a recipient in the customer portal."
	if n.PortalURL != "" {
		s += "\nPortal: " + n.PortalURL
	}
	return s + "\n"
}

func (n *Notifier) runMail(t *tenant.Tenant, p *v1.Plan, op *v1.Operation, event string, sc *Scrubber) Mail {
	rv := runView(op, sc)
	word := map[string]string{tenant.EventFailure: "failed", tenant.EventWarning: "finished with warnings", tenant.EventSuccess: "succeeded"}[event]
	var b strings.Builder
	fmt.Fprintf(&b, "Backup of plan %s %s.\n\n", p.GetId(), word)
	fmt.Fprintf(&b, "Customer:  %s\n", t.Name)
	fmt.Fprintf(&b, "Plan:      %s\n", p.GetId())
	fmt.Fprintf(&b, "Started:   %s\n", time.UnixMilli(rv.StartedAt).UTC().Format(time.RFC1123))
	if rv.EndedAt > 0 {
		fmt.Fprintf(&b, "Finished:  %s\n", time.UnixMilli(rv.EndedAt).UTC().Format(time.RFC1123))
	}
	if rv.SnapshotID != "" {
		fmt.Fprintf(&b, "Snapshot:  %s\n", rv.SnapshotID)
	}
	if rv.BytesTotal > 0 {
		fmt.Fprintf(&b, "Processed: %s, added %s\n", humanBytes(rv.BytesTotal), humanBytes(rv.BytesAdded))
	}
	if rv.Errors > 0 {
		fmt.Fprintf(&b, "Unreadable files: %d\n", rv.Errors)
	}
	if rv.Message != "" {
		fmt.Fprintf(&b, "\nMessage: %s\n", rv.Message)
	}
	return Mail{To: t.Notify.Recipients, Subject: "Relyper Backup: " + p.GetId() + " " + word, Body: b.String() + n.footer()}
}

// CheckStale mails once per plan when a scheduled plan had no successful
// backup for StaleAfter, and allows the next mail after it recovered.
func (n *Notifier) CheckStale() {
	n.init()
	cfg, err := n.Config()
	if err != nil {
		return
	}
	h := &Handler{d: Deps{OpLog: n.OpLog, StaleAfter: n.StaleAfter, Now: n.Now}}
	for _, t := range n.Store.List() {
		if t.Suspended {
			continue
		}
		s := newScope(cfg, t)
		for _, p := range s.plans {
			key := t.ID + "/" + p.GetId()
			state := planState(p, h.summarize(s, p), n.Now(), n.StaleAfter)
			n.mu.Lock()
			already := n.stale[key]
			if state == StateStale {
				n.stale[key] = true
			} else if state == StateOK || state == StateWarning {
				delete(n.stale, key)
			}
			n.mu.Unlock()
			if state != StateStale || already || !t.Notify.Has(tenant.EventStale) || len(t.Notify.Recipients) == 0 {
				continue
			}
			body := fmt.Sprintf("Plan %s had no successful backup for more than %d hours.\n\nCustomer: %s\n", p.GetId(), int(n.StaleAfter.Hours()), t.Name)
			n.enqueue(Mail{To: t.Notify.Recipients, Subject: "Relyper Backup: " + p.GetId() + " has no recent backup", Body: body + n.footer()})
		}
	}
}

// SendTest sends a test mail synchronously.
func (n *Notifier) SendTest(t *tenant.Tenant) error {
	n.init()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	err := n.Sender.Send(ctx, Mail{To: t.Notify.Recipients, Subject: "Relyper Backup: test mail",
		Body: "This is a test mail for " + t.Name + ". Notifications are set up correctly.\n" + n.footer()})
	if err != nil {
		zap.S().Warnw("customer test mail failed", "tenant", t.ID, "error", err)
	}
	return err
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
