// Relyper-owned: details of discovered services (networks, ports, restart
// policy, resources, dependencies, domains, labels), the classification of
// their data and the deployment definitions of apps.
package discovery

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

// Classes of data.
const (
	ClassPersistent    = "persistent"    // application data, backed up
	ClassConfiguration = "configuration" // configuration files or directories, backed up
	ClassDatabase      = "database"      // live database files, protected by the dump
	ClassCache         = "cache"         // can be rebuilt, not backed up
	ClassTemporary     = "temporary"     // tmpfs or /tmp, not backed up
	// ClassSource is a read-only mount of Relyper Backup itself through
	// which it reads the data of other apps (/var/lib/docker/volumes,
	// /etc/easypanel, …); it is backed up through those apps.
	ClassSource = "source"
)

// Backed reports whether data of this class belongs in a backup.
func Backed(class string) bool {
	return class != ClassCache && class != ClassTemporary && class != ClassDatabase && class != ClassSource
}

// DeploymentFile is a file that defines how an app is deployed, e.g. its
// compose file.
type DeploymentFile struct {
	HostPath   string `json:"host_path"`
	BackupPath string `json:"backup_path,omitempty"` // empty if not mounted into Relyper Backup
}

var (
	cacheSeg  = regexp.MustCompile(`(^|/)(\.?cache|caches|cache-?data)(/|$)`)
	tmpSeg    = regexp.MustCompile(`^/(tmp|var/tmp|run)(/|$)`)
	configExt = regexp.MustCompile(`\.(conf|cfg|ini|ya?ml|json|toml|env|xml|properties)$`)
)

// classify returns the class of a mount from its type and container path.
func classifyData(mountType, containerPath string, databaseFiles bool) string {
	p := path.Clean(containerPath)
	switch {
	case databaseFiles:
		return ClassDatabase
	case mountType == "tmpfs" || tmpSeg.MatchString(p):
		return ClassTemporary
	case cacheSeg.MatchString(strings.ToLower(p)):
		return ClassCache
	case strings.HasPrefix(p, "/etc/") || p == "/etc" || p == "/config" || strings.HasPrefix(p, "/config/") ||
		strings.HasSuffix(p, "/conf") || strings.HasSuffix(p, "/config") || configExt.MatchString(p):
		return ClassConfiguration
	}
	return ClassPersistent
}

// Engines without dump support. Their data is backed up as files only,
// which is not a consistent backup while they run.
var unsupportedByRepo = map[string]string{
	"mcr.microsoft.com/mssql/server": "mssql", "mssql/server": "mssql", "cassandra": "cassandra",
	"elasticsearch": "elasticsearch", "elasticsearch/elasticsearch": "elasticsearch", "opensearchproject/opensearch": "opensearch",
	"influxdb": "influxdb", "couchdb": "couchdb", "neo4j": "neo4j", "clickhouse/clickhouse-server": "clickhouse",
	"memcached": "memcached", "rethinkdb": "rethinkdb", "arangodb": "arangodb", "cockroachdb/cockroach": "cockroachdb",
	"qdrant/qdrant": "qdrant", "meilisearch/meilisearch": "meilisearch", "getmeili/meilisearch": "meilisearch",
}

func unsupportedEngine(image string) string {
	ref := image
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	if e, ok := unsupportedByRepo[ref]; ok {
		return e
	}
	return unsupportedByRepo[imageRepo(image)]
}

// Label keys or values that may carry secrets are left out.
var secretLabel = regexp.MustCompile(`(?i)(pass(word)?|secret|token|api[_-]?key|private|credential|basicauth|digestauth|users)`)

