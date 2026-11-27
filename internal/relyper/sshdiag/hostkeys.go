// Package sshdiag implements SFTP repository diagnostics for Relyper Backup:
// fetching and explicitly trusting SSH host keys, and a step-by-step
// connection test whose failures are classified into stable categories.
//
// Host keys are never trusted implicitly. A key only ends up in known_hosts
// after a user confirmed its SHA-256 fingerprint, and a recorded key is never
// replaced automatically.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package sshdiag

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// KeyStatus tells whether a presented host key is trusted.
type KeyStatus int

const (
	KeyUnknown  KeyStatus = iota // not recorded
	KeyTrusted                   // recorded in known_hosts
	KeyMismatch                  // a different key of the same type is recorded
	KeyRevoked                   // marked @revoked
)

func (s KeyStatus) String() string {
	switch s {
	case KeyTrusted:
		return "trusted"
	case KeyMismatch:
		return "mismatch"
	case KeyRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// HostKey is one key presented by a server.
type HostKey struct {
	Type        string
	Fingerprint string // ssh.FingerprintSHA256 format
	Status      KeyStatus
	Key         ssh.PublicKey
}

// scanAlgorithms are requested one by one so that every key type the server
// offers is collected, strongest first.
var scanAlgorithms = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256,
	ssh.KeyAlgoECDSA384,
	ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512,
}

var errKeyCaptured = errors.New("sshdiag: host key captured")

// Address returns host:port with the default port 22 for port <= 0.
func Address(host string, port int) string {
	if port <= 0 {
		port = 22
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// ScanHostKeys connects to the server once per key algorithm, records the
// host key it presents and aborts each handshake before authentication. The
// keys are compared against knownHostsPath (which may not exist yet).
func ScanHostKeys(ctx context.Context, host string, port int, knownHostsPath string, timeout time.Duration) ([]HostKey, *Diagnostic) {
	if d := validateHost(host); d != nil {
		return nil, d
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	addr := Address(host, port)
	if d := resolve(ctx, host); d != nil {
		return nil, d
	}

	var keys []HostKey
	seen := map[string]bool{}
	var firstErr *Diagnostic
	for _, algo := range scanAlgorithms {
		key, err := fetchHostKey(ctx, addr, algo, timeout)
		if err != nil {
			d := Classify(err)
			if d.Category == CategoryAlgorithm {
				continue // the server simply does not offer this key type
			}
			if firstErr == nil {
				firstErr = d
			}
			if d.Category == CategoryConnectionRefused || d.Category == CategoryTimeout || d.Category == CategoryNetwork {
				break // no point in trying the other algorithms
			}
			continue
		}
		fp := ssh.FingerprintSHA256(key)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		keys = append(keys, HostKey{Type: key.Type(), Fingerprint: fp, Key: key})
	}
	if len(keys) == 0 {
		if firstErr == nil {
			firstErr = &Diagnostic{Category: CategoryAlgorithm, Message: "The server offered none of the supported host key types (ed25519, ECDSA, RSA with SHA-2).", Hint: "Enable a modern host key type on the server."}
		}
		return nil, firstErr
	}

	for i := range keys {
		keys[i].Status = keyStatus(knownHostsPath, addr, keys[i].Key)
	}
	return keys, nil
}

func fetchHostKey(ctx context.Context, addr, algo string, timeout time.Duration) (ssh.PublicKey, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	var captured ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User:              "relyper-host-key-scan",
		HostKeyAlgorithms: []string{algo},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return errKeyCaptured
		},
		Timeout: timeout,
	}
	_, _, _, err = ssh.NewClientConn(conn, addr, cfg)
	if captured != nil {
		return captured, nil
	}
	if err == nil {
		return nil, errors.New("ssh handshake finished without a host key")
	}
	return nil, err
}

// keyStatus compares key against the known_hosts entries for addr.
func keyStatus(knownHostsPath, addr string, key ssh.PublicKey) KeyStatus {
	cb, err := hostKeyCallback(knownHostsPath)
	if err != nil {
		return KeyUnknown
	}
	return keyStatusFromErr(cb(addr, fakeAddr(addr), key), key)
}

// keyStatusFromErr interprets the result of a knownhosts callback for key.
func keyStatusFromErr(err error, key ssh.PublicKey) KeyStatus {
	if err == nil {
		return KeyTrusted
	}
	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return KeyRevoked
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		for _, want := range keyErr.Want {
			if want.Key.Type() == key.Type() {
				return KeyMismatch
			}
		}
	}
	return KeyUnknown
}

// hostKeyCallback builds a strict callback from knownHostsPath. A missing
// file is treated as empty: every key is then unknown.
func hostKeyCallback(knownHostsPath string) (ssh.HostKeyCallback, error) {
	if knownHostsPath == "" {
		return nil, errors.New("no known_hosts path configured")
	}
	if _, err := os.Stat(knownHostsPath); errors.Is(err, os.ErrNotExist) {
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return &knownhosts.KeyError{}
		}, nil
	}
	return knownhosts.New(knownHostsPath)
}

