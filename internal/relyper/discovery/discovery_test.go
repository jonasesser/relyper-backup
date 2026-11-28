// Relyper-owned tests for grouping containers into apps.
package discovery

import (
	"sort"
	"strings"
	"testing"

	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

func ctr(id, name, image, state string, labels map[string]string, mounts ...docker.Mount) docker.ContainerSummary {
	return docker.ContainerSummary{ID: id, Names: []string{"/" + name}, Image: image, State: state, Labels: labels, Mounts: mounts}
}

func TestBuild(t *testing.T) {
	exists := map[string]bool{"/var/lib/docker/volumes/shop_pg/_data": true, "/sources/srv/shop/uploads": true}
	opt := Options{
		SelfID:  "aaaaaaaaaaaa",
		PathMap: []PathMapping{{Host: "/srv", Local: "/sources/srv"}},
		Exists:  func(p string) bool { return exists[p] },
	}
	containers := []docker.ContainerSummary{
		ctr("aaaaaaaaaaaa1111", "relyper-backup", "relyper-backup:latest", "running", nil),
		ctr("b1", "shop-db-1", "docker.io/library/postgres:16-alpine", "running",
			map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "db"},
			docker.Mount{Type: "volume", Name: "shop_pg", Driver: "local", Destination: "/var/lib/postgresql/data", RW: true}),
		ctr("b2", "shop-web-1", "ghcr.io/acme/shop:2", "running",
			map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "web"},
			docker.Mount{Type: "bind", Source: "/srv/shop/uploads", Destination: "/app/uploads", RW: true},
			docker.Mount{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"}),
		ctr("c1", "crm_db.1.xyz", "mariadb:11@sha256:abc", "running",
			map[string]string{"com.docker.stack.namespace": "crm", "com.docker.swarm.service.name": "crm_db"},
			docker.Mount{Type: "volume", Name: "crm_data", Source: "/var/lib/docker/volumes/crm_data/_data", Destination: "/var/lib/mysql", RW: true}),
		ctr("d1", "nginx", "nginx:1.27", "exited", nil),
		ctr("e1", "relyper-verify-1", "postgres:16", "running", map[string]string{docker.VerifyLabel: "true"}),
		ctr("e2", "relyper-docker-guard", "relyper-docker-guard:latest", "running", map[string]string{docker.RoleLabel: docker.RoleGuard}),
		ctr("f1", "easypanel.1.x", "easypanel/easypanel:latest", "running", map[string]string{"com.docker.swarm.service.name": "easypanel"}),
		ctr("g1", "blog_wordpress.1.y", "wordpress:6", "running",
			map[string]string{"com.docker.swarm.service.name": "blog_wordpress"},
			docker.Mount{Type: "bind", Source: "/etc/easypanel/projects/blog/wordpress/volumes/data", Destination: "/var/www/html", RW: true}),
		ctr("g2", "blog_db.1.z", "mysql:8.4", "running", map[string]string{"com.docker.swarm.service.name": "blog_db"}),
	}
	var svc docker.Service
	svc.Spec.Name = "crm_cache"
	svc.Spec.Labels = map[string]string{"com.docker.stack.namespace": "crm"}
	svc.Spec.TaskTemplate.ContainerSpec.Image = "redis:7"

	apps, easypanel := Build(containers, []docker.Service{svc}, opt)
	if !easypanel {
		t.Fatal("easypanel not detected")
	}
	inv := &Inventory{Apps: apps}
	ids := map[string]bool{}
	for _, a := range apps {
		ids[a.ID] = true
	}
	for _, id := range []string{"compose:shop", "swarm_stack:crm", "container:nginx", "easypanel:blog_wordpress", "easypanel:blog_db"} {
		if !ids[id] {
			t.Errorf("missing app %s in %v", id, ids)
		}
	}
	if len(apps) != 5 {
		t.Errorf("unexpected apps (self, verify and easypanel containers must be skipped): %v", ids)
	}

	shop := inv.Find("compose:shop")
	db, web := shop.Services[0], shop.Services[1]
	if db.Name != "db" || db.Database == nil || db.Database.Engine != EnginePostgres {
		t.Fatalf("db: %+v", db)
	}
	if !db.Data[0].DatabaseFiles || db.Data[0].BackupPath != "/var/lib/docker/volumes/shop_pg/_data" {
		t.Errorf("db data: %+v", db.Data[0])
	}
	if len(web.Data) != 1 || web.Data[0].BackupPath != "/sources/srv/shop/uploads" || web.Data[0].DatabaseFiles {
		t.Errorf("web data (socket must be skipped, path mapped): %+v", web.Data)
	}

	crm := inv.Find("swarm_stack:crm")
	if len(crm.Services) != 2 || crm.Services[0].Name != "cache" || crm.Services[0].State != "remote" || crm.Services[1].Name != "db" {
		t.Fatalf("crm: %+v", crm.Services)
	}
	if crm.Services[1].Database.Engine != EngineMariaDB || crm.Services[1].Data[0].BackupPath != "" || len(crm.Notes) == 0 {
		t.Errorf("crm db: %+v notes %v", crm.Services[1], crm.Notes)
	}

	// Each Easypanel service is its own app; the project is a label.
	wp := inv.Find("easypanel:blog_wordpress")
	if wp == nil || wp.Name != "wordpress" || wp.Project != "blog" || len(wp.Services) != 1 || len(wp.Notes) == 0 {
		t.Fatalf("wordpress: %+v", wp)
	}
	// Plans made for the whole project (easypanel:blog) still find all of
	// its services.
	blog := inv.Find("easypanel:blog")
	if blog == nil || len(blog.Services) != 2 || len(blog.Members) != 2 {
		t.Fatalf("old project ID: %+v", blog)
	}
}

func TestImageRepo(t *testing.T) {
	cases := map[string]string{
		"postgres":                            "postgres",
		"postgres:16":                         "postgres",
		"docker.io/library/postgres:16":       "postgres",
		"registry.example:5000/team/app:1":    "team/app",
		"bitnami/postgresql@sha256:abcdef":    "bitnami/postgresql",
		"ghcr.io/acme/shop":                   "acme/shop",
		"localhost/mariadb:11":                "mariadb",
		"easypanel/easypanel:1.2@sha256:abcd": "easypanel/easypanel",
	}
	for in, want := range cases {
		if got := imageRepo(in); got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", in, got, want)
		}
	}
	if EngineOf("valkey/valkey:8") != EngineRedis || EngineOf("nginx") != "" {
		t.Error("EngineOf")
	}
}

