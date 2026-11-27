package sshdiag

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// StepStatus is the outcome of one test step.
type StepStatus int

const (
	StepOK StepStatus = iota + 1
	StepWarning
	StepFailed
	StepSkipped
)

// Step names, in execution order.
const (
	StepDNS             = "dns"
	StepTCP             = "tcp"
	StepHostKey         = "host_key"
	StepAuthentication  = "authentication"
	StepSFTP            = "sftp"
	StepRemoteDirectory = "remote_directory"
	StepWriteTest       = "write_test"
	StepRepository      = "repository"
	StepStorage         = "storage"
)

var allSteps = []string{StepDNS, StepTCP, StepHostKey, StepAuthentication, StepSFTP, StepRemoteDirectory, StepWriteTest, StepRepository, StepStorage}

// Step is the result of one stage of the connection test.
type Step struct {
	Name     string
	Status   StepStatus
	Message  string
	Hint     string
	Category string
	Duration time.Duration
}

// RepoState describes what was found in the remote directory.
type RepoState int

const (
	RepoUnknown RepoState = iota
	RepoExisting
	RepoEmpty
	RepoMissing
	RepoNotRepository
)

// Options configure TestConnection.
type Options struct {
	Host           string
	Port           int
	User           string
	IdentityFile   string // private key file on this host
	Password       string // used for this test only
	RemotePath     string // as written in the restic URI
	KnownHostsPath string
	WriteTest      bool
	Timeout        time.Duration // per network step; 0 means 15s
}

// Report is the result of TestConnection.
type Report struct {
	OK           bool
	Steps        []Step
	Error        *Diagnostic // first failure
	Repository   RepoState
	ResolvedPath string
	FreeBytes    int64 // -1 when unknown
	TotalBytes   int64 // -1 when unknown
}

type runner struct {
	report *Report
}

func (r *runner) add(name string, status StepStatus, start time.Time, msg string, d *Diagnostic) {
	s := Step{Name: name, Status: status, Message: msg, Duration: time.Since(start)}
	if d != nil {
		s.Category, s.Hint = d.Category, d.Hint
		if s.Message == "" {
			s.Message = d.Message
		}
	}
	r.report.Steps = append(r.report.Steps, s)
	if status == StepFailed && r.report.Error == nil {
		r.report.Error = d
	}
}

func (r *runner) fail(name string, start time.Time, d *Diagnostic) *Report {
	r.add(name, StepFailed, start, d.Message, d)
	done := map[string]bool{}
	for _, s := range r.report.Steps {
		done[s.Name] = true
	}
	for _, n := range allSteps {
		if !done[n] {
			r.report.Steps = append(r.report.Steps, Step{Name: n, Status: StepSkipped, Message: "Skipped because an earlier step failed."})
		}
	}
	return r.report
}

