// relyper: Relyper Backup addition, not part of upstream Backrest.
package ftpdiag

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRevealRcloneValue(t *testing.T) {
	// Produced by `rclone obscure 'Hällo $ecret!'` (rclone v1.68.2).
	got, err := Reveal("UKtQ9eclH7F_0h5ZZAazlGBNOw4zyvTfU7zWmFBj")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hällo $ecret!" {
		t.Fatalf("Reveal = %q", got)
	}
}

func TestObscureRoundTrip(t *testing.T) {
	for _, pw := range []string{"", "a", "s3cret pass!", strings.Repeat("x", 100)} {
		o, err := Obscure(pw)
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) > 3 && strings.Contains(o, pw) {
			t.Fatalf("obscured value contains the password")
		}
		back, err := Reveal(o)
		if err != nil || back != pw {
			t.Fatalf("round trip %q -> %q (%v)", pw, back, err)
		}
	}
	a, _ := Obscure("same")
	b, _ := Obscure("same")
	if a == b {
		t.Fatal("obscure must use a random IV")
	}
	if _, err := Reveal("!!"); err == nil {
		t.Fatal("expected error for invalid input")
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	want := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, in := range []string{hex, strings.ToUpper(hex), want, " " + strings.ToLower(want) + " ", "SHA256:" + hex} {
		if got := NormalizeFingerprint(in); got != want {
			t.Errorf("NormalizeFingerprint(%q) = %q", in, got)
		}
	}
}

func TestReadReply(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("220-Welcome\r\n220-to the server\r\n220 ready\r\n234 AUTH TLS ok\r\n"))
	code, text, err := readReply(r)
	if err != nil || code != 220 || !strings.Contains(text, "ready") {
		t.Fatalf("got %d %q %v", code, text, err)
	}
	code, _, err = readReply(r)
	if err != nil || code != 234 {
		t.Fatalf("got %d %v", code, err)
	}
}

