package sshdiag

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testServer is an in-process SSH server with an SFTP subsystem rooted in a
// temporary directory. It accepts one user with one public key or password.
type testServer struct {
	addr     string
	host     string
	port     int
	hostKey  ssh.Signer
	root     string
	listener net.Listener
}

const testUser = "backup"
const testPassword = "correct horse battery staple"

func newTestServer(t *testing.T, clientKey ssh.PublicKey, noSFTP bool) *testServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == testUser && clientKey != nil && string(k.Marshal()) == string(clientKey.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == testUser && string(pw) == testPassword {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(hostKey)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s := &testServer{addr: l.Addr().String(), hostKey: hostKey, root: root, listener: l}
	host, portStr, _ := net.SplitHostPort(s.addr)
	s.host = host
	s.port, _ = strconv.Atoi(portStr)
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(nc, cfg, noSFTP)
		}
	}()
	return s
}

func (s *testServer) serve(nc net.Conn, cfg *ssh.ServerConfig, noSFTP bool) {
	defer nc.Close()
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, in, err := nch.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range in {
				ok := !noSFTP && req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.root))
					if err == nil {
						_ = srv.Serve()
					}
					ch.Close()
					return
				}
			}
		}()
	}
}

func newClientKey(t *testing.T, dir string) (ssh.PublicKey, string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	return sshPub, path
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func stepByName(r *Report, name string) Step {
	for _, s := range r.Steps {
		if s.Name == name {
			return s
		}
	}
	return Step{}
}

func TestScanAndTrustHostKey(t *testing.T) {
	srv := newTestServer(t, nil, false)
	kh := filepath.Join(t.TempDir(), "ssh", "known_hosts")

	keys, d := ScanHostKeys(ctx(t), srv.host, srv.port, kh, 5*time.Second)
	if d != nil {
		t.Fatalf("scan: %v", d)
	}
	if len(keys) != 1 || keys[0].Type != ssh.KeyAlgoED25519 {
		t.Fatalf("keys = %+v, want one ed25519 key", keys)
	}
	want := ssh.FingerprintSHA256(srv.hostKey.PublicKey())
	if keys[0].Fingerprint != want || keys[0].Status != KeyUnknown {
		t.Fatalf("key = %+v, want fingerprint %s and status unknown", keys[0], want)
	}

	// A wrong fingerprint must never be written.
	if _, err := TrustHostKey(ctx(t), srv.host, srv.port, keys[0].Type, "SHA256:wrong", kh, 5*time.Second); err != ErrFingerprintChanged {
		t.Fatalf("trust wrong fingerprint: err = %v, want ErrFingerprintChanged", err)
	}
	if CountEntries(kh) != 0 {
		t.Fatal("known_hosts was written for a wrong fingerprint")
	}

	already, err := TrustHostKey(ctx(t), srv.host, srv.port, keys[0].Type, want, kh, 5*time.Second)
	if err != nil || already {
		t.Fatalf("trust: already=%v err=%v", already, err)
	}
	info, err := os.Stat(kh)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(kh)
	if !strings.HasPrefix(string(data), "["+srv.host+"]:"+strconv.Itoa(srv.port)+" ssh-ed25519 ") {
		t.Fatalf("unexpected known_hosts line: %q", data)
	}

	keys, _ = ScanHostKeys(ctx(t), srv.host, srv.port, kh, 5*time.Second)
	if keys[0].Status != KeyTrusted {
		t.Fatalf("status after trust = %v, want trusted", keys[0].Status)
	}
	already, err = TrustHostKey(ctx(t), srv.host, srv.port, keys[0].Type, want, kh, 5*time.Second)
	if err != nil || !already || CountEntries(kh) != 1 {
		t.Fatalf("second trust: already=%v err=%v entries=%d", already, err, CountEntries(kh))
	}
}

func TestHostKeyMismatchIsNeverReplaced(t *testing.T) {
	srv := newTestServer(t, nil, false)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	// Record a different ed25519 key for this server.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	line := "[" + srv.host + "]:" + strconv.Itoa(srv.port) + " " + string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey()))
	os.WriteFile(kh, []byte(line), 0o600)

	keys, d := ScanHostKeys(ctx(t), srv.host, srv.port, kh, 5*time.Second)
	if d != nil || keys[0].Status != KeyMismatch {
		t.Fatalf("keys=%+v d=%v, want mismatch", keys, d)
	}
	if _, err := TrustHostKey(ctx(t), srv.host, srv.port, keys[0].Type, keys[0].Fingerprint, kh, 5*time.Second); err != ErrKeyConflict {
		t.Fatalf("trust over mismatch: err = %v, want ErrKeyConflict", err)
	}
	if CountEntries(kh) != 1 {
		t.Fatal("known_hosts was changed despite a mismatch")
	}

	clientKey, keyPath := newClientKey(t, t.TempDir())
	_ = clientKey
	r := TestConnection(ctx(t), Options{Host: srv.host, Port: srv.port, User: testUser, IdentityFile: keyPath, KnownHostsPath: kh, Timeout: 5 * time.Second})
	if r.OK || r.Error.Category != CategoryHostKeyMismatch {
		t.Fatalf("report error = %+v, want host_key_mismatch", r.Error)
	}
}

func trustServer(t *testing.T, srv *testServer) string {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	line := "[" + srv.host + "]:" + strconv.Itoa(srv.port) + " " + string(ssh.MarshalAuthorizedKey(srv.hostKey.PublicKey()))
	if err := os.WriteFile(kh, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return kh
}

func TestConnectionSuccessWithKey(t *testing.T) {
	keyDir := t.TempDir()
	clientKey, keyPath := newClientKey(t, keyDir)
	srv := newTestServer(t, clientKey, false)
	kh := trustServer(t, srv)
	os.MkdirAll(filepath.Join(srv.root, "restic"), 0o755)

	r := TestConnection(ctx(t), Options{Host: srv.host, Port: srv.port, User: testUser, IdentityFile: keyPath, RemotePath: filepath.Join(srv.root, "restic"), KnownHostsPath: kh, WriteTest: true, Timeout: 5 * time.Second})
	if !r.OK {
		t.Fatalf("report not ok: %+v", r)
	}
	if r.Repository != RepoEmpty {
		t.Fatalf("repository = %v, want empty", r.Repository)
	}
	if s := stepByName(r, StepWriteTest); s.Status != StepOK {
		t.Fatalf("write test step = %+v", s)
	}
	entries, _ := os.ReadDir(filepath.Join(srv.root, "restic"))
	if len(entries) != 0 {
		t.Fatalf("write test left files behind: %v", entries)
	}
}

func TestConnectionDetectsRepositoryStates(t *testing.T) {
	clientKey, keyPath := newClientKey(t, t.TempDir())
	srv := newTestServer(t, clientKey, false)
	kh := trustServer(t, srv)

	repo := filepath.Join(srv.root, "repo")
	os.MkdirAll(filepath.Join(repo, "keys"), 0o755)
	os.WriteFile(filepath.Join(repo, "config"), []byte("x"), 0o600)
	other := filepath.Join(srv.root, "other")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "notes.txt"), []byte("x"), 0o600)

	cases := map[string]RepoState{repo: RepoExisting, other: RepoNotRepository, filepath.Join(srv.root, "new", "repo"): RepoMissing}
	for p, want := range cases {
		r := TestConnection(ctx(t), Options{Host: srv.host, Port: srv.port, User: testUser, IdentityFile: keyPath, RemotePath: p, KnownHostsPath: kh, Timeout: 5 * time.Second})
		if r.Repository != want {
			t.Errorf("%s: repository = %v, want %v (steps %+v)", p, r.Repository, want, r.Steps)
		}
	}

	// Relative paths start in the login directory.
	r := TestConnection(ctx(t), Options{Host: srv.host, Port: srv.port, User: testUser, IdentityFile: keyPath, RemotePath: "repo", KnownHostsPath: kh, Timeout: 5 * time.Second})
	if r.ResolvedPath != repo || r.Repository != RepoExisting {
		t.Fatalf("relative path resolved to %q (%v), want %q", r.ResolvedPath, r.Repository, repo)
	}
}

