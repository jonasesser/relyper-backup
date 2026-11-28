// relyper: Relyper Backup addition, not part of upstream Backrest.
package ftpapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	relyperv1 "github.com/garethgeorge/backrest/gen/go/relyper/v1"
	"github.com/garethgeorge/backrest/internal/relyper/ftpdiag"
)

func TestObscurePassword(t *testing.T) {
	h := &Handler{Pins: &ftpdiag.PinStore{Dir: t.TempDir()}}
	res, err := h.ObscurePassword(context.Background(), connect.NewRequest(&relyperv1.ObscurePasswordRequest{Password: "geheim"}))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ftpdiag.Reveal(res.Msg.Obscured); got != "geheim" {
		t.Fatalf("reveal = %q", got)
	}
	if _, err := h.ObscurePassword(context.Background(), connect.NewRequest(&relyperv1.ObscurePasswordRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty password: %v", err)
	}
}

func TestRejectsForeignCertFile(t *testing.T) {
	h := &Handler{Pins: &ftpdiag.PinStore{Dir: t.TempDir()}}
	_, err := h.TestConnection(context.Background(), connect.NewRequest(&relyperv1.FtpTestConnectionRequest{Host: "h", Username: "u", Password: "p", CertFile: "/etc/ssl/certs/ca-certificates.crt"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}

func TestModePort(t *testing.T) {
	if m, p, _ := modePort(relyperv1.FtpTlsMode_FTP_TLS_MODE_UNSPECIFIED, 0); m != ftpdiag.TLSExplicit || p != 21 {
		t.Fatalf("default: %v %d", m, p)
	}
	if m, p, _ := modePort(relyperv1.FtpTlsMode_FTP_TLS_MODE_IMPLICIT, 0); m != ftpdiag.TLSImplicit || p != 990 {
		t.Fatalf("implicit: %v %d", m, p)
	}
	if _, _, err := modePort(relyperv1.FtpTlsMode_FTP_TLS_MODE_NONE, 70000); err == nil {
		t.Fatal("port out of range accepted")
	}
}
