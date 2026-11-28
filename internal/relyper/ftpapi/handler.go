// Package ftpapi exposes package ftpdiag as the relyper.v1.RelyperFtp Connect
// service. It is mounted behind Backrest's authentication middleware; the
// role gate keeps read-only users out (deny by default).
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package ftpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	relyperv1 "github.com/garethgeorge/backrest/gen/go/relyper/v1"
	"github.com/garethgeorge/backrest/gen/go/relyper/v1/v1connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/auth"
	"github.com/garethgeorge/backrest/internal/relyper/ftpdiag"
	"go.uber.org/zap"
)

const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 120 * time.Second
	scanTimeout    = 15 * time.Second
)

// Handler implements v1connect.RelyperFtpHandler.
type Handler struct {
	Pins *ftpdiag.PinStore
	// Rclone overrides the rclone binary (tests); empty means PATH.
	Rclone string
}

var _ v1connect.RelyperFtpHandler = (*Handler)(nil)

// PinDir is where confirmed FTPS certificates are stored.
func PinDir(dataDir string) string {
	return filepath.Join(dataDir, "relyper", "ftp-certs")
}

// New returns the route and HTTP handler for the service.
func New(dataDir string) (string, http.Handler) {
	return v1connect.NewRelyperFtpHandler(&Handler{Pins: &ftpdiag.PinStore{Dir: PinDir(dataDir)}})
}

func (h *Handler) ObscurePassword(ctx context.Context, req *connect.Request[relyperv1.ObscurePasswordRequest]) (*connect.Response[relyperv1.ObscurePasswordResponse], error) {
	if req.Msg.Password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("password is empty"))
	}
	o, err := ftpdiag.Obscure(req.Msg.Password)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not obscure password"))
	}
	return connect.NewResponse(&relyperv1.ObscurePasswordResponse{Obscured: o}), nil
}

func (h *Handler) ScanCertificate(ctx context.Context, req *connect.Request[relyperv1.ScanCertificateRequest]) (*connect.Response[relyperv1.ScanCertificateResponse], error) {
	m := req.Msg
	mode, port, err := modePort(m.TlsMode, m.Port)
	if err != nil {
		return nil, err
	}
	if mode == ftpdiag.TLSNone {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("plain FTP has no certificate"))
	}
	if err := ftpdiag.ValidateHost(m.Host); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	certs, err := ftpdiag.FetchCertificates(ctx, m.Host, port, mode, scanTimeout)
	if err != nil {
		return connect.NewResponse(&relyperv1.ScanCertificateResponse{Error: &relyperv1.Diagnostic{Category: ftpdiag.CategoryTLSUnsupported, Message: err.Error(), Hint: "Check host, port and encryption: explicit TLS normally uses port 21, implicit FTPS port 990."}}), nil
	}
	info := ftpdiag.Inspect(certs, m.Host, port, h.Pins, time.Now())
	return connect.NewResponse(&relyperv1.ScanCertificateResponse{Certificate: toCertInfo(info)}), nil
}

func (h *Handler) TrustCertificate(ctx context.Context, req *connect.Request[relyperv1.TrustCertificateRequest]) (*connect.Response[relyperv1.TrustCertificateResponse], error) {
	m := req.Msg
	mode, port, err := modePort(m.TlsMode, m.Port)
	if err != nil {
		return nil, err
	}
	if mode == ftpdiag.TLSNone {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("plain FTP has no certificate"))
	}
	if err := ftpdiag.ValidateHost(m.Host); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	want := ftpdiag.NormalizeFingerprint(m.FingerprintSha256)
	certs, err := ftpdiag.FetchCertificates(ctx, m.Host, port, mode, scanTimeout)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	info := ftpdiag.Inspect(certs, m.Host, port, nil, time.Now())
	if info.Fingerprint != want {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the server now presents a different certificate than the one you confirmed; scan again and compare"))
	}
	if !info.Pinnable {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this certificate cannot be pinned: "+info.ProblemText))
	}
	file, err := h.Pins.Pin(m.Host, port, info.Leaf)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Audit trail: who trusted what is visible in the server log.
	zap.S().Infow("trusted FTPS certificate", "user", currentUser(ctx), "host", m.Host, "port", port, "fingerprint", info.Fingerprint, "file", file)
	return connect.NewResponse(&relyperv1.TrustCertificateResponse{CertFile: file}), nil
}

