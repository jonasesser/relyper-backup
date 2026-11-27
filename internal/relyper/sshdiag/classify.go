package sshdiag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Stable diagnostic categories. They are part of the API (UI and MCP rely on
// them), so only add new ones; never rename.
const (
	CategoryInvalidInput      = "invalid_input"
	CategoryDNS               = "dns_failure"
	CategoryConnectionRefused = "connection_refused"
	CategoryTimeout           = "connection_timeout"
	CategoryNetwork           = "network_unreachable"
	CategoryProtocol          = "ssh_protocol_error"
	CategoryAlgorithm         = "incompatible_algorithms"
	CategoryHostKeyUnknown    = "host_key_unknown"
	CategoryHostKeyMismatch   = "host_key_mismatch"
	CategoryHostKeyRevoked    = "host_key_revoked"
	CategoryIdentityFile      = "identity_file_invalid"
	CategoryAuthentication    = "authentication_failed"
	CategorySFTPUnavailable   = "sftp_unavailable"
	CategoryPathNotFound      = "remote_path_not_found"
	CategoryPermission        = "permission_denied"
	CategoryRepository        = "repository_error"
	CategoryUnknown           = "unknown"
)

// Diagnostic is a classified failure with an actionable hint.
type Diagnostic struct {
	Category string
	Message  string
	Hint     string
}

func (d *Diagnostic) Error() string {
	if d.Hint == "" {
		return d.Message
	}
	return d.Message + " " + d.Hint
}

// Classify maps an error from dialing, the SSH handshake or SFTP into a
// Diagnostic. The message never contains secrets: callers must not wrap
// passwords or key material into errors.
func Classify(err error) *Diagnostic {
	if err == nil {
		return nil
	}
	var d *Diagnostic
	if errors.As(err, &d) {
		return d
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return &Diagnostic{CategoryDNS, fmt.Sprintf("DNS lookup for %q timed out.", dnsErr.Name), "Check the DNS server configured for the container."}
		}
		return &Diagnostic{CategoryDNS, fmt.Sprintf("The host name %q could not be resolved.", dnsErr.Name), "Check the spelling of the host name and the DNS configuration of the container."}
	}

	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return &Diagnostic{CategoryHostKeyRevoked, "The server presented a host key that is marked as revoked in known_hosts.", "Do not connect. Ask the provider whether the server key was replaced and why."}
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			return &Diagnostic{CategoryHostKeyUnknown, "The server's host key is not trusted yet.", "Fetch the host key, compare its fingerprint with the one your provider publishes, then trust it."}
		}
		return &Diagnostic{CategoryHostKeyMismatch, "The server presented a different host key than the one recorded in known_hosts.", "This can mean a reinstalled server or a man-in-the-middle attack. Confirm the new fingerprint with your provider before you change known_hosts by hand."}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return &Diagnostic{CategoryTimeout, "The connection timed out.", "Check host, port and firewall rules. Some providers only allow SSH from whitelisted IP addresses."}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Diagnostic{CategoryTimeout, "The connection timed out.", "Check host, port and firewall rules. Some providers only allow SSH from whitelisted IP addresses."}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return &Diagnostic{CategoryConnectionRefused, "The server refused the connection.", "Check the port. Many hosting providers run SSH on a port other than 22."}
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return &Diagnostic{CategoryNetwork, "The server is not reachable from this container.", "Check the network configuration and whether the container has outbound internet access."}
	}

	var passErr *ssh.PassphraseMissingError
	if errors.As(err, &passErr) {
		return &Diagnostic{CategoryIdentityFile, "The identity file is protected by a passphrase.", "restic runs unattended and cannot enter a passphrase. Use a key without a passphrase that is stored with restrictive permissions."}
	}

	var statusErr *sftp.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.FxCode() {
		case sftp.ErrSSHFxPermissionDenied:
			return &Diagnostic{CategoryPermission, "The SFTP server denied access.", "Check the permissions of the remote directory for this user."}
		case sftp.ErrSSHFxNoSuchFile:
			return &Diagnostic{CategoryPathNotFound, "The remote path does not exist.", "Check the path. Relative paths start in the user's home directory."}
		}
	}
	if errors.Is(err, os.ErrPermission) {
		return &Diagnostic{CategoryPermission, "The SFTP server denied access.", "Check the permissions of the remote directory for this user."}
	}
	if errors.Is(err, os.ErrNotExist) {
		return &Diagnostic{CategoryPathNotFound, "The remote path does not exist.", "Check the path. Relative paths start in the user's home directory."}
	}

	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no common algorithm"):
		return &Diagnostic{CategoryAlgorithm, "Client and server have no SSH algorithm in common (" + afterLast(msg, "ssh: ") + ").", "The server only offers algorithms considered insecure. Ask the provider to enable modern key exchange, cipher and host key algorithms."}
	case strings.Contains(lower, "unable to authenticate"), strings.Contains(lower, "no supported methods remain"):
		return &Diagnostic{CategoryAuthentication, "The server rejected the login.", "Check the user name. For key authentication, add the public key to ~/.ssh/authorized_keys on the server; for password authentication, check the password."}
	case strings.Contains(lower, "subsystem request failed"):
		return &Diagnostic{CategorySFTPUnavailable, "The server accepted the login but does not offer SFTP for this user.", "Ask the provider to enable SFTP access for this account."}
	case strings.Contains(lower, "connection reset"), strings.Contains(lower, "eof"), strings.Contains(lower, "handshake failed"):
		return &Diagnostic{CategoryProtocol, "The SSH connection was closed during the handshake.", "This often means the port does not speak SSH, or the server blocks this client (fail2ban, IP whitelist)."}
	case strings.Contains(lower, "permission denied"):
		return &Diagnostic{CategoryPermission, "Access was denied.", "Check the permissions for this user."}
	}
	return &Diagnostic{CategoryUnknown, msg, ""}
}

func afterLast(s, sep string) string {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[i+len(sep):]
	}
	return s
}
