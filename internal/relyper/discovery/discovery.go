// Package discovery is Relyper-owned: it builds an inventory of the
// applications running on a Docker host (Compose projects, Swarm stacks,
// Easypanel projects, single containers), their data (volumes and bind
// mounts) and their databases. It only reads from the Docker Engine API.
package discovery

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

// Kinds of applications.
const (
	KindCompose    = "compose"
	KindStack      = "swarm_stack"
	KindEasypanel  = "easypanel"
	KindStandalone = "container"
)

// Database engines with dump support.
const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
	EngineMariaDB  = "mariadb"
	EngineMongo    = "mongodb"
	EngineRedis    = "redis"
)

// Inventory is the result of a discovery run.
type Inventory struct {
	GeneratedAt time.Time `json:"generated_at"`
	Docker      Host      `json:"docker"`
	Apps        []App     `json:"apps"`
}

// Host describes the Docker daemon.
type Host struct {
	Available bool   `json:"available"`
	Endpoint  string `json:"endpoint"`
	Version   string `json:"version,omitempty"`
	Name      string `json:"name,omitempty"`
	Swarm     bool   `json:"swarm"`
	Easypanel bool   `json:"easypanel"`
	Error     string `json:"error,omitempty"`
	// Access is "guard" when the Relyper Docker guard sits in front of the
	// daemon and "unrestricted" otherwise (raw socket or a generic proxy,
	// which give root on the host to whoever controls Relyper Backup).
	Access        string   `json:"access,omitempty"`
	GuardFeatures []string `json:"guard_features,omitempty"`
	// Hint explains a failed connection in plain words.
	Hint string `json:"hint,omitempty"`
}

// App is a group of services that belong together.
type App struct {
	ID       string    `json:"id"` // e.g. compose:shop, swarm_stack:crm, easypanel:blog, container:nginx
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Services []Service `json:"services"`
	// Deployment lists the files the app was deployed from (compose files).
	Deployment []DeploymentFile `json:"deployment,omitempty"`
	// Notes are facts worth knowing (assumptions, data on other nodes).
	Notes []string `json:"notes,omitempty"`
}

// Service is one container (or Swarm service) of an app.
type Service struct {
	Name         string    `json:"name"`
	Container    string    `json:"container,omitempty"` // current container name, empty if none on this node
	ContainerID  string    `json:"container_id,omitempty"`
	Image        string    `json:"image"`
	State        string    `json:"state"` // running, exited, …; "stopped" for a Swarm service without a running task, "remote" for one that runs on another node
	SwarmService string    `json:"swarm_service,omitempty"`
	Data         []Data    `json:"data,omitempty"`
	Database     *Database `json:"database,omitempty"`
	// UnsupportedDatabase names a database engine without dump support; its
	// files are backed up as files, which is not consistent while it runs.
	UnsupportedDatabase string            `json:"unsupported_database,omitempty"`
	Networks            []string          `json:"networks,omitempty"`
	Ports               []string          `json:"ports,omitempty"` // published, host->container/proto
	RestartPolicy       string            `json:"restart_policy,omitempty"`
	MemoryLimit         int64             `json:"memory_limit_bytes,omitempty"`
	CPULimit            float64           `json:"cpu_limit,omitempty"`
	DependsOn           []string          `json:"depends_on,omitempty"`
	Domains             []string          `json:"domains,omitempty"`
	Labels              map[string]string `json:"labels,omitempty"` // without labels that may hold secrets
}

// Data is a volume or bind mount of a service.
type Data struct {
	Type          string `json:"type"` // volume or bind
	Name          string `json:"name,omitempty"`
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
	// BackupPath is where Relyper Backup sees the data; empty if the host
	// path is not mounted into Relyper Backup.
	BackupPath string `json:"backup_path,omitempty"`
	// DatabaseFiles marks the live files of a database. They are not backed
	// up as files (inconsistent while running); the database is dumped.
	DatabaseFiles bool `json:"database_files,omitempty"`
	// Class is persistent, configuration, database, cache or temporary.
	Class string `json:"class"`
}