func TestPathMapAndIDs(t *testing.T) {
	t.Setenv("RELYPER_HOST_PATH_MAP", "/srv=/sources/srv, /srv/big=/big")
	m, err := PathMapFromEnv()
	if err != nil || len(m) != 2 || m[0].Host != "/srv/big" {
		t.Fatalf("%v %v", m, err)
	}
	t.Setenv("RELYPER_HOST_PATH_MAP", "relative=/x")
	if _, err := PathMapFromEnv(); err == nil {
		t.Fatal("relative path accepted")
	}
	if !isSelf("f00d", "relyper-backup", "relyper-backup") || isSelf("f00d", "web", "relyper-backup") || isSelf("f00d", "web", "") {
		t.Error("isSelf")
	}
	if (Options{Exists: func(string) bool { return true }}).LocalPath("") != "" {
		t.Error("an empty mount source must not map to the working directory")
	}
	for id, ok := range map[string]bool{"compose:shop": true, "easypanel:my-blog_2": true, "compose:a;rm -rf /": false, "compose:": false, "x": false, "compose:'a'": false} {
		if ValidAppID(id) != ok {
			t.Errorf("ValidAppID(%q) != %v", id, ok)
		}
	}
}

func TestSwarmStoppedVersusRemote(t *testing.T) {
	zero, one := uint64(0), uint64(1)
	mk := func(id, name string, replicas *uint64, mounts ...[3]string) docker.Service {
		var s docker.Service
		s.ID = id
		s.Spec.Name = name
		s.Spec.Labels = map[string]string{"com.docker.stack.namespace": "voice"}
		s.Spec.TaskTemplate.ContainerSpec.Image = "app:1"
		if replicas != nil {
			s.Spec.Mode.Replicated = &struct {
				Replicas *uint64 `json:"Replicas"`
			}{Replicas: replicas}
		}
		for _, m := range mounts {
			s.Spec.TaskTemplate.ContainerSpec.Mounts = append(s.Spec.TaskTemplate.ContainerSpec.Mounts, struct {
				Type   string `json:"Type"`
				Source string `json:"Source"`
				Target string `json:"Target"`
			}{m[0], m[1], m[2]})
		}
		return s
	}
	running := func(svc, node string) docker.Task {
		var t docker.Task
		t.ServiceID, t.NodeID, t.DesiredState, t.Status.State = svc, node, "running", "running"
		return t
	}
	opt := Options{
		Exists:    func(string) bool { return true },
		LocalNode: "here",
		Volumes:   map[string]bool{"voice_data": true},
		Tasks:     []docker.Task{running("s3", "elsewhere"), running("s4", "here")},
	}
	services := []docker.Service{
		mk("s1", "voice_scaled", &zero, [3]string{"volume", "voice_data", "/data"}, [3]string{"volume", "missing_vol", "/other"}),
		mk("s2", "voice_crashed", &one, [3]string{"bind", "/srv/voice", "/srv"}),
		mk("s3", "voice_remote", &one),
	}
	apps, _ := Build(nil, services, opt)
	inv := &Inventory{Apps: apps}
	a := inv.Find("swarm_stack:voice")
	if a == nil {
		t.Fatalf("apps: %+v", apps)
	}
	st := map[string]Service{}
	for _, s := range a.Services {
		st[s.Name] = s
	}
	if s := st["scaled"]; s.State != "stopped" || len(s.Data) != 1 || s.Data[0].BackupPath != "/var/lib/docker/volumes/voice_data/_data" {
		t.Errorf("scaled to 0: %+v", s)
	}
	if s := st["crashed"]; s.State != "stopped" || len(s.Data) != 1 || s.Data[0].BackupPath != "/srv/voice" {
		t.Errorf("no running task: %+v", s)
	}
	if s := st["remote"]; s.State != "remote" || len(s.Data) != 0 {
		t.Errorf("remote: %+v", s)
	}
	notes := strings.Join(a.Notes, " ")
	if !strings.Contains(notes, "missing_vol") || !strings.Contains(notes, "stopped") || !strings.Contains(notes, "another Swarm node") {
		t.Errorf("notes: %v", a.Notes)
	}
}

