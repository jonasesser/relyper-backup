// Relyper-owned unit tests for service details and data classes.
package discovery

import (
	"reflect"
	"testing"

	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

func TestClassifyData(t *testing.T) {
	cases := []struct {
		typ, dest string
		db        bool
		want      string
	}{
		{"volume", "/var/lib/postgresql/data", true, ClassDatabase},
		{"tmpfs", "/scratch", false, ClassTemporary},
		{"bind", "/tmp/uploads", false, ClassTemporary},
		{"volume", "/app/.cache", false, ClassCache},
		{"volume", "/var/cache/nginx", false, ClassCache},
		{"bind", "/etc/nginx/nginx.conf", false, ClassConfiguration},
		{"volume", "/config", false, ClassConfiguration},
		{"bind", "/app/settings.yml", false, ClassConfiguration},
		{"volume", "/app/uploads", false, ClassPersistent},
		{"volume", "/var/www/html", false, ClassPersistent},
	}
	for _, c := range cases {
		if got := classifyData(c.typ, c.dest, c.db); got != c.want {
			t.Errorf("classifyData(%s, %s) = %s, want %s", c.typ, c.dest, got, c.want)
		}
	}
	if Backed(ClassCache) || Backed(ClassTemporary) || Backed(ClassDatabase) || !Backed(ClassConfiguration) || !Backed(ClassPersistent) {
		t.Error("Backed")
	}
}

func TestDetails(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.shop.rule":                "Host(`shop.example.com`) || Host(`www.shop.example.com`)",
		"traefik.http.middlewares.auth.basicauth.users": "admin:$apr1$secret",
		"com.docker.compose.depends_on":                 "db:service_started:false,cache:service_healthy:true",
		"com.docker.compose.project.config_files":       "/srv/shop/docker-compose.yml,relative.yml",
		"com.docker.compose.project":                    "shop",
		"com.docker.compose.service":                    "web",
		"com.example.api_token":                         "abc",
		"caddy":                                         "blog.example.org",
		"org.opencontainers.image.description":          "web shop",
	}
	if got := domains(labels); !reflect.DeepEqual(got, []string{"blog.example.org", "shop.example.com", "www.shop.example.com"}) {
		t.Errorf("domains %v", got)
	}
	if got := dependsOn(labels); !reflect.DeepEqual(got, []string{"cache", "db"}) {
		t.Errorf("dependsOn %v", got)
	}
	safe := safeLabels(labels)
	for k := range safe {
		if k == "traefik.http.middlewares.auth.basicauth.users" || k == "com.example.api_token" {
			t.Errorf("secret label kept: %s", k)
		}
	}
	if safe["com.docker.compose.service"] != "web" {
		t.Error("ordinary label dropped")
	}
	c := docker.ContainerSummary{ID: "abc", Names: []string{"/shop-web-1"}, Image: "acme/shop", State: "running", Labels: labels,
		Ports:  []docker.Port{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}, {PrivatePort: 9000, Type: "tcp"}},
		Mounts: []docker.Mount{{Type: "tmpfs", Destination: "/run/x"}, {Type: "bind", Source: "/srv/shop/cache", Destination: "/app/cache"}},
	}
	c.NetworkSettings.Networks = map[string]struct{}{"shop_default": {}, "proxy": {}}
	d := &docker.ContainerDetails{}
	d.HostConfig.RestartPolicy.Name = "unless-stopped"
	d.HostConfig.Memory = 512 << 20
	d.HostConfig.NanoCpus = 1500000000
	exists := map[string]bool{"/srv/shop/docker-compose.yml": true, "/srv/shop/cache": true}
	apps, _ := Build([]docker.ContainerSummary{c}, nil, Options{Exists: func(p string) bool { return exists[p] }, Details: map[string]*docker.ContainerDetails{"abc": d}})
	if len(apps) != 1 {
		t.Fatalf("%+v", apps)
	}
	a := apps[0]
	s := a.Services[0]
	if !reflect.DeepEqual(s.Networks, []string{"proxy", "shop_default"}) || !reflect.DeepEqual(s.Ports, []string{"8080->80/tcp"}) ||
		s.RestartPolicy != "unless-stopped" || s.MemoryLimit != 512<<20 || s.CPULimit != 1.5 {
		t.Errorf("service details %+v", s)
	}
	if len(s.Data) != 2 || s.Data[0].Class != ClassTemporary || s.Data[1].Class != ClassCache {
		t.Errorf("data %+v", s.Data)
	}
	if !reflect.DeepEqual(a.Deployment, []DeploymentFile{{HostPath: "/srv/shop/docker-compose.yml", BackupPath: "/srv/shop/docker-compose.yml"}}) {
		t.Errorf("deployment %+v", a.Deployment)
	}
	if unsupportedEngine("mcr.microsoft.com/mssql/server:2022-latest") != "mssql" || unsupportedEngine("docker.io/library/influxdb:2") != "influxdb" || unsupportedEngine("nginx") != "" {
		t.Error("unsupportedEngine")
	}
}
