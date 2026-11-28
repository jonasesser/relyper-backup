// relyper: Relyper Backup addition, not part of upstream Backrest.
package ftpdiag

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Stable diagnostic categories (shared names with the SFTP diagnostics where
// the meaning is the same). Only add new ones; never rename.
const (
	CategoryInvalidInput      = "invalid_input"
	CategoryDNS               = "dns_failure"
	CategoryConnectionRefused = "connection_refused"
	CategoryTimeout           = "connection_timeout"
	CategoryNetwork           = "network_unreachable"
	CategoryTLSUnsupported    = "tls_not_supported"
	CategoryTLSRequired       = "tls_required"
	CategoryCertificate       = "certificate_untrusted"
	CategoryAuthentication    = "authentication_failed"
	CategoryPathNotFound      = "remote_path_not_found"
	CategoryPermission        = "permission_denied"
	CategoryRcloneMissing     = "rclone_missing"
	CategoryUnknown           = "unknown"
)

// Diagnostic is a classified failure with an actionable hint.
type Diagnostic struct {
	Category string
	Message  string
	Hint     string
}

func (d *Diagnostic) Error() string { return strings.TrimSpace(d.Message + " " + d.Hint) }

type StepStatus int

const (
	StepOK StepStatus = iota + 1
	StepWarning
	StepFailed
	StepSkipped
)

type RepoState int

const (
	RepoUnknown RepoState = iota
	RepoExisting
	RepoEmpty
	RepoMissing
	RepoNotRepository
)

type Step struct {
	Name     string
	Status   StepStatus
	Message  string
	Hint     string
	Category string
	Duration time.Duration
}

// Options for TestConnection.
type Options struct {
	Host     string
	Port     int
	User     string
	Password string // plain
	Path     string
	Mode     TLSMode
	// CertFile pins the server certificate (SSL_CERT_FILE for rclone).
	CertFile             string
	SkipCertificateCheck bool
	WriteTest            bool
	Timeout              time.Duration
	// Pins is used to report whether the presented certificate is pinned.
	Pins *PinStore
	// Rclone is the rclone binary; empty means "rclone" from PATH.
	Rclone string
}

type Result struct {
	OK         bool
	Steps      []Step
	Error      *Diagnostic
	Repository RepoState
}

const testRemote = "RELYPERFTPTEST"

// Env returns the rclone environment for an FTP remote named remote. It is
// the same set the repository form writes into the repository's env, so the
// test exercises exactly what backups will use. obscuredPass must already be
// obscured.
func Env(remote string, o Options, obscuredPass string) []string {
	p := "RCLONE_CONFIG_" + strings.ToUpper(remote) + "_"
	port := o.Port
	if port == 0 {
		port = o.Mode.DefaultPort()
	}
	env := []string{
		p + "TYPE=ftp",
		p + "HOST=" + o.Host,
		p + "PORT=" + strconv.Itoa(port),
		p + "USER=" + o.User,
		p + "PASS=" + obscuredPass,
	}
	switch o.Mode {
	case TLSExplicit:
		env = append(env, p+"EXPLICIT_TLS=true")
	case TLSImplicit:
		env = append(env, p+"TLS=true")
	}
	if o.Mode != TLSNone {
		if o.SkipCertificateCheck {
			env = append(env, p+"NO_CHECK_CERTIFICATE=true")
		} else if o.CertFile != "" {
			env = append(env, "SSL_CERT_FILE="+o.CertFile)
		}
	}
	return env
}

// TestConnection checks name resolution, the TCP connection, TLS, the login,
// the remote directory and optionally write access, step by step.
func TestConnection(ctx context.Context, o Options) Result {
	var res Result
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Port == 0 {
		o.Port = o.Mode.DefaultPort()
	}
	fail := func(step string, d *Diagnostic, start time.Time) Result {
		res.Steps = append(res.Steps, Step{Name: step, Status: StepFailed, Message: d.Message, Hint: d.Hint, Category: d.Category, Duration: time.Since(start)})
		res.Error = d
		return res
	}
	ok := func(step, msg string, start time.Time) {
		res.Steps = append(res.Steps, Step{Name: step, Status: StepOK, Message: msg, Duration: time.Since(start)})
	}

	start := time.Now()
	if err := ValidateHost(o.Host); err != nil {
		return fail("dns", &Diagnostic{Category: CategoryInvalidInput, Message: err.Error(), Hint: "Enter only the server name, e.g. ftp.example.com, without ftp:// and without a path."}, start)
	}
	if o.Port < 1 || o.Port > 65535 {
		return fail("dns", &Diagnostic{Category: CategoryInvalidInput, Message: "Port must be between 1 and 65535."}, start)
	}
	if o.User == "" {
		return fail("dns", &Diagnostic{Category: CategoryInvalidInput, Message: "User name is missing."}, start)
	}
	if strings.ContainsAny(o.User+o.Path, "\r\n") {
		return fail("dns", &Diagnostic{Category: CategoryInvalidInput, Message: "User name and directory must not contain line breaks."}, start)
	}

	// DNS
	dctx, cancel := context.WithTimeout(ctx, o.Timeout)
	addrs, err := net.DefaultResolver.LookupHost(dctx, o.Host)
	cancel()
	if err != nil {
		return fail("dns", &Diagnostic{Category: CategoryDNS, Message: "The server name " + o.Host + " could not be resolved.", Hint: "Check the spelling of the server name."}, start)
	}
	ok("dns", strings.Join(addrs, ", "), start)

	// TCP
	start = time.Now()
	d := net.Dialer{Timeout: o.Timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(o.Host, strconv.Itoa(o.Port)))
	if err != nil {
		return fail("tcp", classifyNetErr(err, o.Port), start)
	}
	_ = conn.Close()
	ok("tcp", fmt.Sprintf("Port %d is reachable.", o.Port), start)

	// TLS
	start = time.Now()
	if o.Mode == TLSNone {
		res.Steps = append(res.Steps, Step{Name: "tls", Status: StepWarning, Message: "Plain FTP: password and backup data travel unencrypted.", Hint: "Use explicit FTP over TLS if the server supports it.", Duration: time.Since(start)})
	} else {
		certs, err := FetchCertificates(ctx, o.Host, o.Port, o.Mode, o.Timeout)
		if err != nil {
			var tu *tlsUnsupportedError
			if errors.As(err, &tu) {
				return fail("tls", &Diagnostic{Category: CategoryTLSUnsupported, Message: tu.Error(), Hint: "Try implicit FTPS (port 990), or plain FTP only if the provider offers nothing else."}, start)
			}
			dg := classifyNetErr(err, o.Port)
			if dg.Category == CategoryUnknown {
				dg = &Diagnostic{Category: CategoryTLSUnsupported, Message: "TLS could not be negotiated: " + err.Error(), Hint: "Check the encryption setting: explicit TLS normally uses port 21, implicit FTPS port 990."}
			}
			return fail("tls", dg, start)
		}
		info := Inspect(certs, o.Host, o.Port, o.Pins, time.Now())
		switch {
		case info.Trusted:
			ok("tls", "Certificate is valid for "+o.Host+".", start)
		case o.SkipCertificateCheck:
			res.Steps = append(res.Steps, Step{Name: "tls", Status: StepWarning, Message: "Connection is encrypted, but the certificate is not checked. " + info.ProblemText, Hint: "Anyone able to intercept the connection could pose as the server. Prefer confirming the certificate if the form offers it.", Duration: time.Since(start)})
		case info.Pinned && o.CertFile != "" && o.CertFile == info.PinFile:
			ok("tls", "Certificate matches the one you confirmed.", start)
		default:
			hint := "Compare the fingerprint with the one your provider or FileZilla shows and confirm it, or allow connections without certificate check."
			if !info.Pinnable {
				hint = "This certificate cannot be confirmed for this host name. Only \"Do not check the certificate\" works with it, or use the host name the certificate was issued for."
			}
			return fail("tls", &Diagnostic{Category: CategoryCertificate, Message: info.ProblemText + " Fingerprint: " + info.Fingerprint, Hint: hint}, start)
		}
	}

	// Login and directory, through rclone exactly as restic will use it.
	start = time.Now()
	rclone := o.Rclone
	if rclone == "" {
		rclone = "rclone"
	}
	if _, err := exec.LookPath(rclone); err != nil {
		return fail("login", &Diagnostic{Category: CategoryRcloneMissing, Message: "rclone is not installed on the Relyper Backup host.", Hint: "restic needs rclone for FTP repositories. The Relyper Backup Docker image includes it."}, start)
	}
	obscured, err := Obscure(o.Password)
	if err != nil {
		return fail("login", &Diagnostic{Category: CategoryUnknown, Message: err.Error()}, start)
	}
	env := Env(testRemote, o, obscured)
	r := runner{bin: rclone, env: env, timeout: o.Timeout}
	path := strings.TrimSpace(o.Path)

	// List the login directory first: it tells login problems apart from a
	// missing repository directory.
	if _, err := r.list(ctx, ""); err != nil {
		return fail("login", classifyRclone(err.Error()), start)
	}
	ok("login", "Logged in as "+o.User+".", start)

	start = time.Now()
	entries, err := r.list(ctx, path)
	missing := false
	if err != nil {
		dg := classifyRclone(err.Error())
		if dg.Category != CategoryPathNotFound {
			return fail("remote_directory", dg, start)
		}
		missing = true
		res.Steps = append(res.Steps, Step{Name: "remote_directory", Status: StepOK, Message: "Directory " + displayPath(path) + " does not exist yet; initializing the repository creates it.", Duration: time.Since(start)})
	} else {
		ok("remote_directory", fmt.Sprintf("Directory %s is readable (%d entries).", displayPath(path), len(entries)), start)
	}

	if o.WriteTest {
		start = time.Now()
		if d := r.writeTest(ctx, path, missing); d != nil {
			return fail("write_test", d, start)
		}
		ok("write_test", "A test file was written and removed.", start)
	} else {
		res.Steps = append(res.Steps, Step{Name: "write_test", Status: StepSkipped, Message: "Not requested."})
	}

	start = time.Now()
	switch {
	case missing:
		res.Repository = RepoMissing
	case len(entries) == 0:
		res.Repository = RepoEmpty
	case isRepository(entries):
		res.Repository = RepoExisting
	default:
		res.Repository = RepoNotRepository
	}
	if res.Repository == RepoNotRepository {
		res.Steps = append(res.Steps, Step{Name: "repository", Status: StepWarning, Message: "The directory contains other files but no restic repository.", Hint: "Use an empty or new directory for the repository.", Duration: time.Since(start)})
	} else {
		ok("repository", repoText(res.Repository), start)
	}
	res.OK = true
	return res
}

func displayPath(p string) string {
	if p == "" {
		return "(login directory)"
	}
	return p
}

func repoText(s RepoState) string {
	switch s {
	case RepoExisting:
		return "An existing restic repository was found."
	case RepoEmpty:
		return "Empty directory: ready for a new repository."
	case RepoMissing:
		return "New repository will be created."
	}
	return ""
}

type entry struct {
	Name  string `json:"Name"`
	IsDir bool   `json:"IsDir"`
}

func isRepository(entries []entry) bool {
	var config, keys bool
	for _, e := range entries {
		switch {
		case e.Name == "config" && !e.IsDir:
			config = true
		case e.Name == "keys" && e.IsDir:
			keys = true
		}
	}
	return config && keys
}

type runner struct {
	bin     string
	env     []string
	timeout time.Duration
}

func (r runner) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	base := []string{"--low-level-retries", "1", "--retries", "1", "--contimeout", r.timeout.String(), "--timeout", r.timeout.String(), "-q"}
	cmd := exec.CommandContext(ctx, r.bin, append(args, base...)...)
	cmd.Env = append(cleanEnv(), r.env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errors.New("i/o timeout: rclone did not finish in time")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return stdout.Bytes(), nil
}

func (r runner) list(ctx context.Context, path string) ([]entry, error) {
	out, err := r.run(ctx, nil, "lsjson", "--max-depth", "1", testRemote+":"+path)
	if err != nil {
		return nil, err
	}
	var entries []entry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("unexpected rclone output: %w", err)
	}
	return entries, nil
}

