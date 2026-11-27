// Package sftpapi exposes the SFTP diagnostics of package sshdiag as the
// relyper.v1.RelyperSftp Connect service. It is mounted behind Backrest's
// authentication middleware.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package sftpapi

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
	"github.com/garethgeorge/backrest/internal/relyper/sshdiag"
	"go.uber.org/zap"
)

const maxTimeout = 60 * time.Second

// Handler implements v1connect.RelyperSftpHandler.
type Handler struct {
	// KnownHostsPath is the known_hosts file restic is pointed at, i.e.
	// filepath.Join(env.SSHDir(), "known_hosts").
	KnownHostsPath string
}

var _ v1connect.RelyperSftpHandler = (*Handler)(nil)

// New returns the route and HTTP handler for the service.
func New(sshDir string) (string, http.Handler) {
	return v1connect.NewRelyperSftpHandler(&Handler{KnownHostsPath: filepath.Join(sshDir, "known_hosts")})
}

func (h *Handler) ScanHostKeys(ctx context.Context, req *connect.Request[relyperv1.ScanHostKeysRequest]) (*connect.Response[relyperv1.ScanHostKeysResponse], error) {
	m := req.Msg
	if err := validatePort(m.Port); err != nil {
		return nil, err
	}
	resp := &relyperv1.ScanHostKeysResponse{KnownHostsPath: h.KnownHostsPath}
	keys, d := sshdiag.ScanHostKeys(ctx, m.Host, int(m.Port), h.KnownHostsPath, 15*time.Second)
	if d != nil {
		resp.Error = toDiagnostic(d)
		return connect.NewResponse(resp), nil
	}
	for _, k := range keys {
		resp.Keys = append(resp.Keys, &relyperv1.HostKey{Type: k.Type, FingerprintSha256: k.Fingerprint, Status: toKeyStatus(k.Status)})
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) TrustHostKey(ctx context.Context, req *connect.Request[relyperv1.TrustHostKeyRequest]) (*connect.Response[relyperv1.TrustHostKeyResponse], error) {
	m := req.Msg
	if err := validatePort(m.Port); err != nil {
		return nil, err
	}
	already, err := sshdiag.TrustHostKey(ctx, m.Host, int(m.Port), m.Type, m.FingerprintSha256, h.KnownHostsPath, 15*time.Second)
	if err != nil {
		switch {
		case errors.Is(err, sshdiag.ErrFingerprintChanged), errors.Is(err, sshdiag.ErrKeyConflict):
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		var d *sshdiag.Diagnostic
		if errors.As(err, &d) {
			return nil, connect.NewError(connect.CodeUnavailable, d)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !already {
		// Audit trail: who trusted what is visible in the server log.
		zap.S().Infow("trusted SFTP host key", "user", currentUser(ctx), "host", m.Host, "port", m.Port, "type", m.Type, "fingerprint", m.FingerprintSha256, "known_hosts", h.KnownHostsPath)
	}
	return connect.NewResponse(&relyperv1.TrustHostKeyResponse{KnownHostsPath: h.KnownHostsPath, AlreadyTrusted: already}), nil
}

func (h *Handler) TestConnection(ctx context.Context, req *connect.Request[relyperv1.TestConnectionRequest]) (*connect.Response[relyperv1.TestConnectionResponse], error) {
	m := req.Msg
	if err := validatePort(m.Port); err != nil {
		return nil, err
	}
	timeout := time.Duration(m.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	} else if timeout > maxTimeout {
		timeout = maxTimeout
	}
	r := sshdiag.TestConnection(ctx, sshdiag.Options{
		Host:           m.Host,
		Port:           int(m.Port),
		User:           m.Username,
		IdentityFile:   m.IdentityFile,
		Password:       m.Password,
		RemotePath:     m.RemotePath,
		KnownHostsPath: h.KnownHostsPath,
		WriteTest:      m.WriteTest,
		Timeout:        timeout,
	})
	resp := &relyperv1.TestConnectionResponse{
		Ok:              r.OK,
		Error:           toDiagnostic(r.Error),
		RepositoryState: toRepoState(r.Repository),
		ResolvedPath:    r.ResolvedPath,
		FreeBytes:       r.FreeBytes,
		TotalBytes:      r.TotalBytes,
	}
	for _, s := range r.Steps {
		resp.Steps = append(resp.Steps, &relyperv1.TestStep{Name: s.Name, Status: toStepStatus(s.Status), Message: s.Message, Hint: s.Hint, Category: s.Category, DurationMs: s.Duration.Milliseconds()})
	}
	return connect.NewResponse(resp), nil
}

// currentUser names the signed-in user for audit log lines.
func currentUser(ctx context.Context) string {
	if u, ok := ctx.Value(auth.UserContextKey).(*v1.User); ok && u != nil {
		return u.GetName()
	}
	return "(authentication disabled)"
}

func validatePort(p int32) error {
	if p < 0 || p > 65535 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("port must be between 1 and 65535"))
	}
	return nil
}

func toDiagnostic(d *sshdiag.Diagnostic) *relyperv1.Diagnostic {
	if d == nil {
		return nil
	}
	return &relyperv1.Diagnostic{Category: d.Category, Message: d.Message, Hint: d.Hint}
}

func toKeyStatus(s sshdiag.KeyStatus) relyperv1.HostKeyStatus {
	switch s {
	case sshdiag.KeyTrusted:
		return relyperv1.HostKeyStatus_HOST_KEY_STATUS_TRUSTED
	case sshdiag.KeyMismatch:
		return relyperv1.HostKeyStatus_HOST_KEY_STATUS_MISMATCH
	case sshdiag.KeyRevoked:
		return relyperv1.HostKeyStatus_HOST_KEY_STATUS_REVOKED
	default:
		return relyperv1.HostKeyStatus_HOST_KEY_STATUS_UNKNOWN
	}
}

func toStepStatus(s sshdiag.StepStatus) relyperv1.StepStatus {
	switch s {
	case sshdiag.StepOK:
		return relyperv1.StepStatus_STEP_STATUS_OK
	case sshdiag.StepWarning:
		return relyperv1.StepStatus_STEP_STATUS_WARNING
	case sshdiag.StepFailed:
		return relyperv1.StepStatus_STEP_STATUS_FAILED
	case sshdiag.StepSkipped:
		return relyperv1.StepStatus_STEP_STATUS_SKIPPED
	}
	return relyperv1.StepStatus_STEP_STATUS_UNSPECIFIED
}

func toRepoState(s sshdiag.RepoState) relyperv1.RepositoryState {
	switch s {
	case sshdiag.RepoExisting:
		return relyperv1.RepositoryState_REPOSITORY_STATE_EXISTING
	case sshdiag.RepoEmpty:
		return relyperv1.RepositoryState_REPOSITORY_STATE_EMPTY
	case sshdiag.RepoMissing:
		return relyperv1.RepositoryState_REPOSITORY_STATE_MISSING
	case sshdiag.RepoNotRepository:
		return relyperv1.RepositoryState_REPOSITORY_STATE_NOT_REPOSITORY
	}
	return relyperv1.RepositoryState_REPOSITORY_STATE_UNSPECIFIED
}