// TestConnection checks every stage needed for an SFTP restic repository and
// stops at the first hard failure. It never modifies known_hosts; with
// WriteTest it creates and deletes one small file in the remote directory.
func TestConnection(ctx context.Context, o Options) *Report {
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Second
	}
	r := &runner{report: &Report{FreeBytes: -1, TotalBytes: -1}}
	addr := Address(o.Host, o.Port)

	// 1. DNS
	start := time.Now()
	if d := validateHost(o.Host); d != nil {
		return r.fail(StepDNS, start, d)
	}
	if o.User == "" || strings.HasPrefix(o.User, "-") || strings.ContainsAny(o.User, " \t\r\n@:/") {
		return r.fail(StepDNS, start, &Diagnostic{Category: CategoryInvalidInput, Message: "A valid SSH user name is required.", Hint: "Write the URI as sftp:user@host:/path."})
	}
	if net.ParseIP(o.Host) != nil {
		r.add(StepDNS, StepSkipped, start, "Host is an IP address.", nil)
	} else {
		dnsCtx, cancel := context.WithTimeout(ctx, o.Timeout)
		ips, err := net.DefaultResolver.LookupHost(dnsCtx, o.Host)
		cancel()
		if err != nil {
			return r.fail(StepDNS, start, Classify(err))
		}
		r.add(StepDNS, StepOK, start, "Resolved to "+strings.Join(ips, ", ")+".", nil)
	}

	// 2. TCP
	start = time.Now()
	dialer := net.Dialer{Timeout: o.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return r.fail(StepTCP, start, Classify(err))
	}
	defer conn.Close()
	r.add(StepTCP, StepOK, start, "Connected to "+addr+".", nil)

	// 3./4. SSH handshake: host key, then authentication.
	start = time.Now()
	hostKeyCB, err := hostKeyCallback(o.KnownHostsPath)
	if err != nil {
		return r.fail(StepHostKey, start, &Diagnostic{Category: CategoryHostKeyUnknown, Message: "known_hosts could not be read: " + err.Error(), Hint: "Fix or remove the broken line in known_hosts."})
	}
	var hostKeyErr error
	hostKeyChecked := false
	var presented ssh.PublicKey
	auth, d := authMethods(o)
	if d != nil {
		return r.fail(StepHostKey, start, d)
	}
	cfg := &ssh.ClientConfig{
		User: o.User,
		Auth: auth,
		// Like OpenSSH, prefer the host key types already trusted for this
		// host; otherwise the server may present a different key type and the
		// check fails although a valid key is recorded.
		HostKeyAlgorithms: knownAlgorithms(hostKeyCB, addr),
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			hostKeyChecked = true
			presented = key
			hostKeyErr = hostKeyCB(addr, fakeAddr(addr), key)
			return hostKeyErr
		},
		Timeout: o.Timeout,
	}
	_ = conn.SetDeadline(time.Now().Add(o.Timeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		if hostKeyErr != nil {
			d := Classify(hostKeyErr)
			if d.Category == CategoryHostKeyMismatch && presented != nil && keyStatusFromErr(hostKeyErr, presented) == KeyUnknown {
				// Only keys of other types are recorded: not a conflict.
				d = &Diagnostic{Category: CategoryHostKeyUnknown, Message: "The server presented a " + presented.Type() + " host key; only other key types are trusted for this server.", Hint: "Fetch the host key and trust the matching key after comparing its fingerprint."}
			}
			if presented != nil {
				d.Message += " Presented key: " + presented.Type() + " " + ssh.FingerprintSHA256(presented) + "."
			}
			return r.fail(StepHostKey, start, d)
		}
		if !hostKeyChecked {
			return r.fail(StepHostKey, start, Classify(err))
		}
		r.add(StepHostKey, StepOK, start, "Host key "+presented.Type()+" "+ssh.FingerprintSHA256(presented)+" is trusted.", nil)
		return r.fail(StepAuthentication, time.Now(), Classify(err))
	}
	r.add(StepHostKey, StepOK, start, "Host key "+presented.Type()+" "+ssh.FingerprintSHA256(presented)+" is trusted.", nil)
	r.add(StepAuthentication, StepOK, start, "Logged in as "+o.User+".", nil)
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	// 5. SFTP subsystem
	start = time.Now()
	sc, err := sftp.NewClient(client)
	if err != nil {
		return r.fail(StepSFTP, start, Classify(err))
	}
	defer sc.Close()
	r.add(StepSFTP, StepOK, start, "SFTP is available.", nil)

	// 6. Remote directory
	start = time.Now()
	dir, err := resolveRemotePath(sc, o.RemotePath)
	if err != nil {
		return r.fail(StepRemoteDirectory, start, Classify(err))
	}
	r.report.ResolvedPath = dir
	target := dir // where the write test runs
	info, err := sc.Stat(dir)
	switch {
	case err == nil && !info.IsDir():
		return r.fail(StepRemoteDirectory, start, &Diagnostic{Category: CategoryRepository, Message: dir + " exists but is a file, not a directory."})
	case err == nil:
		r.add(StepRemoteDirectory, StepOK, start, dir+" exists.", nil)
	case isNotExist(err):
		parent := nearestExistingParent(sc, dir)
		if parent == "" {
			return r.fail(StepRemoteDirectory, start, &Diagnostic{Category: CategoryPathNotFound, Message: "Neither " + dir + " nor any parent directory is accessible."})
		}
		target = parent
		r.report.Repository = RepoMissing
		r.add(StepRemoteDirectory, StepWarning, start, dir+" does not exist yet. Initializing the repository creates it inside "+parent+".", nil)
	default:
		return r.fail(StepRemoteDirectory, start, Classify(err))
	}

	// 7. Write test
	start = time.Now()
	if !o.WriteTest {
		r.add(StepWriteTest, StepSkipped, start, "Write test not requested.", nil)
	} else if d := writeTest(sc, target); d != nil {
		return r.fail(StepWriteTest, start, d)
	} else {
		r.add(StepWriteTest, StepOK, start, "Created, read and deleted a test file in "+target+".", nil)
	}

	// 8. Repository detection
	start = time.Now()
	if r.report.Repository != RepoMissing {
		state, msg, d := detectRepository(sc, dir)
		r.report.Repository = state
		status := StepOK
		if state == RepoNotRepository {
			status = StepWarning
		}
		if d != nil {
			return r.fail(StepRepository, start, d)
		}
		r.add(StepRepository, status, start, msg, nil)
	} else {
		r.add(StepRepository, StepOK, start, "No repository yet. It can be initialized.", nil)
	}

	// 9. Storage (informational)
	start = time.Now()
	if vfs, err := sc.StatVFS(target); err == nil {
		r.report.FreeBytes = int64(vfs.FreeSpace())
		r.report.TotalBytes = int64(vfs.TotalSpace())
		r.add(StepStorage, StepOK, start, fmt.Sprintf("%s free of %s (reported by the server).", humanBytes(r.report.FreeBytes), humanBytes(r.report.TotalBytes)), nil)
	} else {
		r.add(StepStorage, StepSkipped, start, "The server does not report free space (statvfs not supported). Check the quota in your provider's control panel.", nil)
	}

	r.report.OK = r.report.Error == nil
	return r.report
}