// Database is a detected database server.
type Database struct {
	Engine string `json:"engine"`
	// DumpSupported: Relyper Backup can dump it consistently.
	DumpSupported bool `json:"dump_supported"`
	// Credentials names the environment variables the dump reads from the
	// container (names only, never values).
	Credentials []string `json:"credentials,omitempty"`
}

// PathMapping maps a host path prefix to the path where it is mounted into
// the Relyper Backup container.
type PathMapping struct {
	Host  string
	Local string
}

// Options tune a discovery run.
type Options struct {
	PathMap []PathMapping
	// SelfID is the container ID of Relyper Backup itself (excluded).
	SelfID string
	// Exists reports whether a local path exists; os.Stat by default.
	Exists func(string) bool
	// Details holds inspect results by container ID (restart policy,
	// resources); optional.
	Details map[string]*docker.ContainerDetails
	// LocalNode is the Swarm node ID of this daemon; Tasks the Swarm tasks
	// that should run and Volumes the names of the local volumes. With
	// Tasks nil, a Swarm service without a local container is reported as
	// running elsewhere unless it is scaled to 0.
	LocalNode string
	Tasks     []docker.Task
	Volumes   map[string]bool
}

// PathMapFromEnv reads RELYPER_HOST_PATH_MAP ("/host=/local,/a=/b"). Host
// paths that are mounted at the same path need no entry.
func PathMapFromEnv() ([]PathMapping, error) {
	var out []PathMapping
	for _, part := range strings.Split(os.Getenv("RELYPER_HOST_PATH_MAP"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		h, l, ok := strings.Cut(part, "=")
		if !ok || !filepath.IsAbs(h) || !filepath.IsAbs(l) {
			return nil, fmt.Errorf("RELYPER_HOST_PATH_MAP: %q must look like /host/path=/local/path", part)
		}
		out = append(out, PathMapping{Host: path.Clean(h), Local: path.Clean(l)})
	}
	// Longest prefix first.
	sort.Slice(out, func(i, j int) bool { return len(out[i].Host) > len(out[j].Host) })
	return out, nil
}

// SelfContainerID guesses the own container ID from the hostname, which
// Docker sets to the short container ID unless a hostname is configured.
func SelfContainerID() string {
	if v := os.Getenv("RELYPER_SELF_CONTAINER"); v != "" {
		return v
	}
	h, _ := os.Hostname()
	return h
}

// LocalPath maps a host path to the path inside Relyper Backup, or "" when
// it is not reachable.
func (o Options) LocalPath(host string) string {
	if !path.IsAbs(host) {
		return ""
	}
	host = path.Clean(host)
	exists := o.Exists
	if exists == nil {
		exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	for _, m := range o.PathMap {
		if host == m.Host || strings.HasPrefix(host, m.Host+"/") {
			p := m.Local + strings.TrimPrefix(host, m.Host)
			if exists(p) {
				return p
			}
			return ""
		}
	}
	if exists(host) {
		return host
	}
	return ""
}

// Discover asks the daemon and builds the inventory. Errors talking to the
// daemon are reported in Host.Error, not returned, so callers can show them.
func Discover(ctx context.Context, c *docker.Client, opt Options) *Inventory {
	inv := &Inventory{GeneratedAt: time.Now(), Docker: Host{Endpoint: c.Endpoint}}
	v, err := c.Ping(ctx)
	if err != nil {
		inv.Docker.Error = err.Error()
		inv.Docker.Hint = docker.Diagnose(c.Endpoint, err)
		return inv
	}
	inv.Docker.Available, inv.Docker.Version = true, v.Version
	inv.Docker.Access = "unrestricted"
	if g, err := c.Guard(ctx); err == nil {
		inv.Docker.Access, inv.Docker.GuardFeatures = "guard", g.Features
	}
	info, err := c.Info(ctx)
	if err == nil {
		inv.Docker.Name = info.Name
		inv.Docker.Swarm = info.Swarm.LocalNodeState == "active"
	}
	containers, err := c.ListContainers(ctx)
	if err != nil {
		inv.Docker.Error = err.Error()
		return inv
	}
	if opt.Details == nil {
		opt.Details = map[string]*docker.ContainerDetails{}
		for _, ct := range containers {
			if d, err := c.InspectContainer(ctx, ct.ID); err == nil {
				opt.Details[ct.ID] = d
			}
		}
	}
	var services []docker.Service
	if inv.Docker.Swarm && info != nil && info.Swarm.ControlAvailable {
		services, _ = c.ListServices(ctx)
		if len(services) > 0 {
			opt.LocalNode = info.Swarm.NodeID
			if t, err := c.ListTasks(ctx); err == nil {
				opt.Tasks = t
				if opt.Tasks == nil {
					opt.Tasks = []docker.Task{}
				}
			}
			if vols, err := c.ListVolumes(ctx); err == nil {
				opt.Volumes = map[string]bool{}
				for _, v := range vols {
					if v.Driver == "" || v.Driver == "local" {
						opt.Volumes[v.Name] = true
					}
				}
			}
		}
	}
	inv.Apps, inv.Docker.Easypanel = Build(containers, services, opt)
	return inv
}

var easypanelPath = regexp.MustCompile(`^/etc/easypanel/projects/([^/]+)/([^/]+)(/|$)`)

// Build groups containers and Swarm services into apps. It is pure and
// tested without a daemon.
func Build(containers []docker.ContainerSummary, services []docker.Service, opt Options) ([]App, bool) {
	easypanel := false
	for _, c := range containers {
		if isEasypanelImage(c.Image) {
			easypanel = true
		}
	}
	apps := map[string]*App{}
	get := func(id, name, kind string) *App {
		if a, ok := apps[id]; ok {
			return a
		}
		a := &App{ID: id, Name: name, Kind: kind}
		apps[id] = a
		return a
	}
	seenSwarm := map[string]bool{}
	specs := map[string]docker.Service{}
	for _, sv := range services {
		specs[sv.Spec.Name] = sv
	}
	for _, c := range containers {
		if c.Labels[docker.VerifyLabel] == "true" || c.Labels[docker.RoleLabel] == docker.RoleGuard || isSelf(c.ID, c.Name(), opt.SelfID) || isEasypanelImage(c.Image) {
			continue
		}
		svc := Service{
			Name: c.Name(), Container: c.Name(), ContainerID: c.ID, Image: c.Image, State: c.State,
			SwarmService: c.Labels["com.docker.swarm.service.name"],
		}
		svc.Database = detectDatabase(c.Image)
		if svc.Database == nil {
			svc.UnsupportedDatabase = unsupportedEngine(c.Image)
		}
		fillDetails(&svc, c, opt.Details[c.ID])
		if sp, ok := specs[svc.SwarmService]; ok {
			// Routing and policies of Swarm services live on the service,
			// not on its task containers.
			svc.Domains = mergeSorted(svc.Domains, domains(sp.Spec.Labels))
			if svc.RestartPolicy == "" || svc.RestartPolicy == "no" {
				svc.RestartPolicy = sp.Spec.TaskTemplate.RestartPolicy.Condition
			}
			if svc.MemoryLimit == 0 {
				svc.MemoryLimit = sp.Spec.TaskTemplate.Resources.Limits.MemoryBytes
			}
			if svc.CPULimit == 0 {
				svc.CPULimit = float64(sp.Spec.TaskTemplate.Resources.Limits.NanoCPUs) / 1e9
			}
		}
		for _, m := range c.Mounts {
			if m.Type == "tmpfs" {
				svc.Data = append(svc.Data, Data{Type: m.Type, ContainerPath: m.Destination, Class: ClassTemporary})
				continue
			}
			if m.Type != "volume" && m.Type != "bind" {
				continue
			}
			if m.Source == "" && m.Type == "volume" && m.Name != "" && (m.Driver == "" || m.Driver == "local") {
				// The container list can omit the source of a volume; local
				// volumes live under the daemon's volume directory.
				m.Source = "/var/lib/docker/volumes/" + m.Name + "/_data"
			}
			d := Data{Type: m.Type, Name: m.Name, HostPath: m.Source, ContainerPath: m.Destination, ReadOnly: !m.RW}
			if isSocket(m.Source) {
				continue
			}
			d.BackupPath = opt.LocalPath(m.Source)
			d.DatabaseFiles = svc.Database != nil && isDataDir(svc.Database.Engine, m.Destination)
			d.Class = classifyData(m.Type, m.Destination, d.DatabaseFiles)
			svc.Data = append(svc.Data, d)
		}
		appID, appName, kind, svcName := classify(c, easypanel)
		svc.Name = svcName
		if svc.SwarmService != "" {
			seenSwarm[svc.SwarmService] = true
		}
		a := get(appID, appName, kind)
		a.Services = append(a.Services, svc)
		for _, f := range deploymentFiles(c) {
			if !hasDeployment(a.Deployment, f) {
				a.Deployment = append(a.Deployment, DeploymentFile{HostPath: f, BackupPath: opt.LocalPath(f)})
			}
		}
	}
	// Swarm services without a task on this node.
	for _, s := range services {
		name := s.Spec.Name
		if seenSwarm[name] || isEasypanelImage(s.Spec.TaskTemplate.ContainerSpec.Image) {
			continue
		}
		appID, appName, kind, svcName := classifyService(s, easypanel)
		a := get(appID, appName, kind)
		img := s.Spec.TaskTemplate.ContainerSpec.Image
		state := swarmState(s, opt)
		rs := Service{
			Name: svcName, Image: img, State: state, SwarmService: name,
			Database: detectDatabase(img), Labels: safeLabels(s.Spec.Labels), Domains: domains(s.Spec.Labels),
			RestartPolicy: s.Spec.TaskTemplate.RestartPolicy.Condition,
			MemoryLimit:   s.Spec.TaskTemplate.Resources.Limits.MemoryBytes,
			CPULimit:      float64(s.Spec.TaskTemplate.Resources.Limits.NanoCPUs) / 1e9,
		}
		if rs.Database == nil {
			rs.UnsupportedDatabase = unsupportedEngine(img)
		}
		if state == "remote" {
			a.Notes = appendOnce(a.Notes, "Some services run on another Swarm node. Their data is not reachable from this node.")
		} else {
			// Stopped: the data stays on this node and is backed up from
			// the service definition.
			for _, m := range s.Spec.TaskTemplate.ContainerSpec.Mounts {
				// A stopped database's files are consistent: back them up
				// as files (no DatabaseFiles exclusion).
				d, ok := specData(m.Type, m.Source, m.Target, nil, opt)
				if !ok {
					if m.Type == "volume" && m.Source != "" {
						a.Notes = appendOnce(a.Notes, fmt.Sprintf("Volume %s of the stopped service %s is not on this node.", m.Source, svcName))
					}
					continue
				}
				rs.Data = append(rs.Data, d)
			}
			a.Notes = appendOnce(a.Notes, "Some services are stopped (no running task). Their data on this node is still backed up; database dumps need the database to run.")
		}
		a.Services = append(a.Services, rs)
	}
	out := make([]App, 0, len(apps))
	for _, a := range apps {
		sort.Slice(a.Services, func(i, j int) bool { return a.Services[i].Name < a.Services[j].Name })
		if a.Kind == KindEasypanel {
			a.Notes = appendOnce(a.Notes, "Easypanel layout (Swarm service names project_service, data under /etc/easypanel/projects) is assumed from conventions and not yet verified on a real Easypanel host.")
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, easypanel
}

func hasDeployment(list []DeploymentFile, f string) bool {
	for _, d := range list {
		if d.HostPath == f {
			return true
		}
	}
	return false
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// isSelf matches Relyper Backup's own container by ID prefix (the default
// hostname) or by name (a hostname set to the container name, as in the
// compose files, or RELYPER_SELF_CONTAINER).
func isSelf(id, name, self string) bool {
	if self == "" {
		return false
	}
	return name == self || (len(self) >= 12 && strings.HasPrefix(id, self))
}

func isSocket(p string) bool {
	return strings.HasSuffix(p, ".sock")
}

func isEasypanelImage(image string) bool {
	repo := imageRepo(image)
	return repo == "easypanel/easypanel"
}

// classify returns app ID, app name, kind and the service name.
func classify(c docker.ContainerSummary, easypanel bool) (string, string, string, string) {
	l := c.Labels
	for _, m := range c.Mounts {
		if g := easypanelPath.FindStringSubmatch(m.Source); g != nil {
			return KindEasypanel + ":" + g[1], g[1], KindEasypanel, g[2]
		}
	}
	if p := l["com.docker.compose.project"]; p != "" {
		s := l["com.docker.compose.service"]
		if s == "" {
			s = c.Name()
		}
		return KindCompose + ":" + p, p, KindCompose, s
	}
	if ns := l["com.docker.stack.namespace"]; ns != "" {
		return KindStack + ":" + ns, ns, KindStack, strings.TrimPrefix(l["com.docker.swarm.service.name"], ns+"_")
	}
	if svc := l["com.docker.swarm.service.name"]; svc != "" && easypanel {
		if p, s, ok := strings.Cut(svc, "_"); ok {
			return KindEasypanel + ":" + p, p, KindEasypanel, s
		}
	}
	if svc := l["com.docker.swarm.service.name"]; svc != "" {
		return KindStandalone + ":" + svc, svc, KindStandalone, svc
	}
	return KindStandalone + ":" + c.Name(), c.Name(), KindStandalone, c.Name()
}

func classifyService(s docker.Service, easypanel bool) (string, string, string, string) {
	name := s.Spec.Name
	if ns := s.Spec.Labels["com.docker.stack.namespace"]; ns != "" {
		return KindStack + ":" + ns, ns, KindStack, strings.TrimPrefix(name, ns+"_")
	}
	for _, m := range s.Spec.TaskTemplate.ContainerSpec.Mounts {
		if g := easypanelPath.FindStringSubmatch(m.Source); g != nil {
			return KindEasypanel + ":" + g[1], g[1], KindEasypanel, g[2]
		}
	}
	if easypanel {
		if p, sv, ok := strings.Cut(name, "_"); ok {
			return KindEasypanel + ":" + p, p, KindEasypanel, sv
		}
	}
	return KindStandalone + ":" + name, name, KindStandalone, name
}

// imageRepo returns the repository of an image reference without registry
// host, tag and digest: "docker.io/library/postgres:16" -> "postgres".
func imageRepo(image string) string {
	ref := image
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	parts := strings.Split(ref, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		parts = parts[1:]
	}
	if len(parts) == 2 && parts[0] == "library" {
		parts = parts[1:]
	}
	return strings.Join(parts, "/")
}

var engineByRepo = map[string]string{
	"postgres": EnginePostgres, "postgis/postgis": EnginePostgres, "timescale/timescaledb": EnginePostgres,
	"timescale/timescaledb-ha": EnginePostgres, "pgvector/pgvector": EnginePostgres, "supabase/postgres": EnginePostgres,
	"bitnami/postgresql": EnginePostgres,
	"mysql":              EngineMySQL, "bitnami/mysql": EngineMySQL, "percona": EngineMySQL, "percona/percona-server": EngineMySQL,
	"mariadb": EngineMariaDB, "bitnami/mariadb": EngineMariaDB, "linuxserver/mariadb": EngineMariaDB,
	"mongo": EngineMongo, "bitnami/mongodb": EngineMongo, "mongodb/mongodb-community-server": EngineMongo,
	"redis": EngineRedis, "bitnami/redis": EngineRedis, "redis/redis-stack-server": EngineRedis,
	"valkey/valkey": EngineRedis, "eqalpha/keydb": EngineRedis,
}

// Credential variables read at dump time (names only).
var credentialEnv = map[string][]string{
	EnginePostgres: {"POSTGRES_USER", "POSTGRES_PASSWORD"},
	EngineMySQL:    {"MYSQL_ROOT_PASSWORD", "MYSQL_USER", "MYSQL_PASSWORD"},
	EngineMariaDB:  {"MARIADB_ROOT_PASSWORD", "MYSQL_ROOT_PASSWORD", "MARIADB_USER", "MARIADB_PASSWORD"},
	EngineMongo:    {"MONGO_INITDB_ROOT_USERNAME", "MONGO_INITDB_ROOT_PASSWORD"},
	EngineRedis:    {"REDIS_PASSWORD"},
}

// Live data directories of the engines (official and Bitnami images).
var dataDirs = map[string][]string{
	EnginePostgres: {"/var/lib/postgresql", "/bitnami/postgresql", "/home/postgres/pgdata"},
	EngineMySQL:    {"/var/lib/mysql", "/bitnami/mysql"},
	EngineMariaDB:  {"/var/lib/mysql", "/bitnami/mariadb", "/config/databases"},
	EngineMongo:    {"/data/db", "/data/configdb", "/bitnami/mongodb"},
	EngineRedis:    {"/data", "/bitnami/redis"},
}

func isDataDir(engine, dest string) bool {
	for _, d := range dataDirs[engine] {
		if dest == d || strings.HasPrefix(dest, d+"/") {
			return true
		}
	}
	return false
}

// EngineOf returns the database engine of an image, or "".
func EngineOf(image string) string {
	return engineByRepo[imageRepo(image)]
}

func detectDatabase(image string) *Database {
	e := EngineOf(image)
	if e == "" {
		return nil
	}
	return &Database{Engine: e, DumpSupported: true, Credentials: credentialEnv[e]}
}

// Find returns the app with the given ID.
func (inv *Inventory) Find(id string) *App {
	for i := range inv.Apps {
		if inv.Apps[i].ID == id {
			return &inv.Apps[i]
		}
	}
	return nil
}

// ValidAppID reports whether id is safe to use in file names and hook
// commands.
func ValidAppID(id string) bool {
	return appIDRe.MatchString(id)
}

var appIDRe = regexp.MustCompile(`^[a-z_]+:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Slug turns an app ID into a directory name.
func Slug(id string) string {
	return strings.NewReplacer(":", "-", "/", "-").Replace(id)
}

// swarmState tells a Swarm service without a container on this node apart:
// "stopped" when it is scaled to 0 or has no running task, "remote" when a
// task runs on another node.
func swarmState(s docker.Service, opt Options) string {
	if r := s.Spec.Mode.Replicated; r != nil && r.Replicas != nil && *r.Replicas == 0 {
		return "stopped"
	}
	if opt.Tasks == nil {
		return "remote" // unknown: keep the cautious answer
	}
	for _, t := range opt.Tasks {
		if t.ServiceID == s.ID && t.Status.State == "running" && t.NodeID != opt.LocalNode {
			return "remote"
		}
	}
	return "stopped"
}

// specData describes a mount of a service definition for a service that has
// no container here. ok is false when its data is not on this node.
func specData(typ, source, target string, db *Database, opt Options) (Data, bool) {
	switch typ {
	case "tmpfs":
		return Data{Type: typ, ContainerPath: target, Class: ClassTemporary}, true
	case "volume":
		if source == "" || !opt.Volumes[source] {
			return Data{}, false
		}
		host := "/var/lib/docker/volumes/" + source + "/_data"
		d := Data{Type: typ, Name: source, HostPath: host, ContainerPath: target}
		d.BackupPath = opt.LocalPath(host)
		d.DatabaseFiles = db != nil && isDataDir(db.Engine, target)
		d.Class = classifyData(typ, target, d.DatabaseFiles)
		return d, true
	case "bind":
		if source == "" || isSocket(source) {
			return Data{}, false
		}
		d := Data{Type: typ, HostPath: source, ContainerPath: target}
		d.BackupPath = opt.LocalPath(source)
		d.DatabaseFiles = db != nil && isDataDir(db.Engine, target)
		d.Class = classifyData(typ, target, d.DatabaseFiles)
		return d, true
	}
	return Data{}, false
}
