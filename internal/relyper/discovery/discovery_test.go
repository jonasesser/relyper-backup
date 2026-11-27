// Relyper-owned tests for grouping containers into apps.
package discovery

import (
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
			docker.Mount{Type: "volume", Name: "shop_pg", Source: "/var/lib/docker/volumes/shop_pg/_data", Destination: "/var/lib/postgresql/data", RW: true}),
		ctr("b2", "shop-web-1", "ghcr.io/acme/shop:2", "running",
			map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "web"},
			docker.Mount{Type: "bind", Source: "/srv/shop/uploads", Destination: "/app/uploads", RW: true},
			docker.Mount{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"}),
		ctr("c1", "crm_db.1.xyz", "mariadb:11@sha256:abc", "running",
			map[string]string{"com.docker.stack.namespace": "crm", "com.docker.swarm.service.name": "crm_db"},
			docker.Mount{Type: "volume", Name: "crm_data", Source: "/var/lib/docker/volumes/crm_data/_data", Destination: "/var/lib/mysql", RW: true}),
		ctr("d1", "nginx", "nginx:1.27", "exited", nil),
		ctr("e1", "relyper-verify-1", "postgres:16", "running", map[string]string{docker.VerifyLabel: "true"}),
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
	for _, id := range []string{"compose:shop", "swarm_stack:crm", "container:nginx", "easypanel:blog"} {
		if !ids[id] {
			t.Errorf("missing app %s in %v", id, ids)
		}
	}
	if len(apps) != 4 {
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

	blog := inv.Find("easypanel:blog")
	if len(blog.Services) != 2 || blog.Services[0].Name != "db" || blog.Services[1].Name != "wordpress" || len(blog.Notes) == 0 {
		t.Fatalf("blog: %+v", blog)
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
	for id, ok := range map[string]bool{"compose:shop": true, "easypanel:my-blog_2": true, "compose:a;rm -rf /": false, "compose:": false, "x": false, "compose:'a'": false} {
		if ValidAppID(id) != ok {
			t.Errorf("ValidAppID(%q) != %v", id, ok)
		}
	}
}
