// Package docker is Relyper-owned: a small client for the Docker Engine API.
//
// It covers what discovery, database dumps and restore verification need and
// nothing more. Discovery only reads (containers, volumes, Swarm services and
// tasks). Dumps run an exec inside an existing database container. Restore
// verification creates, starts and removes its own throwaway containers,
// which always carry the label relyper.verify=true. The client never stops,
// restarts or changes other containers.
//
// The endpoint comes from RELYPER_GUARD_URL, then RELYPER_DOCKER_HOST
// (deprecated), then DOCKER_HOST, then
// unix:///var/run/docker.sock. The recommended endpoint is the Relyper
// Docker guard (cmd/relyper-docker-guard), which allows exactly the calls
// made here; RELYPER_GUARD_TOKEN (the same variable the guard uses;
// deprecated: RELYPER_DOCKER_TOKEN) is sent to it as a bearer token.
package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client talks to one Docker daemon.
type Client struct {
	base string // http://docker or http://host:port
	http *http.Client
	// token is sent as a bearer token (Relyper Docker guard). Never logged.
	token string
	// Endpoint is the configured address, for display.
	Endpoint string
}

// tokenFromEnv returns RELYPER_GUARD_TOKEN, or the deprecated
// RELYPER_DOCKER_TOKEN.
func tokenFromEnv() string {
	for _, name := range []string{"RELYPER_GUARD_TOKEN", "RELYPER_DOCKER_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// ErrUnavailable is returned when no daemon is configured or reachable.
var ErrUnavailable = errors.New("docker is not available")

// EndpointFromEnv returns the configured Docker endpoint.
func EndpointFromEnv() string {
	for _, name := range []string{"RELYPER_GUARD_URL", "RELYPER_DOCKER_HOST"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(os.Getenv("DOCKER_HOST")); v != "" {
		return v
	}
	return "unix:///var/run/docker.sock"
}

// New creates a client for endpoint (unix://path, tcp://host:port or
// http://host:port). It does not connect yet.
func New(endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid docker endpoint: %w", err)
	}
	c := &Client{Endpoint: endpoint, token: tokenFromEnv()}
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	switch u.Scheme {
	case "unix":
		path := u.Path
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
		c.base = "http://docker"
	case "tcp", "http":
		c.base = "http://" + u.Host
	default:
		return nil, fmt.Errorf("unsupported docker endpoint scheme %q (use unix:// or tcp://)", u.Scheme)
	}
	c.http = &http.Client{Transport: tr}
	return c, nil
}

// APIError is an error answer of the daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %d %s", e.Status, e.Message) }

// IsNotFound reports a 404 from the daemon.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	resp, err := c.raw(ctx, method, path, query, body, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Do sends one request and decodes the JSON answer into out (may be nil).
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	return c.do(ctx, method, path, query, body, out)
}

func (c *Client) raw(ctx context.Context, method, path string, query url.Values, body any, contentType string) (*http.Response, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case io.Reader:
		rd = b
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(buf)
		contentType = "application/json"
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var m struct {
			Message string `json:"message"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(b, &m) != nil || m.Message == "" {
			m.Message = strings.TrimSpace(string(b))
		}
		return nil, &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	return resp, nil
}

// Version is the daemon version.
type Version struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	OS         string `json:"Os"`
}

// Ping checks the connection and returns the daemon version.
func (c *Client) Ping(ctx context.Context) (*Version, error) {
	var v Version
	if err := c.do(ctx, http.MethodGet, "/version", nil, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// RoleLabel with the value RoleGuard marks the Relyper Docker guard's
// container (set in its image); discovery does not list it as an app.
const (
	RoleLabel = "relyper.role"
	RoleGuard = "docker-guard"
)

// GuardPath is answered by the Relyper Docker guard and by nothing else.
const GuardPath = "/relyper/guard"

// GuardInfo describes the Relyper Docker guard in front of the daemon.
type GuardInfo struct {
	Guard        string   `json:"guard"` // "relyper-docker-guard"
	Features     []string `json:"features"`
	RequireLabel bool     `json:"require_label"`
}

// Guard returns the guard's description, or an error when the endpoint is
// not a Relyper Docker guard (raw socket or another proxy).
func (c *Client) Guard(ctx context.Context) (*GuardInfo, error) {
	var g GuardInfo
	if err := c.do(ctx, http.MethodGet, GuardPath, nil, nil, &g); err != nil {
		return nil, err
	}
	if g.Guard != "relyper-docker-guard" {
		return nil, errors.New("not a Relyper Docker guard")
	}
	return &g, nil
}

// Info is the part of /info that matters here.
type Info struct {
	Name          string `json:"Name"`
	DockerRootDir string `json:"DockerRootDir"`
	Swarm         struct {
		LocalNodeState   string `json:"LocalNodeState"`
		ControlAvailable bool   `json:"ControlAvailable"`
		NodeID           string `json:"NodeID"`
	} `json:"Swarm"`
}

// Info returns daemon information.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var i Info
	if err := c.do(ctx, http.MethodGet, "/info", nil, nil, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

// Mount is a mount of a container.
type Mount struct {
	Type        string `json:"Type"` // bind, volume, tmpfs
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Driver      string `json:"Driver"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

// ContainerSummary is an entry of /containers/json.
type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Mounts  []Mount           `json:"Mounts"`
	Created int64             `json:"Created"`
	Ports   []Port            `json:"Ports"`
	// NetworkSettings.Networks is keyed by network name.
	NetworkSettings struct {
		Networks map[string]struct{} `json:"Networks"`
	} `json:"NetworkSettings"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// Port is a port of a container; PublicPort is 0 when it is not published.
type Port struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// Name is the container name without the leading slash.
func (s *ContainerSummary) Name() string {
	if len(s.Names) == 0 {
		return s.ID[:12]
	}
	return strings.TrimPrefix(s.Names[0], "/")
}

// ListContainers lists all containers, running or not.
func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}}, nil, &out)
	return out, err
}

// ContainerDetails is the part of /containers/{id}/json that matters here.
type ContainerDetails struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"` // image ID
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
		Health  *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
		User   string            `json:"User"`
	} `json:"Config"`
	Mounts     []Mount `json:"Mounts"`
	HostConfig struct {
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Memory   int64 `json:"Memory"`
		NanoCpus int64 `json:"NanoCpus"`
	} `json:"HostConfig"`
}

// Env returns the value of an environment variable of the container.
func (d *ContainerDetails) Env(name string) (string, bool) {
	for _, e := range d.Config.Env {
		if k, v, ok := strings.Cut(e, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// InspectContainer returns details of a container.
func (c *Client) InspectContainer(ctx context.Context, id string) (*ContainerDetails, error) {
	var d ContainerDetails
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &d); err != nil {
		return nil, err
	}
	d.Name = strings.TrimPrefix(d.Name, "/")
	return &d, nil
}

// Volume is a named volume.
type Volume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	Labels     map[string]string `json:"Labels"`
	Scope      string            `json:"Scope"`
}

// ListVolumes lists named volumes.
func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	var out struct {
		Volumes []Volume `json:"Volumes"`
	}
	err := c.do(ctx, http.MethodGet, "/volumes", nil, nil, &out)
	return out.Volumes, err
}

// Service is a Swarm service.
type Service struct {
	ID   string `json:"ID"`
	Spec struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
		// Mode tells replicated (with the wanted number of replicas) from
		// global services.
		Mode struct {
			Replicated *struct {
				Replicas *uint64 `json:"Replicas"`
			} `json:"Replicated"`
			Global *struct{} `json:"Global"`
		} `json:"Mode"`
		TaskTemplate struct {
			ContainerSpec struct {
				Image  string            `json:"Image"`
				Labels map[string]string `json:"Labels"`
				Mounts []struct {
					Type   string `json:"Type"`
					Source string `json:"Source"`
					Target string `json:"Target"`
				} `json:"Mounts"`
			} `json:"ContainerSpec"`
			RestartPolicy struct {
				Condition string `json:"Condition"`
			} `json:"RestartPolicy"`
			Resources struct {
				Limits struct {
					NanoCPUs    int64 `json:"NanoCPUs"`
					MemoryBytes int64 `json:"MemoryBytes"`
				} `json:"Limits"`
			} `json:"Resources"`
		} `json:"TaskTemplate"`
	} `json:"Spec"`
}

