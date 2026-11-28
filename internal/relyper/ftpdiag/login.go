// relyper: Relyper Backup addition, not part of upstream Backrest.
package ftpdiag

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// LoginResult is the outcome of a direct FTP login, independent of rclone.
type LoginResult struct {
	OK bool
	// Reply is the server's raw answer to PASS (or USER), e.g.
	// "530 Login incorrect.". It never contains the password.
	Reply string
	// Stage is where it stopped: greeting, auth_tls, tls, user, pass.
	Stage string
}

// ProbeLogin logs in with USER/PASS the way FileZilla does and returns the
// server's raw replies. TLS is not verified here; the TLS step of the test
// checks the certificate separately.
func ProbeLogin(ctx context.Context, host string, port int, mode TLSMode, user, password string, timeout time.Duration) (LoginResult, error) {
	if strings.ContainsAny(user+password, "\r\n") {
		return LoginResult{}, fmt.Errorf("user name and password must not contain line breaks")
	}
	if port == 0 {
		port = mode.DefaultPort()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return LoginResult{}, err
	}
	defer raw.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}
	var conn net.Conn = raw
	tlsCfg := &tls.Config{ServerName: host, InsecureSkipVerify: true} //nolint:gosec // certificate is checked by the TLS step
	if mode == TLSImplicit {
		tc := tls.Client(raw, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return LoginResult{Stage: "tls"}, err
		}
		conn = tc
	}
	r := bufio.NewReader(conn)
	code, text, err := readReply(r)
	if err != nil {
		return LoginResult{Stage: "greeting"}, err
	}
	if code != 220 {
		return LoginResult{Stage: "greeting", Reply: fmt.Sprintf("%d %s", code, text)}, nil
	}
	if mode == TLSExplicit {
		if _, err := fmt.Fprintf(conn, "AUTH TLS\r\n"); err != nil {
			return LoginResult{Stage: "auth_tls"}, err
		}
		code, text, err = readReply(r)
		if err != nil {
			return LoginResult{Stage: "auth_tls"}, err
		}
		if code != 234 {
			return LoginResult{Stage: "auth_tls", Reply: fmt.Sprintf("%d %s", code, text)}, nil
		}
		tc := tls.Client(raw, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return LoginResult{Stage: "tls"}, err
		}
		conn = tc
		r = bufio.NewReader(conn)
	}
	cmd := func(line string) (int, string, error) {
		if _, err := fmt.Fprintf(conn, "%s\r\n", line); err != nil {
			return 0, "", err
		}
		return readReply(r)
	}
	code, text, err = cmd("USER " + user)
	if err != nil {
		return LoginResult{Stage: "user"}, err
	}
	switch {
	case code == 230:
		_, _, _ = cmd("QUIT")
		return LoginResult{OK: true, Stage: "user", Reply: fmt.Sprintf("%d %s", code, text)}, nil
	case code != 331 && code != 332:
		return LoginResult{Stage: "user", Reply: fmt.Sprintf("%d %s", code, text)}, nil
	}
	code, text, err = cmd("PASS " + password)
	if err != nil {
		return LoginResult{Stage: "pass"}, err
	}
	res := LoginResult{OK: code == 230 || code == 202, Stage: "pass", Reply: fmt.Sprintf("%d %s", code, firstLine(text))}
	_, _, _ = cmd("QUIT")
	return res, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// credentialHints points at typical copy/paste problems without revealing
// the credentials.
func credentialHints(user, password string) []string {
	var hints []string
	if user != strings.TrimSpace(user) {
		hints = append(hints, "The user name starts or ends with a space.")
	}
	if password != strings.TrimSpace(password) {
		hints = append(hints, "The password starts or ends with a space (often from copy and paste).")
	}
	for _, c := range password {
		if c > unicode.MaxASCII {
			hints = append(hints, "The password contains non-ASCII characters (e.g. umlauts, §, €). Relyper Backup sends them as UTF-8; some FTP servers expect another character set. Try a password with only ASCII characters.")
			break
		}
	}
	return hints
}
