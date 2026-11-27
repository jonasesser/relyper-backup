// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/relyper/mcpauth"
	"github.com/garethgeorge/backrest/internal/relyper/sshdiag"
	"google.golang.org/protobuf/proto"
)

// ---- input validation shared by repository and plan tools ----

var idRe = regexp.MustCompile(`^[a-zA-Z0-9_\-.]{1,64}$`)

func validateID(kind, id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("%s ID %q must be 1-64 characters of letters, digits, '-', '_' or '.'", kind, id)
	}
	return nil
}

// allowedEnv lists environment variables restic backends need. Anything
// else (RESTIC_PASSWORD_COMMAND, LD_PRELOAD, RCLONE_* program options …)
// could execute code or weaken security and is refused.
var allowedEnv = regexp.MustCompile(`^(AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN|AWS_DEFAULT_REGION|AWS_REGION|AWS_PROFILE|RESTIC_S3_STORAGE_CLASS|B2_ACCOUNT_ID|B2_ACCOUNT_KEY|AZURE_ACCOUNT_NAME|AZURE_ACCOUNT_KEY|AZURE_ACCOUNT_SAS|AZURE_ENDPOINT_SUFFIX|GOOGLE_PROJECT_ID|GOOGLE_APPLICATION_CREDENTIALS|GOOGLE_ACCESS_TOKEN|OS_[A-Z_]+|ST_AUTH|ST_USER|ST_KEY|RESTIC_REST_USERNAME|RESTIC_REST_PASSWORD|RESTIC_CACHE_DIR|RESTIC_COMPRESSION|RESTIC_PACK_SIZE|RESTIC_READ_CONCURRENCY)$`)

func validateEnv(env map[string]string) error {
	for k, v := range env {
		if !allowedEnv.MatchString(k) {
			return fmt.Errorf("environment variable %q is not allowed through MCP (allowed: storage credentials such as AWS_*, B2_*, AZURE_*, GOOGLE_*, OS_*, RESTIC_REST_*); set others in the web UI", k)
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("environment variable %q contains a line break", k)
		}
	}
	return nil
}

// safePath accepts absolute paths without characters that could break
// out of ssh option strings.
var safePathRe = regexp.MustCompile(`^/[A-Za-z0-9_\-./@+]*$`)

// RepoOptions are restic options that are safe to set through MCP.
type RepoOptions struct {
	LimitUploadKiB   int    `json:"limit_upload_kib,omitempty" jsonschema:"upload bandwidth limit in KiB/s (0 = unlimited)"`
	LimitDownloadKiB int    `json:"limit_download_kib,omitempty" jsonschema:"download bandwidth limit in KiB/s (0 = unlimited)"`
	Compression      string `json:"compression,omitempty" jsonschema:"auto (default), off or max"`
	PackSizeMiB      int    `json:"pack_size_mib,omitempty" jsonschema:"target pack size in MiB (restic default 16)"`
}

// SFTPSettings configure how restic runs ssh for an sftp: repository.
type SFTPSettings struct {
	IdentityFile string `json:"identity_file,omitempty" jsonschema:"absolute path of the private key inside the container, e.g. the key_path returned by create_sftp_key"`
	Port         int    `json:"port,omitempty" jsonschema:"SSH port (default 22)"`
}

func (s *Server) knownHostsPath() string {
	return filepath.Join(s.d.SSHDir, "known_hosts")
}

// sftpFlag builds the sftp.args option the same way the web UI does:
// BatchMode, identity file, port, Relyper's known_hosts.
func (s *Server) sftpFlag(in *SFTPSettings) (string, error) {
	args := "-oBatchMode=yes"
	if in.IdentityFile != "" {
		if !safePathRe.MatchString(in.IdentityFile) {
			return "", fmt.Errorf("identity_file %q must be an absolute path of letters, digits and -_./@+", in.IdentityFile)
		}
		args += " -i " + in.IdentityFile
	}
	if in.Port != 0 {
		if in.Port < 1 || in.Port > 65535 {
			return "", errors.New("sftp port must be between 1 and 65535")
		}
		if in.Port != 22 {
			args += " -p " + strconv.Itoa(in.Port)
		}
	}
	args += " -oUserKnownHostsFile=" + s.knownHostsPath()
	return "--option=sftp.args=" + args, nil
}