func authMethods(o Options) ([]ssh.AuthMethod, *Diagnostic) {
	var methods []ssh.AuthMethod
	if o.IdentityFile != "" {
		data, err := os.ReadFile(o.IdentityFile)
		if err != nil {
			return nil, &Diagnostic{Category: CategoryIdentityFile, Message: "The identity file could not be read.", Hint: "Check the path; it must exist inside the Relyper Backup container."}
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			var passErr *ssh.PassphraseMissingError
			if errors.As(err, &passErr) {
				return nil, Classify(err)
			}
			return nil, &Diagnostic{Category: CategoryIdentityFile, Message: "The identity file is not a valid private key.", Hint: "Use an OpenSSH or PEM private key file."}
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if o.Password != "" {
		pw := o.Password
		methods = append(methods, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = pw
			}
			return answers, nil
		}))
	}
	if len(methods) == 0 {
		return nil, &Diagnostic{Category: CategoryInvalidInput, Message: "No identity file or password given.", Hint: "Generate a key with \"Generate Key\" or enter the path of an existing key."}
	}
	return methods, nil
}

// resolveRemotePath applies restic's semantics: absolute paths stay as they
// are, relative paths start in the login directory.
func resolveRemotePath(sc *sftp.Client, p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.ContainsRune(p, 0) {
		return "", &Diagnostic{Category: CategoryInvalidInput, Message: "The path contains invalid characters."}
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	wd, err := sc.Getwd()
	if err != nil {
		return "", err
	}
	if p == "" {
		return wd, nil
	}
	return path.Join(wd, p), nil
}

func nearestExistingParent(sc *sftp.Client, dir string) string {
	for p := path.Dir(dir); ; p = path.Dir(p) {
		if info, err := sc.Stat(p); err == nil && info.IsDir() {
			return p
		}
		if p == "/" || p == "." {
			return ""
		}
	}
}

func isNotExist(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var se *sftp.StatusError
	return errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxNoSuchFile
}

func writeTest(sc *sftp.Client, dir string) *Diagnostic {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	name := path.Join(dir, ".relyper-write-test-"+hex.EncodeToString(buf))
	payload := []byte("relyper-backup write test\n")

	f, err := sc.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		d := Classify(err)
		d.Message = "Cannot create files in " + dir + ". " + d.Message
		return d
	}
	_, werr := f.Write(payload)
	cerr := f.Close()
	defer sc.Remove(name)
	if werr != nil || cerr != nil {
		return &Diagnostic{Category: CategoryPermission, Message: "Writing to " + dir + " failed.", Hint: "The disk or quota may be full, or the user lacks write permission."}
	}
	rf, err := sc.Open(name)
	if err != nil {
		return Classify(err)
	}
	got, err := io.ReadAll(rf)
	rf.Close()
	if err != nil || string(got) != string(payload) {
		return &Diagnostic{Category: CategoryRepository, Message: "A test file could be written but not read back correctly.", Hint: "Check the storage for errors and quota limits."}
	}
	if err := sc.Remove(name); err != nil {
		return &Diagnostic{Category: CategoryPermission, Message: "A test file could be written but not deleted.", Hint: "restic needs delete permission for locks and pruning. Remove " + name + " by hand."}
	}
	return nil
}

func detectRepository(sc *sftp.Client, dir string) (RepoState, string, *Diagnostic) {
	entries, err := sc.ReadDir(dir)
	if err != nil {
		return RepoUnknown, "", Classify(err)
	}
	if len(entries) == 0 {
		return RepoEmpty, "The directory is empty. A new repository can be initialized here.", nil
	}
	names := map[string]os.FileInfo{}
	for _, e := range entries {
		names[e.Name()] = e
	}
	cfg, hasConfig := names["config"]
	keys, hasKeys := names["keys"]
	if hasConfig && !cfg.IsDir() && hasKeys && keys.IsDir() {
		return RepoExisting, "An existing restic repository was found. Add it with its existing password.", nil
	}
	return RepoNotRepository, fmt.Sprintf("The directory contains %d entries but no restic repository. Use an empty or new subdirectory.", len(entries)), nil
}

func resolve(ctx context.Context, host string) *Diagnostic {
	if net.ParseIP(host) != nil {
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(c, host); err != nil {
		return Classify(err)
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