func (r runner) writeTest(ctx context.Context, path string, createdDir bool) *Diagnostic {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	name := ".relyper-write-test-" + hex.EncodeToString(buf)
	target := name
	if path != "" {
		target = strings.TrimSuffix(path, "/") + "/" + name
	}
	if _, err := r.run(ctx, []byte("relyper write test\n"), "rcat", testRemote+":"+target); err != nil {
		return classifyRclone(err.Error())
	}
	if _, err := r.run(ctx, nil, "deletefile", testRemote+":"+target); err != nil {
		d := classifyRclone(err.Error())
		d.Message = "The test file " + target + " was written but could not be removed: " + d.Message
		return d
	}
	if createdDir && path != "" {
		// Leave no trace: remove the directory the write test created (only
		// succeeds when it is empty).
		_, _ = r.run(ctx, nil, "rmdir", testRemote+":"+path)
	}
	return nil
}

// cleanEnv is the process environment without rclone and TLS settings that
// could change what the test does.
func cleanEnv() []string {
	var out []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "RCLONE_") || strings.HasPrefix(e, "SSL_CERT_") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func classifyNetErr(err error, port int) *Diagnostic {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"):
		return &Diagnostic{Category: CategoryDNS, Message: "The server name could not be resolved."}
	case strings.Contains(msg, "connection refused"):
		return &Diagnostic{Category: CategoryConnectionRefused, Message: fmt.Sprintf("The server refused the connection on port %d.", port), Hint: "Check the port: 21 for FTP and explicit TLS, 990 for implicit FTPS."}
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return &Diagnostic{Category: CategoryTimeout, Message: fmt.Sprintf("No answer from the server on port %d.", port), Hint: "A firewall may block the connection, or the port is wrong."}
	case strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "no route to host"):
		return &Diagnostic{Category: CategoryNetwork, Message: "The network of the server is not reachable from the Relyper Backup host."}
	}
	return &Diagnostic{Category: CategoryUnknown, Message: msg}
}