func (h *Handler) TestConnection(ctx context.Context, req *connect.Request[relyperv1.FtpTestConnectionRequest]) (*connect.Response[relyperv1.FtpTestConnectionResponse], error) {
	m := req.Msg
	mode, port, err := modePort(m.TlsMode, m.Port)
	if err != nil {
		return nil, err
	}
	password := m.Password
	if password == "" && m.ObscuredPassword != "" {
		password, err = ftpdiag.Reveal(m.ObscuredPassword)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the stored FTP password is not readable; enter it again"))
		}
	}
	if m.CertFile != "" && !h.Pins.Owns(m.CertFile) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate file is not a confirmed FTPS certificate"))
	}
	timeout := time.Duration(m.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	} else if timeout > maxTimeout {
		timeout = maxTimeout
	}
	r := ftpdiag.TestConnection(ctx, ftpdiag.Options{
		Host:                 m.Host,
		Port:                 port,
		User:                 m.Username,
		Password:             password,
		Path:                 m.RemotePath,
		Mode:                 mode,
		CertFile:             m.CertFile,
		SkipCertificateCheck: m.SkipCertificateCheck,
		WriteTest:            m.WriteTest,
		Timeout:              timeout,
		Pins:                 h.Pins,
		Rclone:               h.Rclone,
	})
	resp := &relyperv1.FtpTestConnectionResponse{Ok: r.OK, RepositoryState: toRepoState(r.Repository)}
	if r.Error != nil {
		resp.Error = &relyperv1.Diagnostic{Category: r.Error.Category, Message: r.Error.Message, Hint: r.Error.Hint}
	}
	for _, s := range r.Steps {
		resp.Steps = append(resp.Steps, &relyperv1.TestStep{Name: s.Name, Status: toStepStatus(s.Status), Message: s.Message, Hint: s.Hint, Category: s.Category, DurationMs: s.Duration.Milliseconds()})
	}
	return connect.NewResponse(resp), nil
}

func modePort(m relyperv1.FtpTlsMode, port int32) (ftpdiag.TLSMode, int, error) {
	var mode ftpdiag.TLSMode
	switch m {
	case relyperv1.FtpTlsMode_FTP_TLS_MODE_UNSPECIFIED, relyperv1.FtpTlsMode_FTP_TLS_MODE_EXPLICIT:
		mode = ftpdiag.TLSExplicit
	case relyperv1.FtpTlsMode_FTP_TLS_MODE_IMPLICIT:
		mode = ftpdiag.TLSImplicit
	case relyperv1.FtpTlsMode_FTP_TLS_MODE_NONE:
		mode = ftpdiag.TLSNone
	default:
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown TLS mode"))
	}
	if port < 0 || port > 65535 {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("port must be between 1 and 65535"))
	}
	p := int(port)
	if p == 0 {
		p = mode.DefaultPort()
	}
	return mode, p, nil
}

func toCertInfo(i ftpdiag.CertInfo) *relyperv1.CertificateInfo {
	c := &relyperv1.CertificateInfo{
		Subject:           i.Leaf.Subject.String(),
		Issuer:            i.Leaf.Issuer.String(),
		DnsNames:          i.Leaf.DNSNames,
		NotBeforeUnix:     i.Leaf.NotBefore.Unix(),
		NotAfterUnix:      i.Leaf.NotAfter.Unix(),
		FingerprintSha256: i.Fingerprint,
		Problem:           i.Problem,
		ProblemMessage:    i.ProblemText,
		Pinnable:          i.Pinnable,
	}
	switch {
	case i.Pinned:
		c.Status = relyperv1.CertificateStatus_CERTIFICATE_STATUS_PINNED
		c.CertFile = i.PinFile
	case i.Trusted:
		c.Status = relyperv1.CertificateStatus_CERTIFICATE_STATUS_TRUSTED
	default:
		c.Status = relyperv1.CertificateStatus_CERTIFICATE_STATUS_UNTRUSTED
	}
	return c
}

func currentUser(ctx context.Context) string {
	if u, ok := ctx.Value(auth.UserContextKey).(*v1.User); ok && u != nil {
		return u.GetName()
	}
	return "(authentication disabled)"
}

func toStepStatus(s ftpdiag.StepStatus) relyperv1.StepStatus {
	switch s {
	case ftpdiag.StepOK:
		return relyperv1.StepStatus_STEP_STATUS_OK
	case ftpdiag.StepWarning:
		return relyperv1.StepStatus_STEP_STATUS_WARNING
	case ftpdiag.StepFailed:
		return relyperv1.StepStatus_STEP_STATUS_FAILED
	case ftpdiag.StepSkipped:
		return relyperv1.StepStatus_STEP_STATUS_SKIPPED
	}
	return relyperv1.StepStatus_STEP_STATUS_UNSPECIFIED
}

func toRepoState(s ftpdiag.RepoState) relyperv1.RepositoryState {
	switch s {
	case ftpdiag.RepoExisting:
		return relyperv1.RepositoryState_REPOSITORY_STATE_EXISTING
	case ftpdiag.RepoEmpty:
		return relyperv1.RepositoryState_REPOSITORY_STATE_EMPTY
	case ftpdiag.RepoMissing:
		return relyperv1.RepositoryState_REPOSITORY_STATE_MISSING
	case ftpdiag.RepoNotRepository:
		return relyperv1.RepositoryState_REPOSITORY_STATE_NOT_REPOSITORY
	}
	return relyperv1.RepositoryState_REPOSITORY_STATE_UNSPECIFIED
}