func optionFlags(o *RepoOptions) ([]string, error) {
	if o == nil {
		return nil, nil
	}
	var out []string
	if o.LimitUploadKiB < 0 || o.LimitDownloadKiB < 0 || o.PackSizeMiB < 0 {
		return nil, errors.New("options must not be negative")
	}
	if o.LimitUploadKiB > 0 {
		out = append(out, "--limit-upload="+strconv.Itoa(o.LimitUploadKiB))
	}
	if o.LimitDownloadKiB > 0 {
		out = append(out, "--limit-download="+strconv.Itoa(o.LimitDownloadKiB))
	}
	switch o.Compression {
	case "", "auto":
	case "off", "max":
		out = append(out, "--compression="+o.Compression)
	default:
		return nil, errors.New("compression must be auto, off or max")
	}
	if o.PackSizeMiB > 0 {
		if o.PackSizeMiB > 128 {
			return nil, errors.New("pack_size_mib must be at most 128")
		}
		out = append(out, "--pack-size="+strconv.Itoa(o.PackSizeMiB))
	}
	return out, nil
}

// managedFlagPrefixes are the flags the MCP tools own; other flags (set in
// the web UI) are kept untouched.
var managedFlagPrefixes = []string{"--option=sftp.args=", "--limit-upload=", "--limit-download=", "--compression=", "--pack-size="}

func isManagedFlag(f string) bool {
	return slices.ContainsFunc(managedFlagPrefixes, func(p string) bool { return strings.HasPrefix(f, p) })
}

// allowedBackupFlag lists restic backup flags that cannot run commands or
// read files outside the plan.
var allowedBackupFlag = regexp.MustCompile(`^(--one-file-system|--exclude-caches|--exclude-larger-than=[0-9]+[kKmMgGtT]?|--exclude-if-present=[A-Za-z0-9_.\-]+|--ignore-inode|--ignore-ctime|--with-atime|--no-scan|--read-concurrency=[0-9]+|--host=[A-Za-z0-9_.\-]+|--tag=[A-Za-z0-9_.\-:]+)$`)

func validateBackupFlags(flags []string) error {
	for _, f := range flags {
		if !allowedBackupFlag.MatchString(f) {
			return fmt.Errorf("backup flag %q is not allowed through MCP (allowed: --one-file-system, --exclude-caches, --exclude-larger-than=, --exclude-if-present=, --ignore-inode, --ignore-ctime, --with-atime, --no-scan, --read-concurrency=, --host=, --tag=)", f)
		}
	}
	return nil
}

func validatePaths(paths []string) (warnings []string, err error) {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("path %q must be absolute", p)
		}
		if strings.ContainsAny(p, "\n\r\x00") {
			return nil, fmt.Errorf("path %q contains a control character", p)
		}
		if _, err := os.Stat(p); err != nil {
			warnings = append(warnings, fmt.Sprintf("path %q does not exist inside the container yet (is the volume mounted?)", p))
		}
	}
	return warnings, nil
}

func validatePatterns(kind string, pats []string) error {
	for _, p := range pats {
		if p == "" || strings.ContainsAny(p, "\n\r\x00") {
			return fmt.Errorf("%s pattern %q is empty or contains a control character", kind, p)
		}
	}
	return nil
}

func validateURI(uri string) error {
	if strings.TrimSpace(uri) != uri || uri == "" {
		return errors.New("uri must not be empty or have surrounding spaces")
	}
	if strings.ContainsAny(uri, "\n\r\x00") {
		return errors.New("uri contains a control character")
	}
	if strings.HasPrefix(uri, "-") {
		return errors.New("uri must not start with '-'")
	}
	switch repoType(uri) {
	case "local", "sftp", "s3", "b2", "azure", "gs", "rest", "swift", "rclone":
	default:
		return fmt.Errorf("unsupported repository type in %q (use a path, sftp:, s3:, b2:, azure:, gs:, rest:, swift: or rclone:)", uri)
	}
	if repoType(uri) == "sftp" {
		host := sftpHost(uri)
		if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " ;|&$`'\"\\") {
			return fmt.Errorf("invalid SFTP host in %q", uri)
		}
	}
	return nil
}