// ListServices lists Swarm services. It returns nil, nil when the daemon is
// not a Swarm manager.
func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	var out []Service
	err := c.do(ctx, http.MethodGet, "/services", nil, nil, &out)
	var e *APIError
	if errors.As(err, &e) && (e.Status == http.StatusServiceUnavailable || e.Status == http.StatusNotAcceptable) {
		return nil, nil
	}
	return out, err
}

// Task is a Swarm task (one replica of a service on one node).
type Task struct {
	ID           string `json:"ID"`
	ServiceID    string `json:"ServiceID"`
	NodeID       string `json:"NodeID"`
	DesiredState string `json:"DesiredState"`
	Status       struct {
		State string `json:"State"`
	} `json:"Status"`
}

// ListTasks lists the Swarm tasks that should run (desired state running).
func (c *Client) ListTasks(ctx context.Context) ([]Task, error) {
	var out []Task
	err := c.do(ctx, http.MethodGet, "/tasks", url.Values{"filters": {`{"desired-state":["running"]}`}}, nil, &out)
	return out, err
}

// ExecResult is the outcome of an exec.
type ExecResult struct {
	ExitCode int
}

// Exec runs cmd in a running container and streams its stdout and stderr.
// env adds environment variables for this command only; they are sent to
// the daemon and never logged here.
func (c *Client) Exec(ctx context.Context, container string, cmd, env []string, user string, stdout, stderr io.Writer) (*ExecResult, error) {
	var created struct {
		ID string `json:"Id"`
	}
	body := map[string]any{"AttachStdout": true, "AttachStderr": true, "Cmd": cmd, "Env": env}
	if user != "" {
		body["User"] = user
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(container)+"/exec", nil, body, &created); err != nil {
		return nil, err
	}
	resp, err := c.raw(ctx, http.MethodPost, "/exec/"+created.ID+"/start", nil, map[string]any{"Detach": false, "Tty": false}, "")
	if err != nil {
		return nil, err
	}
	err = demux(resp.Body, stdout, stderr)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var st struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	// The exit code can lag a few milliseconds behind the end of the stream.
	for i := 0; i < 50; i++ {
		if err := c.do(ctx, http.MethodGet, "/exec/"+created.ID+"/json", nil, nil, &st); err != nil {
			return nil, err
		}
		if !st.Running {
			return &ExecResult{ExitCode: st.ExitCode}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, errors.New("exec did not finish")
}

// demux splits Docker's multiplexed stream (8-byte frame headers).
func demux(r io.Reader, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return errors.New("docker stream ended in the middle of a frame")
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		w := stdout
		if hdr[0] == 2 {
			w = stderr
		}
		if _, err := io.CopyN(w, r, n); err != nil {
			return fmt.Errorf("docker stream: %w", err)
		}
	}
}

// ContainerConfig creates a throwaway container.
type ContainerConfig struct {
	Image  string            `json:"Image"`
	Env    []string          `json:"Env,omitempty"`
	Cmd    []string          `json:"Cmd,omitempty"`
	Labels map[string]string `json:"Labels"`
	// HostConfig keeps the container isolated: no network, no mounts.
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
		AutoRemove  bool   `json:"AutoRemove"`
		Memory      int64  `json:"Memory,omitempty"`
	} `json:"HostConfig"`
}