// classifyRclone maps rclone's error output to a diagnostic. rclone never
// prints the password, so the message is safe to show.
func classifyRclone(msg string) *Diagnostic {
	msg = lastLine(msg)
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "tls required") || strings.Contains(l, "must use encryption") || strings.Contains(l, "ssl/tls required") || strings.Contains(l, "policy requires ssl"):
		return &Diagnostic{Category: CategoryTLSRequired, Message: "The server only accepts encrypted connections.", Hint: "Choose explicit FTP over TLS."}
	case strings.Contains(l, "auth\" not understood") || strings.Contains(l, "auth tls") || strings.Contains(l, " 534 "):
		return &Diagnostic{Category: CategoryTLSUnsupported, Message: "The server does not support this kind of TLS: " + msg}
	case strings.Contains(l, "x509") || strings.Contains(l, "certificate"):
		return &Diagnostic{Category: CategoryCertificate, Message: "The server certificate was rejected: " + msg, Hint: "Confirm the certificate in the form, or allow connections without certificate check."}
	case strings.Contains(l, " 530 ") || strings.Contains(l, ": 530") || strings.Contains(l, "login incorrect") || strings.Contains(l, "authentication failed"):
		return &Diagnostic{Category: CategoryAuthentication, Message: "Login failed: user name or password is wrong.", Hint: "Use the FTP user and password of the backup space, not the customer account login."}
	case strings.Contains(l, "no such") || strings.Contains(l, "not found") || strings.Contains(l, "doesn't exist") || strings.Contains(l, "does not exist") || strings.Contains(l, " 550 "):
		if strings.Contains(l, "permission") || strings.Contains(l, "denied") {
			return &Diagnostic{Category: CategoryPermission, Message: "Permission denied: " + msg}
		}
		return &Diagnostic{Category: CategoryPathNotFound, Message: "The directory does not exist: " + msg}
	case strings.Contains(l, "permission") || strings.Contains(l, "denied") || strings.Contains(l, " 553 ") || strings.Contains(l, " 532 "):
		return &Diagnostic{Category: CategoryPermission, Message: "Permission denied: " + msg, Hint: "The FTP user may not write to this directory."}
	case strings.Contains(l, "connection refused"):
		return &Diagnostic{Category: CategoryConnectionRefused, Message: msg}
	case strings.Contains(l, "timeout") || strings.Contains(l, "timed out"):
		return &Diagnostic{Category: CategoryTimeout, Message: msg, Hint: "Passive data connections may be blocked by a firewall."}
	}
	return &Diagnostic{Category: CategoryUnknown, Message: msg}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	// Drop the log timestamp and level rclone puts in front.
	for _, lvl := range []string{"CRITICAL: ", "ERROR : ", "NOTICE: "} {
		if i := strings.Index(s, lvl); i >= 0 {
			s = s[i+len(lvl):]
			break
		}
	}
	return s
}