// sftpHost returns the host part of sftp:user@host:/path or
// sftp://user@host:port/path.
func sftpHost(uri string) string {
	rest := strings.TrimPrefix(uri, "sftp:")
	rest = strings.TrimPrefix(rest, "//")
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if i := strings.IndexAny(rest, ":/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// transform applies fn to a clone of the config and saves it with a new
// modification number, so web UI clients holding an older copy get a
// conflict instead of silently overwriting the change.
func (s *Server) transform(fn func(cfg *v1.Config) error) (*v1.Config, error) {
	var saved *v1.Config
	err := s.d.Config.Transform(func(cfg *v1.Config) (*v1.Config, error) {
		if err := fn(cfg); err != nil {
			return nil, err
		}
		cfg.Modno++
		saved = cfg
		return cfg, nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// ---- repository tools ----

type CreateRepoIn struct {
	ID          string            `json:"id" jsonschema:"short unique ID, e.g. hosteurope or b2-offsite"`
	URI         string            `json:"uri" jsonschema:"restic repository location, e.g. sftp:user@host:/backups/relyper, s3:https://s3.eu-central-1.amazonaws.com/bucket/path, b2:bucket:path, rest:https://host/, /local/path"`
	Password    string            `json:"password" jsonschema:"repository encryption password; for an existing repository its current password. Without it backups cannot be restored: the user must keep a copy."`
	Env         map[string]string `json:"env,omitempty" jsonschema:"storage credentials as environment variables, e.g. AWS_ACCESS_KEY_ID; values are stored but never returned"`
	SFTP        *SFTPSettings     `json:"sftp,omitempty" jsonschema:"for sftp: repositories"`
	Options     *RepoOptions      `json:"options,omitempty"`
	Maintenance *RepoMaintenance  `json:"maintenance,omitempty" jsonschema:"prune and check schedules; default: check weekly (Sunday 03:00), prune monthly (1st, 04:00)"`
	AutoUnlock  bool              `json:"auto_unlock,omitempty" jsonschema:"remove stale locks automatically (only if no other machine uses the repository)"`
}

type UpdateRepoIn struct {
	RepoID      string            `json:"repo_id"`
	URI         *string           `json:"uri,omitempty" jsonschema:"new location of the same repository (e.g. after a move)"`
	Password    *string           `json:"password,omitempty" jsonschema:"replace the stored password (must be the repository's real password; this does not change the repository key)"`
	EnvSet      map[string]string `json:"env_set,omitempty" jsonschema:"environment variables to add or replace"`
	EnvRemove   []string          `json:"env_remove,omitempty" jsonschema:"names of environment variables to remove"`
	SFTP        *SFTPSettings     `json:"sftp,omitempty"`
	Options     *RepoOptions      `json:"options,omitempty" jsonschema:"replaces all MCP-managed restic options"`
	Maintenance *RepoMaintenance  `json:"maintenance,omitempty" jsonschema:"replaces prune and check settings; fields left out are disabled"`
	AutoUnlock  *bool             `json:"auto_unlock,omitempty"`
}

type RepoChangeOut struct {
	Repository RepoView `json:"repository"`
	Warnings   []string `json:"warnings,omitempty"`
	Message    string   `json:"message"`
}

func defaultMaintenance() *RepoMaintenance {
	return &RepoMaintenance{
		PruneSchedule:         &Schedule{Kind: "cron", Cron: "0 4 1 * *"},
		PruneMaxUnusedPercent: 10,
		CheckSchedule:         &Schedule{Kind: "cron", Cron: "0 3 * * 0"},
	}
}

func applyMaintenance(r *v1.Repo, m *RepoMaintenance) error {
	ps, err := scheduleToProto(m.PruneSchedule)
	if err != nil {
		return fmt.Errorf("prune_schedule: %w", err)
	}
	cs, err := scheduleToProto(m.CheckSchedule)
	if err != nil {
		return fmt.Errorf("check_schedule: %w", err)
	}
	if ps == nil {
		ps = &v1.Schedule{Schedule: &v1.Schedule_Disabled{Disabled: true}}
	}
	if cs == nil {
		cs = &v1.Schedule{Schedule: &v1.Schedule_Disabled{Disabled: true}}
	}
	if m.PruneMaxUnusedPercent < 0 || m.PruneMaxUnusedPercent > 100 || m.CheckReadDataPercent < 0 || m.CheckReadDataPercent > 100 {
		return errors.New("percentages must be between 0 and 100")
	}
	unused := m.PruneMaxUnusedPercent
	if unused == 0 {
		unused = 10
	}
	r.PrunePolicy = &v1.PrunePolicy{Schedule: ps, MaxUnusedPercent: unused}
	r.CheckPolicy = &v1.CheckPolicy{Schedule: cs}
	if m.CheckReadDataPercent > 0 {
		r.CheckPolicy.Mode = &v1.CheckPolicy_ReadDataSubsetPercent{ReadDataSubsetPercent: m.CheckReadDataPercent}
	} else {
		r.CheckPolicy.Mode = &v1.CheckPolicy_StructureOnly{StructureOnly: true}
	}
	return nil
}

func envList(env map[string]string) []string {
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// saveRepo validates the repository against restic (initializing it if it
// does not exist yet) through the same AddRepo handler as the web UI.
func (s *Server) saveRepo(ctx context.Context, r *v1.Repo) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err := s.d.Backrest.AddRepo(ctx, connect.NewRequest(&v1.AddRepoRequest{Repo: r}))
	if err != nil {
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "host key verification failed") {
			return fmt.Errorf("the SFTP host key is not trusted yet: use scan_sftp_host_keys and trust_sftp_host_key first (%s)", msg)
		}
		return err
	}
	return nil
}

// ---- plan tools ----

type CreatePlanIn struct {
	ID              string     `json:"id" jsonschema:"short unique ID, e.g. easypanel-data"`
	RepoID          string     `json:"repo_id"`
	Paths           []string   `json:"paths" jsonschema:"absolute paths inside the Relyper Backup container"`
	Excludes        []string   `json:"excludes,omitempty" jsonschema:"restic exclude patterns"`
	IExcludes       []string   `json:"iexcludes,omitempty" jsonschema:"case-insensitive exclude patterns"`
	Schedule        Schedule   `json:"schedule"`
	Retention       *Retention `json:"retention,omitempty" jsonschema:"which snapshots to keep; strongly recommended (e.g. daily 7, weekly 4, monthly 12)"`
	BackupFlags     []string   `json:"backup_flags,omitempty"`
	SkipIfUnchanged bool       `json:"skip_if_unchanged,omitempty" jsonschema:"do not create a snapshot if nothing changed"`
}

type UpdatePlanIn struct {
	PlanID          string    `json:"plan_id"`
	RepoID          *string   `json:"repo_id,omitempty" jsonschema:"move the plan to another repository (old snapshots stay in the old one)"`
	Paths           *[]string `json:"paths,omitempty" jsonschema:"replaces all paths"`
	Excludes        *[]string `json:"excludes,omitempty" jsonschema:"replaces all excludes"`
	IExcludes       *[]string `json:"iexcludes,omitempty"`
	Schedule        *Schedule `json:"schedule,omitempty"`
	BackupFlags     *[]string `json:"backup_flags,omitempty"`
	SkipIfUnchanged *bool     `json:"skip_if_unchanged,omitempty"`
}

type EnablePlanIn struct {
	PlanID   string    `json:"plan_id"`
	Schedule *Schedule `json:"schedule,omitempty" jsonschema:"schedule to use; omit to restore the schedule the plan had before disable_backup_plan"`
}

type PlanChangeOut struct {
	Plan     PlanView `json:"plan"`
	Warnings []string `json:"warnings,omitempty"`
	Message  string   `json:"message"`
}

func registerConfigTools(r *registrar) {
	s := r.s
	repoSpec := func(name, title, desc string, idem bool) toolSpec {
		return toolSpec{Name: name, Title: title, Description: desc, Scope: mcpauth.ScopeReposManage, Idempotent: idem}
	}
	planSpec := func(name, title, desc string, idem bool) toolSpec {
		return toolSpec{Name: name, Title: title, Description: desc, Scope: mcpauth.ScopePlansManage, Idempotent: idem}
	}

	addTool(r, repoSpec("create_repository", "Create repository", "Adds a restic repository. If the location holds no repository yet it is initialized with the password; if it holds one, the password must be the existing one. For SFTP, first create a key (create_sftp_key), install the public key on the server, trust the host key (scan_sftp_host_keys, trust_sftp_host_key) and run test_sftp_connection. The password is stored on the server and never returned.", false),
		func(ctx context.Context, c caller, in CreateRepoIn) (*RepoChangeOut, error) {
			if err := validateID("repository", in.ID); err != nil {
				return nil, err
			}
			if err := validateURI(in.URI); err != nil {
				return nil, err
			}
			if len(in.Password) < 8 {
				return nil, errors.New("password must have at least 8 characters")
			}
			if err := validateEnv(in.Env); err != nil {
				return nil, err
			}
			cfg, err := s.cfg()
			if err != nil {
				return nil, err
			}
			if _, err := findRepo(cfg, in.ID); err == nil {
				return nil, fmt.Errorf("repository %q already exists; use update_repository", in.ID)
			}
			rp := &v1.Repo{Id: in.ID, Uri: in.URI, Password: in.Password, Env: envList(in.Env), AutoUnlock: in.AutoUnlock}
			var warnings []string
			if repoType(in.URI) == "sftp" {
				sftp := in.SFTP
				if sftp == nil {
					sftp = &SFTPSettings{}
					warnings = append(warnings, "no sftp.identity_file given: ssh uses its default key")
				}
				f, err := s.sftpFlag(sftp)
				if err != nil {
					return nil, err
				}
				rp.Flags = append(rp.Flags, f)
			} else if in.SFTP != nil {
				return nil, errors.New("sftp settings are only valid for sftp: repositories")
			}
			opts, err := optionFlags(in.Options)
			if err != nil {
				return nil, err
			}
			rp.Flags = append(rp.Flags, opts...)
			m := in.Maintenance
			if m == nil {
				m = defaultMaintenance()
			}
			if err := applyMaintenance(rp, m); err != nil {
				return nil, err
			}
			if err := s.saveRepo(ctx, rp); err != nil {
				return nil, err
			}
			cfg, _ = s.cfg()
			saved, err := findRepo(cfg, in.ID)
			if err != nil {
				return nil, err
			}
			return &RepoChangeOut{Repository: repoView(cfg, saved), Warnings: warnings, Message: "Repository created and verified with restic. Remind the user to keep the repository password in a password manager."}, nil
		})

	addTool(r, repoSpec("update_repository", "Update repository", "Changes a repository's settings. Changing location, password, credentials, SFTP settings or options re-verifies the repository with restic. Hooks and flags set in the web UI are kept. Repository-wide retention is changed with modify_retention_policy.", true),
		func(ctx context.Context, c caller, in UpdateRepoIn) (*RepoChangeOut, error) {
			cfg, err := s.cfg()
			if err != nil {
				return nil, err
			}
			old, err := findRepo(cfg, in.RepoID)
			if err != nil {
				return nil, err
			}
			if old.OriginInstanceId != "" {
				return nil, errors.New("this repository is managed by another instance and cannot be changed here")
			}
			rp := proto.Clone(old).(*v1.Repo)
			reverify := false
			if in.URI != nil {
				if err := validateURI(*in.URI); err != nil {
					return nil, err
				}
				rp.Uri, reverify = *in.URI, true
			}
			if in.Password != nil {
				if len(*in.Password) < 8 {
					return nil, errors.New("password must have at least 8 characters")
				}
				rp.Password, reverify = *in.Password, true
			}
			if len(in.EnvSet) > 0 || len(in.EnvRemove) > 0 {
				if err := validateEnv(in.EnvSet); err != nil {
					return nil, err
				}
				env := map[string]string{}
				for _, e := range rp.Env {
					k, v, _ := strings.Cut(e, "=")
					env[k] = v
				}
				for _, k := range in.EnvRemove {
					delete(env, k)
				}
				for k, v := range in.EnvSet {
					env[k] = v
				}
				rp.Env, reverify = envList(env), true
			}
			if in.SFTP != nil || in.Options != nil || (in.URI != nil && repoType(*in.URI) == "sftp") {
				var keep []string
				for _, f := range rp.Flags {
					managed := isManagedFlag(f)
					if strings.HasPrefix(f, "--option=sftp.args=") && in.SFTP == nil && repoType(rp.Uri) == "sftp" {
						managed = false // keep existing ssh settings
					}
					if !strings.HasPrefix(f, "--option=sftp.args=") && in.Options == nil && isManagedFlag(f) {
						managed = false // keep existing options
					}
					if !managed {
						keep = append(keep, f)
					}
				}
				rp.Flags = keep
				if in.SFTP != nil {
					if repoType(rp.Uri) != "sftp" {
						return nil, errors.New("sftp settings are only valid for sftp: repositories")
					}
					f, err := s.sftpFlag(in.SFTP)
					if err != nil {
						return nil, err
					}
					rp.Flags = append(rp.Flags, f)
				}
				if in.Options != nil {
					opts, err := optionFlags(in.Options)
					if err != nil {
						return nil, err
					}
					rp.Flags = append(rp.Flags, opts...)
				}
				reverify = true
			}
			if in.Maintenance != nil {
				if err := applyMaintenance(rp, in.Maintenance); err != nil {
					return nil, err
				}
			}
			if in.AutoUnlock != nil {
				rp.AutoUnlock = *in.AutoUnlock
			}
			if reverify {
				if err := s.saveRepo(ctx, rp); err != nil {
					return nil, err
				}
			} else if _, err := s.transform(func(cfg *v1.Config) error {
				idx := slices.IndexFunc(cfg.Repos, func(x *v1.Repo) bool { return x.Id == rp.Id })
				if idx < 0 {
					return fmt.Errorf("repository %q disappeared", rp.Id)
				}
				cfg.Repos[idx] = rp
				return nil
			}); err != nil {
				return nil, err
			}
			cfg, _ = s.cfg()
			saved, err := findRepo(cfg, in.RepoID)
			if err != nil {
				return nil, err
			}
			msg := "Repository updated."
			if reverify {
				msg = "Repository updated and verified with restic."
			}
			return &RepoChangeOut{Repository: repoView(cfg, saved), Message: msg}, nil
		})

	addTool(r, toolSpec{Name: "test_repository_connection", Title: "Test repository", Scope: mcpauth.ScopeReposManage, ReadOnly: true, Idempotent: true,
		Description: "Checks with restic whether a configured repository can be reached and opened with its stored credentials. Changes nothing."},
		func(ctx context.Context, c caller, in RepoIDIn) (*TestRepoOut, error) {
			cfg, err := s.cfg()
			if err != nil {
				return nil, err
			}
			rp, err := findRepo(cfg, in.RepoID)
			if err != nil {
				return nil, err
			}
			resp, err := s.d.Backrest.CheckRepoExists(ctx, connect.NewRequest(&v1.CheckRepoExistsRequest{Repo: proto.Clone(rp).(*v1.Repo)}))
			if err != nil {
				return &TestRepoOut{OK: false, Message: err.Error()}, nil
			}
			out := &TestRepoOut{OK: resp.Msg.Exists, Exists: resp.Msg.Exists, HostKeyUntrusted: resp.Msg.HostKeyUntrusted}
			switch {
			case resp.Msg.HostKeyUntrusted:
				out.Message = "The SFTP host key is not trusted; use scan_sftp_host_keys and trust_sftp_host_key."
			case !resp.Msg.Exists:
				out.Message = "The location is reachable but holds no restic repository."
			default:
				out.Message = "Repository reachable and opened successfully."
			}
			return out, nil
		})

	addTool(r, repoSpec("create_sftp_key", "Create SFTP key", "Generates an ed25519 key pair for an SFTP host inside the container (in the Relyper SSH directory) and returns the public key and the private key path. The user installs the public key on the SFTP server (authorized_keys). Never returns the private key.", false),
		func(ctx context.Context, c caller, in SFTPHostIn) (*SFTPKeyOut, error) {
			if err := sshdiag.ValidateHost(in.Host); err != nil {
				return nil, err
			}
			port := ""
			if in.Port != 0 {
				port = strconv.Itoa(in.Port)
			}
			resp, err := s.d.Backrest.SetupSftp(ctx, connect.NewRequest(&v1.SetupSftpRequest{Host: in.Host, Port: port}))
			if err != nil {
				return nil, err
			}
			return &SFTPKeyOut{PublicKey: resp.Msg.PublicKey, KeyPath: resp.Msg.KeyPath, KnownHostsPath: resp.Msg.KnownHostsPath,
				Next: "Install the public key on the server (e.g. append it to ~/.ssh/authorized_keys of the SFTP user, or upload it in the provider's console). Then scan_sftp_host_keys."}, nil
		})

	addTool(r, toolSpec{Name: "scan_sftp_host_keys", Title: "Scan SFTP host keys", Scope: mcpauth.ScopeReposManage, ReadOnly: true, Idempotent: true,
		Description: "Fetches the SSH host keys a server presents and shows for each its SHA-256 fingerprint and whether it is already trusted. Changes nothing. The user must compare the fingerprint with the one published by the provider before trusting it."},
		func(ctx context.Context, c caller, in SFTPHostIn) (*HostKeysOut, error) {
			keys, d := sshdiag.ScanHostKeys(ctx, in.Host, in.Port, s.knownHostsPath(), 15*time.Second)
			if d != nil {
				return nil, fmt.Errorf("%s: %s", d.Category, d.Message)
			}
			out := &HostKeysOut{Host: in.Host, Port: in.Port, Keys: []HostKeyView{}}
			for _, k := range keys {
				out.Keys = append(out.Keys, HostKeyView{Type: k.Type, FingerprintSHA256: k.Fingerprint, Status: k.Status.String()})
			}
			return out, nil
		})

	addTool(r, toolSpec{Name: "test_sftp_connection", Title: "Test SFTP connection", Scope: mcpauth.ScopeReposManage, ReadOnly: true, Idempotent: true,
		Description: "Step-by-step SFTP test (DNS, TCP, host key, authentication with the identity file, SFTP subsystem, remote path, optional write test) with a stable error category for each failure. Never changes known_hosts. The write test creates and deletes one small file."},
		func(ctx context.Context, c caller, in SFTPTestIn) (*SFTPTestOut, error) {
			if in.IdentityFile != "" && !safePathRe.MatchString(in.IdentityFile) {
				return nil, errors.New("identity_file must be an absolute path")
			}
			res := sshdiag.TestConnection(ctx, sshdiag.Options{
				Host: in.Host, Port: in.Port, User: in.Username, IdentityFile: in.IdentityFile, RemotePath: in.RemotePath,
				KnownHostsPath: s.knownHostsPath(), WriteTest: in.WriteTest, Timeout: 20 * time.Second,
			})
			out := &SFTPTestOut{OK: res.OK, ResolvedPath: res.ResolvedPath, FreeBytes: res.FreeBytes, TotalBytes: res.TotalBytes, Repository: repoStateName(res.Repository), Steps: []SFTPStep{}}
			if res.Error != nil {
				out.ErrorCategory, out.Error = res.Error.Category, res.Error.Message
			}
			for _, st := range res.Steps {
				out.Steps = append(out.Steps, SFTPStep{Name: st.Name, Status: stepStatusName(st.Status), Message: st.Message, Hint: st.Hint, Category: st.Category})
			}
			return out, nil
		})

	addTool(r, planSpec("create_backup_plan", "Create backup plan", "Creates a backup plan: which paths go to which repository, when, and how long snapshots are kept. Hooks cannot be created through MCP.", false),
		func(ctx context.Context, c caller, in CreatePlanIn) (*PlanChangeOut, error) {
			if err := validateID("plan", in.ID); err != nil {
				return nil, err
			}
			if len(in.Paths) == 0 {
				return nil, errors.New("at least one path is required")
			}
			warnings, err := validatePaths(in.Paths)
			if err != nil {
				return nil, err
			}
			if err := errors.Join(validatePatterns("exclude", in.Excludes), validatePatterns("iexclude", in.IExcludes), validateBackupFlags(in.BackupFlags)); err != nil {
				return nil, err
			}
			sched, err := scheduleToProto(&in.Schedule)
			if err != nil {
				return nil, err
			}
			ret, err := retentionToProto(in.Retention)
			if err != nil {
				return nil, err
			}
			if ret == nil {
				warnings = append(warnings, "no retention policy: snapshots are never deleted automatically; set one with modify_retention_policy")
			}
			p := &v1.Plan{Id: in.ID, Repo: in.RepoID, Paths: in.Paths, Excludes: in.Excludes, Iexcludes: in.IExcludes, Schedule: sched, Retention: ret, BackupFlags: in.BackupFlags, SkipIfUnchanged: in.SkipIfUnchanged}
			cfg, err := s.transform(func(cfg *v1.Config) error {
				if _, err := findPlan(cfg, in.ID); err == nil {
					return fmt.Errorf("backup plan %q already exists; use update_backup_plan", in.ID)
				}
				rp, err := findRepo(cfg, in.RepoID)
				if err != nil {
					return err
				}
				if rp.OriginInstanceId != "" {
					return errors.New("the repository is managed by another instance")
				}
				cfg.Plans = append(cfg.Plans, p)
				return nil
			})
			if err != nil {
				return nil, err
			}
			saved, _ := findPlan(cfg, in.ID)
			return &PlanChangeOut{Plan: planView(saved), Warnings: warnings, Message: "Backup plan created. Start a first backup with run_backup to verify it."}, nil
		})

	addTool(r, planSpec("update_backup_plan", "Update backup plan", "Changes a backup plan. Only the given fields change; lists replace the old list. Hooks are kept. Retention is changed with modify_retention_policy (needs approval).", true),
		func(ctx context.Context, c caller, in UpdatePlanIn) (*PlanChangeOut, error) {
			var warnings []string
			if in.Paths != nil {
				if len(*in.Paths) == 0 {
					return nil, errors.New("at least one path is required")
				}
				w, err := validatePaths(*in.Paths)
				if err != nil {
					return nil, err
				}
				warnings = w
			}
			var errs []error
			if in.Excludes != nil {
				errs = append(errs, validatePatterns("exclude", *in.Excludes))
			}
			if in.IExcludes != nil {
				errs = append(errs, validatePatterns("iexclude", *in.IExcludes))
			}
			if in.BackupFlags != nil {
				errs = append(errs, validateBackupFlags(*in.BackupFlags))
			}
			if err := errors.Join(errs...); err != nil {
				return nil, err
			}
			sched, err := scheduleToProto(in.Schedule)
			if err != nil {
				return nil, err
			}
			cfg, err := s.transform(func(cfg *v1.Config) error {
				p, err := findPlan(cfg, in.PlanID)
				if err != nil {
					return err
				}
				if in.RepoID != nil {
					rp, err := findRepo(cfg, *in.RepoID)
					if err != nil {
						return err
					}
					if rp.OriginInstanceId != "" {
						return errors.New("the repository is managed by another instance")
					}
					p.Repo = *in.RepoID
				}
				if in.Paths != nil {
					p.Paths = *in.Paths
				}
				if in.Excludes != nil {
					p.Excludes = *in.Excludes
				}
				if in.IExcludes != nil {
					p.Iexcludes = *in.IExcludes
				}
				if sched != nil {
					p.Schedule = sched
				}
				if in.BackupFlags != nil {
					p.BackupFlags = *in.BackupFlags
				}
				if in.SkipIfUnchanged != nil {
					p.SkipIfUnchanged = *in.SkipIfUnchanged
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			saved, _ := findPlan(cfg, in.PlanID)
			return &PlanChangeOut{Plan: planView(saved), Warnings: warnings, Message: "Backup plan updated."}, nil
		})

	addTool(r, planSpec("disable_backup_plan", "Disable backup plan", "Stops scheduled backups of a plan. The schedule is remembered for enable_backup_plan. Snapshots are not touched.", true),
		func(ctx context.Context, c caller, in PlanIDIn) (*PlanChangeOut, error) {
			var prev *v1.Schedule
			cfg, err := s.transform(func(cfg *v1.Config) error {
				p, err := findPlan(cfg, in.PlanID)
				if err != nil {
					return err
				}
				if scheduleEnabled(p.Schedule) {
					prev = proto.Clone(p.Schedule).(*v1.Schedule)
				}
				clock := p.GetSchedule().GetClock()
				p.Schedule = &v1.Schedule{Schedule: &v1.Schedule_Disabled{Disabled: true}, Clock: clock}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if prev != nil {
				s.rememberSchedule(in.PlanID, scheduleFromProto(prev))
			}
			saved, _ := findPlan(cfg, in.PlanID)
			return &PlanChangeOut{Plan: planView(saved), Message: "Scheduled backups disabled."}, nil
		})

	addTool(r, planSpec("enable_backup_plan", "Enable backup plan", "Turns scheduled backups of a plan back on, with the given schedule or the one it had before it was disabled.", true),
		func(ctx context.Context, c caller, in EnablePlanIn) (*PlanChangeOut, error) {
			sch := in.Schedule
			if sch == nil {
				sch = s.rememberedSchedule(in.PlanID)
			}
			if sch == nil {
				return nil, errors.New("no previous schedule is known for this plan; pass a schedule")
			}
			if sch.Kind == "disabled" {
				return nil, errors.New("schedule kind must not be disabled")
			}
			sched, err := scheduleToProto(sch)
			if err != nil {
				return nil, err
			}
			cfg, err := s.transform(func(cfg *v1.Config) error {
				p, err := findPlan(cfg, in.PlanID)
				if err != nil {
					return err
				}
				p.Schedule = sched
				return nil
			})
			if err != nil {
				return nil, err
			}
			s.rememberSchedule(in.PlanID, nil)
			saved, _ := findPlan(cfg, in.PlanID)
			return &PlanChangeOut{Plan: planView(saved), Message: "Scheduled backups enabled."}, nil
		})
}

type TestRepoOut struct {
	OK               bool   `json:"ok"`
	Exists           bool   `json:"exists"`
	HostKeyUntrusted bool   `json:"host_key_untrusted,omitempty"`
	Message          string `json:"message" jsonschema:"may contain untrusted text from restic"`
}

type SFTPHostIn struct {
	Host string `json:"host" jsonschema:"SFTP server host name"`
	Port int    `json:"port,omitempty" jsonschema:"SSH port (default 22)"`
}

type SFTPKeyOut struct {
	PublicKey      string `json:"public_key"`
	KeyPath        string `json:"key_path" jsonschema:"use as sftp.identity_file in create_repository"`
	KnownHostsPath string `json:"known_hosts_path"`
	Next           string `json:"next"`
}

type HostKeyView struct {
	Type              string `json:"type"`
	FingerprintSHA256 string `json:"fingerprint_sha256"`
	Status            string `json:"status" jsonschema:"trusted, unknown, mismatch or revoked"`
}

type HostKeysOut struct {
	Host string        `json:"host"`
	Port int           `json:"port,omitempty"`
	Keys []HostKeyView `json:"keys"`
}

type SFTPTestIn struct {
	Host         string `json:"host"`
	Port         int    `json:"port,omitempty"`
	Username     string `json:"username"`
	IdentityFile string `json:"identity_file,omitempty" jsonschema:"absolute path of the private key inside the container"`
	RemotePath   string `json:"remote_path,omitempty" jsonschema:"repository directory on the server"`
	WriteTest    bool   `json:"write_test,omitempty"`
}

type SFTPStep struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Category string `json:"category,omitempty"`
}

type SFTPTestOut struct {
	OK            bool       `json:"ok"`
	ErrorCategory string     `json:"error_category,omitempty"`
	Error         string     `json:"error,omitempty"`
	Repository    string     `json:"repository_state,omitempty"`
	ResolvedPath  string     `json:"resolved_path,omitempty"`
	FreeBytes     int64      `json:"free_bytes,omitempty"`
	TotalBytes    int64      `json:"total_bytes,omitempty"`
	Steps         []SFTPStep `json:"steps"`
}

func stepStatusName(s sshdiag.StepStatus) string {
	switch s {
	case sshdiag.StepOK:
		return "ok"
	case sshdiag.StepWarning:
		return "warning"
	case sshdiag.StepFailed:
		return "failed"
	case sshdiag.StepSkipped:
		return "skipped"
	}
	return "unknown"
}

func repoStateName(s sshdiag.RepoState) string {
	switch s {
	case sshdiag.RepoExisting:
		return "existing_repository"
	case sshdiag.RepoEmpty:
		return "empty_directory"
	case sshdiag.RepoMissing:
		return "directory_missing"
	case sshdiag.RepoNotRepository:
		return "not_a_repository"
	}
	return "unknown"
}