func TestTags(t *testing.T) {
	path := t.TempDir() + "/" + TagsFileName
	if tags, err := LoadTags(path); err != nil || len(tags) != 0 {
		t.Fatalf("missing file: %v %v", tags, err)
	}
	if _, err := CleanTags([]string{"ok", "no;rm -rf"}); err == nil {
		t.Fatal("bad tag accepted")
	}
	clean, err := CleanTags([]string{"Berlin Beat", "berlin-beat", " ", "shop"})
	if err != nil || len(clean) != 2 || clean[0] != "berlin-beat" {
		t.Fatalf("clean: %v %v", clean, err)
	}
	tags := Tags{"easypanel:hosting_web": {"berlin-beat"}, "easypanel:data_db": {"berlin-beat", "shared"}}
	if err := SaveTags(path, tags); err != nil {
		t.Fatal(err)
	}
	tags, err = LoadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	inv := &Inventory{Apps: []App{
		{ID: "easypanel:hosting_web", Name: "web", Kind: KindEasypanel, Project: "hosting", Services: []Service{{Name: "web", DependsOn: []string{"db"}}}},
		{ID: "easypanel:data_db", Name: "db", Kind: KindEasypanel, Project: "data", Services: []Service{{Name: "db", Database: &Database{Engine: EnginePostgres}}}},
	}}
	inv.ApplyTags(tags)
	inv.ApplyTags(tags) // idempotent
	g := inv.Find(GroupID("berlin-beat"))
	if g == nil || len(g.Members) != 2 || len(g.Services) != 2 || !ValidAppID(g.ID) {
		t.Fatalf("group: %+v", g)
	}
	names := []string{g.Services[0].Name, g.Services[1].Name}
	sort.Strings(names)
	if names[0] != "db.db" || names[1] != "web.web" {
		t.Fatalf("service names: %v", names)
	}
	if web := inv.Find("easypanel:hosting_web"); web.Services[0].DependsOn[0] != "db" || len(web.Tags) != 1 {
		t.Fatalf("member changed: %+v", web)
	}
	if n := len(inv.Apps); n != 4 {
		t.Fatalf("apps: %d", n)
	}
}