// fakeAddr satisfies knownhosts, which also matches the IP of the remote
// address. Using the dialed address keeps the check hostname-based like
// OpenSSH with CheckHostIP=no (the OpenSSH default since 8.5).
func fakeAddr(addr string) net.Addr {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return &net.TCPAddr{IP: net.ParseIP(host), Port: port}
}

// knownAlgorithms returns the host key algorithms matching the key types
// recorded for addr, strongest first, or nil (library default) when none are
// recorded.
func knownAlgorithms(cb ssh.HostKeyCallback, addr string) []string {
	types := map[string]bool{}
	for _, k := range knownKeys(cb, addr) {
		types[k.Type()] = true
	}
	var algos []string
	for _, t := range []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521} {
		if types[t] {
			algos = append(algos, t)
		}
	}
	if types[ssh.KeyAlgoRSA] {
		algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
	}
	return algos
}

// knownKeys lists the keys recorded for addr by probing the callback with a
// throwaway key: knownhosts reports the recorded keys in KeyError.Want.
func knownKeys(cb ssh.HostKeyCallback, addr string) []ssh.PublicKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil
	}
	probe, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if err := cb(addr, fakeAddr(addr), probe.PublicKey()); errors.As(err, &keyErr) {
		var keys []ssh.PublicKey
		for _, w := range keyErr.Want {
			keys = append(keys, w.Key)
		}
		return keys
	}
	return nil
}

var knownHostsMu sync.Mutex

// ErrFingerprintChanged means the server no longer presents the key the
// user confirmed.
var ErrFingerprintChanged = errors.New("the server does not present a key with the confirmed fingerprint")

// ErrKeyConflict means a different key of the same type is already trusted.
var ErrKeyConflict = errors.New("a different host key of this type is already trusted for this server")

// TrustHostKey fetches the host keys again and appends the one whose type and
// fingerprint match the confirmed values to knownHostsPath. It returns
// alreadyTrusted when the key was recorded before. It never removes or
// replaces existing entries.
func TrustHostKey(ctx context.Context, host string, port int, keyType, fingerprint, knownHostsPath string, timeout time.Duration) (alreadyTrusted bool, err error) {
	if knownHostsPath == "" {
		return false, errors.New("no known_hosts path configured")
	}
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		return false, errors.New("fingerprint must be in SHA256:... format")
	}
	keys, d := ScanHostKeys(ctx, host, port, knownHostsPath, timeout)
	if d != nil {
		return false, d
	}
	var match *HostKey
	for i := range keys {
		if keys[i].Fingerprint == fingerprint && keys[i].Type == keyType {
			match = &keys[i]
			break
		}
	}
	if match == nil {
		return false, ErrFingerprintChanged
	}
	switch match.Status {
	case KeyTrusted:
		return true, nil
	case KeyMismatch, KeyRevoked:
		return false, ErrKeyConflict
	}

	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0o700); err != nil {
		return false, fmt.Errorf("create ssh directory: %w", err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(Address(host, port))}, match.Key)
	f, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("open known_hosts: %w", err)
	}
	defer f.Close()
	prefix := ""
	if needsNewline(knownHostsPath) {
		prefix = "\n"
	}
	if _, err := f.WriteString(prefix + line + "\n"); err != nil {
		return false, fmt.Errorf("write known_hosts: %w", err)
	}
	return false, nil
}

func needsNewline(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && len(data) > 0 && data[len(data)-1] != '\n'
}

// CountEntries returns the number of non-comment lines in a known_hosts file.
func CountEntries(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}

// validateHost rejects values that are not a plain host name or IP address,
// e.g. option injection such as "-oProxyCommand=..." or embedded spaces.
func validateHost(host string) *Diagnostic {
	if host == "" {
		return &Diagnostic{Category: CategoryInvalidInput, Message: "No host given."}
	}
	if len(host) > 253 || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\r\n/\\@'\"`$;|&<>") {
		return &Diagnostic{Category: CategoryInvalidInput, Message: fmt.Sprintf("%q is not a valid host name or IP address.", host)}
	}
	return nil
}