// VerifyLabel marks containers created by Relyper Backup for restore tests.
const VerifyLabel = "relyper.verify"

// CreateVerifyContainer creates an isolated container (no network) labeled
// relyper.verify=true and returns its ID.
func (c *Client) CreateVerifyContainer(ctx context.Context, name string, cfg ContainerConfig) (string, error) {
	if cfg.Labels == nil {
		cfg.Labels = map[string]string{}
	}
	cfg.Labels[VerifyLabel] = "true"
	cfg.HostConfig.NetworkMode = "none"
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, cfg, &out)
	return out.ID, err
}

// StartContainer starts a container.
func (c *Client) StartContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
}

// RemoveVerifyContainer force-removes a container, but only one created for
// restore verification.
func (c *Client) RemoveVerifyContainer(ctx context.Context, id string) error {
	d, err := c.InspectContainer(ctx, id)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return err
	}
	if d.Config.Labels[VerifyLabel] != "true" {
		return fmt.Errorf("refusing to remove %s: not a verification container", d.Name)
	}
	return c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
}

// PutArchive extracts a tar stream into dir inside the container.
func (c *Client) PutArchive(ctx context.Context, id, dir string, tarStream io.Reader) error {
	resp, err := c.raw(ctx, http.MethodPut, "/containers/"+url.PathEscape(id)+"/archive", url.Values{"path": {dir}}, tarStream, "application/x-tar")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ListVerifyContainers lists leftover verification containers.
func (c *Client) ListVerifyContainers(ctx context.Context) ([]ContainerSummary, error) {
	f, _ := json.Marshal(map[string][]string{"label": {VerifyLabel + "=true"}})
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(f)}}, nil, &out)
	return out, err
}

// StopContainer stops a container, waiting up to timeout seconds before
// the daemon kills it.
func (c *Client) StopContainer(ctx context.Context, id string, timeout int) error {
	err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", url.Values{"t": {strconv.Itoa(timeout)}}, nil, nil)
	var e *APIError
	if errors.As(err, &e) && e.Status == http.StatusNotModified {
		return nil // already stopped
	}
	return err
}

// RestartContainer restarts a container.
func (c *Client) RestartContainer(ctx context.Context, id string, timeout int) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/restart", url.Values{"t": {strconv.Itoa(timeout)}}, nil, nil)
}