func TestConnectionFailureCategories(t *testing.T) {
	clientKey, keyPath := newClientKey(t, t.TempDir())
	_, wrongKeyPath := newClientKey(t, t.TempDir())
	srv := newTestServer(t, clientKey, false)
	kh := trustServer(t, srv)
	noSFTP := newTestServer(t, clientKey, true)
	khNoSFTP := trustServer(t, noSFTP)

	// A closed port on localhost.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	base := Options{Host: srv.host, Port: srv.port, User: testUser, IdentityFile: keyPath, KnownHostsPath: kh, Timeout: 5 * time.Second}
	tests := []struct {
		name     string
		mod      func(o *Options)
		step     string
		category string
	}{
		{"dns", func(o *Options) { o.Host = "does-not-exist.invalid" }, StepDNS, CategoryDNS},
		{"refused", func(o *Options) { o.Port = closedPort }, StepTCP, CategoryConnectionRefused},
		{"host key unknown", func(o *Options) { o.KnownHostsPath = filepath.Join(t.TempDir(), "empty") }, StepHostKey, CategoryHostKeyUnknown},
		{"wrong key", func(o *Options) { o.IdentityFile = wrongKeyPath }, StepAuthentication, CategoryAuthentication},
		{"wrong password", func(o *Options) { o.IdentityFile = ""; o.Password = "nope" }, StepAuthentication, CategoryAuthentication},
		{"missing key file", func(o *Options) { o.IdentityFile = "/nonexistent/key" }, StepHostKey, CategoryIdentityFile},
		{"option injection", func(o *Options) { o.Host = "-oProxyCommand=touch /tmp/x" }, StepDNS, CategoryInvalidInput},
		{"no sftp", func(o *Options) { o.Port = noSFTP.port; o.KnownHostsPath = khNoSFTP }, StepSFTP, CategorySFTPUnavailable},
		{"path is file", func(o *Options) {
			f := filepath.Join(srv.root, "afile")
			os.WriteFile(f, []byte("x"), 0o600)
			o.RemotePath = f
		}, StepRemoteDirectory, CategoryRepository},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mod(&o)
			r := TestConnection(ctx(t), o)
			if r.OK || r.Error == nil {
				t.Fatalf("expected failure, got %+v", r)
			}
			if r.Error.Category != tc.category {
				t.Fatalf("category = %q (%s), want %q", r.Error.Category, r.Error.Message, tc.category)
			}
			if s := stepByName(r, tc.step); s.Status != StepFailed {
				t.Fatalf("step %s = %+v, want failed; steps %+v", tc.step, s, r.Steps)
			}
			if len(r.Steps) != len(allSteps) {
				t.Fatalf("got %d steps, want %d", len(r.Steps), len(allSteps))
			}
		})
	}

	// Password authentication works and the password never appears in output.
	o := base
	o.IdentityFile, o.Password = "", testPassword
	r := TestConnection(ctx(t), o)
	if !r.OK {
		t.Fatalf("password auth failed: %+v", r.Error)
	}
	for _, s := range r.Steps {
		if strings.Contains(s.Message+s.Hint, testPassword) {
			t.Fatal("password leaked into a step message")
		}
	}
}

func TestClassifyAlgorithm(t *testing.T) {
	d := Classify(errString("ssh: handshake failed: ssh: no common algorithm for key exchange; client offered: [curve25519-sha256], server offered: [diffie-hellman-group1-sha1]"))
	if d.Category != CategoryAlgorithm {
		t.Fatalf("category = %q", d.Category)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestValidateHost(t *testing.T) {
	for _, h := range []string{"example.com", "10.0.0.1", "::1", "sftp.host-europe.example"} {
		if d := validateHost(h); d != nil {
			t.Errorf("%q rejected: %v", h, d)
		}
	}
	for _, h := range []string{"", "-oProxyCommand=x", "a b", "host;rm", "user@host", "$(id)"} {
		if d := validateHost(h); d == nil {
			t.Errorf("%q accepted", h)
		}
	}
}
