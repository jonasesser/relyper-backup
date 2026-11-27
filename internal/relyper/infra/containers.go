// Relyper-owned: listings for the MCP tools named in the product concept
// (containers, Swarm services, volumes, Easypanel projects, databases) and
// stopping or restarting app containers after an approval.
package infra

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

// ContainerView is one discovered container or Swarm service.
type ContainerView struct {
	App string `json:"app"`
	discovery.Service
}

// Containers lists every service of every app.
func (s *Service) Containers(ctx context.Context, swarmOnly bool) []ContainerView {
	var out []ContainerView
	for _, a := range s.Inventory(ctx, false).Apps {
		for _, svc := range a.Services {
			if swarmOnly && svc.SwarmService == "" {
				continue
			}
			out = append(out, ContainerView{App: a.ID, Service: svc})
		}
	}
	return out
}

// VolumeView is a named volume and who uses it.
type VolumeView struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Mountpoint string   `json:"mountpoint"`
	BackupPath string   `json:"backup_path,omitempty"` // empty: not reachable from Relyper Backup
	UsedBy     []string `json:"used_by,omitempty"`     // app/service
	Class      string   `json:"class,omitempty"`
	Unused     bool     `json:"unused,omitempty"`
}

// Volumes lists the named volumes of the host.
func (s *Service) Volumes(ctx context.Context) ([]VolumeView, error) {
	if s.docker == nil {
		return nil, errors.New("docker is not available")
	}
	vols, err := s.docker.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	use := map[string][]string{}
	class := map[string]string{}
	for _, a := range s.Inventory(ctx, false).Apps {
		for _, svc := range a.Services {
			for _, d := range svc.Data {
				if d.Type == "volume" && d.Name != "" {
					use[d.Name] = append(use[d.Name], a.ID+"/"+svc.Name)
					class[d.Name] = d.Class
				}
			}
		}
	}
	opt := discovery.Options{PathMap: s.s.PathMap}
	out := make([]VolumeView, 0, len(vols))
	for _, v := range vols {
		vv := VolumeView{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, UsedBy: use[v.Name], Class: class[v.Name], Unused: len(use[v.Name]) == 0}
		if v.Mountpoint != "" {
			vv.BackupPath = opt.LocalPath(v.Mountpoint)
		}
		out = append(out, vv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DatabaseView is a detected database service.
type DatabaseView struct {
	App           string   `json:"app"`
	Service       string   `json:"service"`
	Container     string   `json:"container,omitempty"`
	Image         string   `json:"image"`
	Engine        string   `json:"engine"`
	State         string   `json:"state"`
	DumpSupported bool     `json:"dump_supported"`
	Credentials   []string `json:"credential_variables,omitempty"` // names only
	DataPaths     []string `json:"data_paths,omitempty"`
}

// Databases lists the detected databases.
func (s *Service) Databases(ctx context.Context) []DatabaseView {
	var out []DatabaseView
	for _, a := range s.Inventory(ctx, false).Apps {
		for _, svc := range a.Services {
			if svc.Database == nil && svc.UnsupportedDatabase == "" {
				continue
			}
			d := DatabaseView{App: a.ID, Service: svc.Name, Container: svc.Container, Image: svc.Image, State: svc.State}
			if svc.Database != nil {
				d.Engine, d.DumpSupported, d.Credentials = svc.Database.Engine, svc.Database.DumpSupported, svc.Database.Credentials
			} else {
				d.Engine = svc.UnsupportedDatabase
			}
			for _, m := range svc.Data {
				if m.HostPath != "" {
					d.DataPaths = append(d.DataPaths, m.HostPath)
				}
			}
			out = append(out, d)
		}
	}
	return out
}

// EasypanelProject is an Easypanel project with its services.
type EasypanelProject struct {
	Project  string              `json:"project"`
	App      string              `json:"app"`
	Services []discovery.Service `json:"services"`
	Domains  []string            `json:"domains,omitempty"`
	Notes    []string            `json:"notes,omitempty"`
}

// EasypanelProjects lists the Easypanel projects.
func (s *Service) EasypanelProjects(ctx context.Context) (bool, []EasypanelProject) {
	inv := s.Inventory(ctx, false)
	var out []EasypanelProject
	for _, a := range inv.Apps {
		if a.Kind != discovery.KindEasypanel {
			continue
		}
		p := EasypanelProject{Project: a.Name, App: a.ID, Services: a.Services, Notes: a.Notes}
		set := map[string]bool{}
		for _, svc := range a.Services {
			for _, d := range svc.Domains {
				set[d] = true
			}
		}
		for d := range set {
			p.Domains = append(p.Domains, d)
		}
		sort.Strings(p.Domains)
		out = append(out, p)
	}
	return inv.Docker.Easypanel, out
}

// DumpPlans returns the plans that dump the app's databases.
func (s *Service) DumpPlans(appID string) ([]string, error) {
	cfg, err := s.cfg()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range cfg.Plans {
		if id, _ := HookApp(p); id == appID {
			out = append(out, p.Id)
		}
	}
	return out, nil
}

// ContainerTarget is a container of a discovered app.
type ContainerTarget struct {
	App         string `json:"app"`
	Service     string `json:"service"`
	Container   string `json:"container"`
	ContainerID string `json:"container_id"`
	Image       string `json:"image"`
	State       string `json:"state"`
	Database    string `json:"database,omitempty"`
}

// FindContainer resolves a service of a discovered app to its container on
// this node. Relyper Backup's own container, Easypanel itself and
// verification containers are never part of an app, so they cannot be
// targeted.
func (s *Service) FindContainer(ctx context.Context, appID, service string) (*ContainerTarget, error) {
	if !discovery.ValidAppID(appID) {
		return nil, fmt.Errorf("invalid app ID %q", appID)
	}
	app := s.Inventory(ctx, true).Find(appID)
	if app == nil {
		return nil, fmt.Errorf("app %q not found", appID)
	}
	for _, svc := range app.Services {
		if svc.Name != service {
			continue
		}
		if svc.ContainerID == "" {
			return nil, fmt.Errorf("service %q has no container on this node", service)
		}
		t := &ContainerTarget{App: appID, Service: svc.Name, Container: svc.Container, ContainerID: svc.ContainerID, Image: svc.Image, State: svc.State}
		if svc.Database != nil {
			t.Database = svc.Database.Engine
		}
		return t, nil
	}
	return nil, fmt.Errorf("service %q not found in app %q", service, appID)
}

// ContainerAction stops or restarts an app container. Callers must have
// obtained an approval first.
func (s *Service) ContainerAction(ctx context.Context, t *ContainerTarget, action, actor string) error {
	if s.docker == nil {
		return errors.New("docker is not available")
	}
	var err error
	switch action {
	case "stop":
		err = s.docker.StopContainer(ctx, t.ContainerID, 30)
	case "restart":
		err = s.docker.RestartContainer(ctx, t.ContainerID, 30)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	s.record(actor, "container_"+action, outcome, t.App+"/"+t.Service+" ("+t.Container+")")
	s.Inventory(ctx, true)
	return err
}