func safeLabels(l map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range l {
		if secretLabel.MatchString(k) || secretLabel.MatchString(v) || len(v) > 300 {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var hostRule = regexp.MustCompile("Host(?:SNI)?\\(([^)]*)\\)")
var backtickHost = regexp.MustCompile("`([^`]+)`")

// domains extracts host names from Traefik router rules and Caddy labels.
func domains(l map[string]string) []string {
	set := map[string]bool{}
	for k, v := range l {
		switch {
		case strings.HasPrefix(k, "traefik.") && strings.HasSuffix(k, ".rule"):
			for _, m := range hostRule.FindAllStringSubmatch(v, -1) {
				for _, h := range backtickHost.FindAllStringSubmatch(m[1], -1) {
					set[h[1]] = true
				}
			}
		case k == "caddy" || (strings.HasPrefix(k, "caddy_") && !strings.Contains(k, ".")):
			for _, h := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' }) {
				h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
				if strings.Contains(h, ".") {
					set[h] = true
				}
			}
		}
	}
	return sortedKeys(set)
}

// dependsOn reads the compose dependency label ("db:service_started:false,…").
func dependsOn(l map[string]string) []string {
	var out []string
	for _, part := range strings.Split(l["com.docker.compose.depends_on"], ",") {
		if name, _, _ := strings.Cut(strings.TrimSpace(part), ":"); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func ports(ps []docker.Port) []string {
	set := map[string]bool{}
	for _, p := range ps {
		if p.PublicPort == 0 {
			continue
		}
		ip := p.IP
		if ip == "" || ip == "0.0.0.0" || ip == "::" {
			ip = ""
		} else {
			ip += ":"
		}
		set[ip+strconv.Itoa(p.PublicPort)+"->"+strconv.Itoa(p.PrivatePort)+"/"+p.Type] = true
	}
	return sortedKeys(set)
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fillDetails adds what the container list and inspect tell about a service.
func fillDetails(svc *Service, c docker.ContainerSummary, d *docker.ContainerDetails) {
	for n := range c.NetworkSettings.Networks {
		svc.Networks = append(svc.Networks, n)
	}
	sort.Strings(svc.Networks)
	if len(svc.Networks) == 0 && c.HostConfig.NetworkMode != "" {
		svc.Networks = []string{c.HostConfig.NetworkMode}
	}
	svc.Ports = ports(c.Ports)
	svc.DependsOn = dependsOn(c.Labels)
	svc.Domains = domains(c.Labels)
	svc.Labels = safeLabels(c.Labels)
	if d != nil {
		svc.RestartPolicy = d.HostConfig.RestartPolicy.Name
		svc.MemoryLimit = d.HostConfig.Memory
		svc.CPULimit = float64(d.HostConfig.NanoCpus) / 1e9
		if svc.Database == nil {
			svc.DatabaseLinks = databaseLinks(d.Config.Env)
		}
	}
}

// deploymentFiles returns the compose files an app was started from.
func deploymentFiles(c docker.ContainerSummary) []string {
	var out []string
	for _, f := range strings.Split(c.Labels["com.docker.compose.project.config_files"], ",") {
		if f = strings.TrimSpace(f); path.IsAbs(f) {
			out = append(out, path.Clean(f))
		}
	}
	return out
}

func mergeSorted(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range append(append([]string{}, a...), b...) {
		set[x] = true
	}
	return sortedKeys(set)
}

// Host directories that hold the data of many apps. A read-only bind of
// one of them is a source mount of a backup tool, not data of its own.
var sourceRoots = map[string]bool{
	"/": true, "/var/lib/docker": true, "/var/lib/docker/volumes": true,
	"/etc/easypanel": true, "/etc/easypanel/projects": true,
}

// markSourceMounts classifies read-only binds that contain the data of
// other apps as ClassSource. It catches Relyper Backup's own source mounts
// when its container could not be recognized (e.g. a Swarm task whose
// hostname differs) and other backup tools that read the whole host.
func markSourceMounts(apps map[string]*App) {
	type owner struct{ app, path string }
	var others []owner
	for id, a := range apps {
		for _, s := range a.Services {
			for _, d := range s.Data {
				if d.HostPath != "" {
					others = append(others, owner{id, path.Clean(d.HostPath)})
				}
			}
		}
	}
	contains := func(id, dir string) bool {
		for _, o := range others {
			if o.app != id && o.path != dir && strings.HasPrefix(o.path, strings.TrimSuffix(dir, "/")+"/") {
				return true
			}
		}
		return false
	}
	for id, a := range apps {
		for i := range a.Services {
			for j := range a.Services[i].Data {
				d := &a.Services[i].Data[j]
				if d.Type != "bind" || !d.ReadOnly || d.HostPath == "" || d.Class == ClassSource {
					continue
				}
				if p := path.Clean(d.HostPath); sourceRoots[p] || contains(id, p) {
					d.Class = ClassSource
				}
			}
		}
	}
}
