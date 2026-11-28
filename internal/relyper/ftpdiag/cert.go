// relyper: Relyper Backup addition, not part of upstream Backrest.
package ftpdiag

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TLSMode selects how the FTP connection is encrypted.
type TLSMode int

const (
	TLSExplicit TLSMode = iota // AUTH TLS on the normal port (FTPES)
	TLSImplicit                // TLS from the first byte (FTPS, port 990)
	TLSNone                    // plain FTP
)

// DefaultPort returns the usual port for the mode.
func (m TLSMode) DefaultPort() int {
	if m == TLSImplicit {
		return 990
	}
	return 21
}

// Certificate problems.
const (
	ProblemUnknownAuthority = "unknown_authority"
	ProblemNameMismatch     = "name_mismatch"
	ProblemExpired          = "expired"
	ProblemNotYetValid      = "not_yet_valid"
	ProblemOther            = "invalid"
)

// CertInfo describes the certificate a server presented.
type CertInfo struct {
	Leaf        *x509.Certificate
	Fingerprint string // SHA-256, upper-case hex with colons
	Trusted     bool   // verified against the system roots for the host
	Pinned      bool   // equals the pinned certificate for this host
	PinFile     string // set when Pinned
	Problem     string
	ProblemText string
	// Pinnable reports whether pinning can work: Go (and so rclone) still
	// checks the host name and the validity period of a pinned certificate.
	Pinnable bool
}

// Fingerprint formats the SHA-256 fingerprint of a DER certificate the way
// FileZilla shows it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// NormalizeFingerprint accepts hex with or without colons, any case.
func NormalizeFingerprint(fp string) string {
	s := strings.ToUpper(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(fp)))
	s = strings.TrimPrefix(s, "SHA256")
	if len(s) != 64 {
		return s
	}
	parts := make([]string, 32)
	for i := range parts {
		parts[i] = s[2*i : 2*i+2]
	}
	return strings.Join(parts, ":")
}

// FetchCertificates connects to host:port, negotiates TLS without verifying
// it and returns the certificate chain the server presented.
func FetchCertificates(ctx context.Context, host string, port int, mode TLSMode, timeout time.Duration) ([]*x509.Certificate, error) {
	if mode == TLSNone {
		return nil, errors.New("plain FTP has no certificate")
	}
	if err := ValidateHost(host); err != nil {
		return nil, err
	}
	if port == 0 {
		port = mode.DefaultPort()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if mode == TLSExplicit {
		r := bufio.NewReader(conn)
		code, text, err := readReply(r)
		if err != nil {
			return nil, fmt.Errorf("reading server greeting: %w", err)
		}
		if code != 220 {
			return nil, fmt.Errorf("unexpected server greeting: %d %s", code, text)
		}
		if _, err := conn.Write([]byte("AUTH TLS\r\n")); err != nil {
			return nil, err
		}
		code, text, err = readReply(r)
		if err != nil {
			return nil, fmt.Errorf("reading AUTH TLS reply: %w", err)
		}
		if code != 234 {
			return nil, &tlsUnsupportedError{reply: fmt.Sprintf("%d %s", code, text)}
		}
	}
	tc := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: true}) //nolint:gosec // inspection only; trust is decided by the caller
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("TLS handshake: %w", err)
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("server presented no certificate")
	}
	_ = tc.Close()
	return certs, nil
}

type tlsUnsupportedError struct{ reply string }

func (e *tlsUnsupportedError) Error() string {
	return "server does not support explicit TLS (AUTH TLS answered " + e.reply + ")"
}

// readReply reads one (possibly multi-line) FTP reply.
func readReply(r *bufio.Reader) (int, string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 4 {
		return 0, "", fmt.Errorf("malformed reply %q", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, "", fmt.Errorf("malformed reply %q", line)
	}
	text := line[4:]
	if line[3] == '-' {
		end := line[:3] + " "
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return 0, "", err
			}
			l = strings.TrimRight(l, "\r\n")
			if strings.HasPrefix(l, end) {
				text += "\n" + l[4:]
				break
			}
		}
	}
	return code, text, nil
}

// Inspect checks the chain against the system roots and the pin store.
func Inspect(certs []*x509.Certificate, host string, port int, pins *PinStore, now time.Time) CertInfo {
	leaf := certs[0]
	info := CertInfo{Leaf: leaf, Fingerprint: Fingerprint(leaf.Raw)}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, CurrentTime: now})
	if err == nil {
		info.Trusted = true
		info.Pinnable = true
	} else {
		info.Problem, info.ProblemText = classifyCertError(err)
		// A pinned certificate is used as its own root: only host name and
		// validity still need to match.
		roots := x509.NewCertPool()
		roots.AddCert(leaf)
		_, perr := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, CurrentTime: now})
		info.Pinnable = perr == nil
		if !info.Pinnable && info.Problem == ProblemUnknownAuthority {
			info.Problem, info.ProblemText = classifyCertError(perr)
		}
	}
	if pins != nil {
		if file, ok := pins.Matches(host, port, info.Fingerprint); ok {
			info.Pinned = true
			info.PinFile = file
		}
	}
	return info
}

func classifyCertError(err error) (string, string) {
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	var ci x509.CertificateInvalidError
	switch {
	case errors.As(err, &ua):
		return ProblemUnknownAuthority, "The certificate is not issued by a trusted certificate authority (self-signed or private CA)."
	case errors.As(err, &he):
		return ProblemNameMismatch, "The certificate is not valid for this host name: " + he.Error()
	case errors.As(err, &ci):
		switch ci.Reason {
		case x509.Expired:
			if strings.Contains(ci.Detail, "before") {
				return ProblemNotYetValid, "The certificate is not valid yet."
			}
			return ProblemExpired, "The certificate has expired."
		}
		return ProblemOther, ci.Error()
	}
	return ProblemOther, err.Error()
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]+$`)

// ValidateHost rejects empty host names and characters that do not belong in
// one (and would be unsafe in file names or environment values).
func ValidateHost(host string) error {
	if host == "" || len(host) > 253 || !hostPattern.MatchString(host) || strings.HasPrefix(host, "-") {
		return fmt.Errorf("invalid host name %q", host)
	}
	return nil
}

// PinStore keeps confirmed certificates as PEM files, one per host and port.
type PinStore struct {
	Dir string
}

// File returns the pin file path for host and port.
func (p *PinStore) File(host string, port int) string {
	name := strings.NewReplacer(":", "_", "[", "", "]", "").Replace(strings.ToLower(host))
	return filepath.Join(p.Dir, fmt.Sprintf("%s_%d.pem", name, port))
}

// Matches reports whether the pinned certificate for host:port has the given
// fingerprint.
func (p *PinStore) Matches(host string, port int, fingerprint string) (string, bool) {
	file := p.File(host, port)
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", false
	}
	return file, Fingerprint(block.Bytes) == NormalizeFingerprint(fingerprint)
}

// Pin stores leaf as the only trusted certificate for host:port. An earlier
// pin for the same host and port is replaced; the caller must only do that
// after the user confirmed the new fingerprint.
func (p *PinStore) Pin(host string, port int, leaf *x509.Certificate) (string, error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return "", err
	}
	file := p.File(host, port)
	tmp := file + ".tmp"
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, file); err != nil {
		return "", err
	}
	return file, nil
}

// Owns reports whether file is a pin file inside the store.
func (p *PinStore) Owns(file string) bool {
	rel, err := filepath.Rel(p.Dir, filepath.Clean(file))
	return err == nil && !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)) && strings.HasSuffix(rel, ".pem")
}