func selfSigned(t *testing.T, names []string, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInspectAndPin(t *testing.T) {
	now := time.Now()
	pins := &PinStore{Dir: filepath.Join(t.TempDir(), "ftp-certs")}

	self := selfSigned(t, []string{"ftp.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	info := Inspect([]*x509.Certificate{self}, "ftp.example.com", 21, pins, now)
	if info.Trusted || info.Problem != ProblemUnknownAuthority || !info.Pinnable || info.Pinned {
		t.Fatalf("self-signed: %+v", info)
	}
	file, err := pins.Pin("ftp.example.com", 21, self)
	if err != nil {
		t.Fatal(err)
	}
	if !pins.Owns(file) || pins.Owns("/etc/passwd") || pins.Owns(filepath.Join(pins.Dir, "..", "x.pem")) {
		t.Fatal("Owns is wrong")
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Fatalf("pin file mode %v", st.Mode().Perm())
	}
	info = Inspect([]*x509.Certificate{self}, "ftp.example.com", 21, pins, now)
	if !info.Pinned || info.PinFile != file {
		t.Fatalf("expected pinned: %+v", info)
	}
	// Another port is another pin.
	if Inspect([]*x509.Certificate{self}, "ftp.example.com", 990, pins, now).Pinned {
		t.Fatal("pin must be per port")
	}
	// A different certificate for the same host is not pinned.
	other := selfSigned(t, []string{"ftp.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	if Inspect([]*x509.Certificate{other}, "ftp.example.com", 21, pins, now).Pinned {
		t.Fatal("different certificate must not match the pin")
	}

	wrongName := selfSigned(t, []string{"other.example.net"}, now.Add(-time.Hour), now.Add(time.Hour))
	info = Inspect([]*x509.Certificate{wrongName}, "ftp.example.com", 21, nil, now)
	if info.Pinnable || info.Problem != ProblemNameMismatch {
		t.Fatalf("name mismatch: %+v", info)
	}

	expired := selfSigned(t, []string{"ftp.example.com"}, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	info = Inspect([]*x509.Certificate{expired}, "ftp.example.com", 21, nil, now)
	if info.Pinnable || info.Problem != ProblemExpired {
		t.Fatalf("expired: %+v", info)
	}
}

func TestValidateHost(t *testing.T) {
	for _, h := range []string{"wp1171826.server-he.de", "127.0.0.1", "::1"} {
		if err := ValidateHost(h); err != nil {
			t.Errorf("%q: %v", h, err)
		}
	}
	for _, h := range []string{"", "ftp://x", "a b", "x/../y", "-oProxy", "x\n"} {
		if ValidateHost(h) == nil {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestEnv(t *testing.T) {
	o := Options{Host: "h", User: "u", Mode: TLSExplicit, CertFile: "/data/relyper/ftp-certs/h_21.pem"}
	got := strings.Join(Env("hosteurope", o, "OBS"), " ")
	want := "RCLONE_CONFIG_HOSTEUROPE_TYPE=ftp RCLONE_CONFIG_HOSTEUROPE_HOST=h RCLONE_CONFIG_HOSTEUROPE_PORT=21 RCLONE_CONFIG_HOSTEUROPE_USER=u RCLONE_CONFIG_HOSTEUROPE_PASS=OBS RCLONE_CONFIG_HOSTEUROPE_EXPLICIT_TLS=true SSL_CERT_FILE=/data/relyper/ftp-certs/h_21.pem"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	o = Options{Host: "h", User: "u", Mode: TLSImplicit, SkipCertificateCheck: true, CertFile: "ignored"}
	got = strings.Join(Env("r", o, "OBS"), " ")
	if !strings.Contains(got, "_PORT=990") || !strings.Contains(got, "_TLS=true") || !strings.Contains(got, "NO_CHECK_CERTIFICATE=true") || strings.Contains(got, "SSL_CERT_FILE") {
		t.Fatalf("implicit/skip: %s", got)
	}
	got = strings.Join(Env("r", Options{Host: "h", User: "u", Mode: TLSNone}, "OBS"), " ")
	if strings.Contains(got, "TLS") || strings.Contains(got, "CERT") {
		t.Fatalf("plain: %s", got)
	}
}

func TestClassifyRclone(t *testing.T) {
	cases := map[string]string{
		`2026/11/28 02:03:12 CRITICAL: Failed to create file system for "T:": NewFs: failed to make FTP connection to "127.0.0.1:2121": 530 Authentication failed.`:                                   CategoryAuthentication,
		`CRITICAL: Failed to create file system for "T:": NewFs: failed to make FTP connection to "127.0.0.1:2121": 500 Command "AUTH" not understood.`:                                               CategoryTLSUnsupported,
		`CRITICAL: Failed to create file system for "T:": NewFs: failed to make FTP connection to "127.0.0.1:2122": tls: failed to verify certificate: x509: certificate signed by unknown authority`: CategoryCertificate,
		`CRITICAL: Failed to create file system for "T:": NewFs: failed to make FTP connection to "127.0.0.1:2122": SSL/TLS required on the control channel.`:                                         CategoryTLSRequired,
		`failed to make FTP connection to "x:21": 530 Non-anonymous sessions must use encryption.`:                                                                                                    CategoryTLSRequired,
		"ERROR : : error listing: 501 No such directory.\nNOTICE: Failed to lsjson with 2 errors: last error was: error in ListJSON: 501 No such directory.":                                          CategoryPathNotFound,
		`ERROR : x: Failed to copy: 553 Could not create file.`: CategoryPermission,
		`dial tcp 127.0.0.1:2199: connect: connection refused`:  CategoryConnectionRefused,
	}
	for in, want := range cases {
		if got := classifyRclone(in).Category; got != want {
			t.Errorf("%s\n  got %s want %s", in, got, want)
		}
	}
}

func TestInputValidation(t *testing.T) {
	r := TestConnection(context.Background(), Options{Host: "ftp://x", User: "u"})
	if r.OK || r.Error == nil || r.Error.Category != CategoryInvalidInput {
		t.Fatalf("%+v", r)
	}
	r = TestConnection(context.Background(), Options{Host: "x", Port: 21})
	if r.OK || r.Error.Category != CategoryInvalidInput {
		t.Fatalf("%+v", r)
	}
}

// TestE2E runs against a real FTP server when RELYPER_FTP_E2E is set to
// "host:port:user:password:mode[:skip]" (mode: explicit, implicit, none) and
// rclone is installed. Example with pyftpdlib, see the issue for #55.
func TestE2E(t *testing.T) {
	spec := os.Getenv("RELYPER_FTP_E2E")
	if spec == "" {
		t.Skip("RELYPER_FTP_E2E not set")
	}
	p := strings.Split(spec, ":")
	port, _ := strconv.Atoi(p[1])
	mode := map[string]TLSMode{"explicit": TLSExplicit, "implicit": TLSImplicit, "none": TLSNone}[p[4]]
	o := Options{Host: p[0], Port: port, User: p[2], Password: p[3], Mode: mode, Path: "e2e/" + strconv.FormatInt(time.Now().UnixNano(), 10), WriteTest: true, Timeout: 20 * time.Second}
	o.SkipCertificateCheck = len(p) > 5 && p[5] == "skip"
	if pin := os.Getenv("RELYPER_FTP_E2E_PIN_DIR"); pin != "" {
		o.Pins = &PinStore{Dir: pin}
		certs, err := FetchCertificates(context.Background(), o.Host, o.Port, o.Mode, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if o.CertFile, err = o.Pins.Pin(o.Host, o.Port, certs[0]); err != nil {
			t.Fatal(err)
		}
	}
	r := TestConnection(context.Background(), o)
	for _, s := range r.Steps {
		t.Logf("%-16s %d %s %s", s.Name, s.Status, s.Message, s.Hint)
	}
	if !r.OK || r.Repository != RepoMissing {
		t.Fatalf("result %+v", r.Error)
	}
}
