package infra

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/discovery"
)

func TestSetTagsAndGroupPlan(t *testing.T) {
	root := t.TempDir()
	s := &Service{s: Settings{DumpDir: filepath.Join(root, "dumps"), DumpCommand: "relyper-dbdump",
		TagsPath: filepath.Join(root, "config", discovery.TagsFileName)}}
	if _, err := s.SetTags(context.Background(), "group:shop", []string{"x"}, "t"); err == nil {
		t.Fatal("tags on a group app must be refused")
	}
	if _, err := s.SetTags(context.Background(), "easypanel:a_web", []string{"bad/tag"}, "t"); err == nil {
		t.Fatal("an invalid tag must be refused")
	}
	got, err := s.SetTags(context.Background(), "easypanel:a_web", []string{"Shop", " shop "}, "t")
	if err != nil || !slices.Equal(got, []string{"shop"}) {
		t.Fatalf("SetTags: %v %v", got, err)
	}
	if _, err := s.SetTags(context.Background(), "easypanel:b_db", []string{"shop"}, "t"); err != nil {
		t.Fatal(err)
	}
	tags, err := discovery.LoadTags(s.s.TagsPath)
	if err != nil || len(tags) != 2 {
		t.Fatalf("stored tags %v %v", tags, err)
	}

	inv := &discovery.Inventory{Docker: discovery.Host{Available: true}, Apps: []discovery.App{
		{ID: "easypanel:a_web", Name: "web", Kind: discovery.KindEasypanel, Project: "a", Services: []discovery.Service{{Name: "web", State: "running",
			Data: []discovery.Data{{Type: "volume", Name: "up", HostPath: "/v/up", BackupPath: "/v/up", Class: discovery.ClassPersistent}}}}},
		{ID: "easypanel:b_db", Name: "db", Kind: discovery.KindEasypanel, Project: "b", Services: []discovery.Service{{Name: "db", State: "running",
			Database: &discovery.Database{Engine: "postgres"}}}},
	}}
	inv.ApplyTags(tags)
	s.inv, s.invAt = inv, time.Now()
	p, err := s.ProposeAppPlan(context.Background(), "group:shop")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Paths, "/v/up") || !strings.Contains(p.HookCommand, "'group:shop'") || len(p.Databases) != 1 {
		t.Fatalf("group plan: paths %v hook %q dbs %v", p.Paths, p.HookCommand, p.Databases)
	}
}
