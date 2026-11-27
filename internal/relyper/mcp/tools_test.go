// Relyper-owned: the tools named in the product concept are registered with
// the right scopes.
package mcp

import (
	"testing"

	"github.com/garethgeorge/backrest/internal/relyper/infra"
	"github.com/garethgeorge/backrest/internal/relyper/mcpauth"
)

func TestConceptToolsRegistered(t *testing.T) {
	s := &Server{d: Deps{Infra: &infra.Service{}}}
	byName := map[string]ToolInfo{}
	for _, ti := range s.Tools() {
		if _, dup := byName[ti.Name]; dup {
			t.Errorf("tool %s registered twice", ti.Name)
		}
		byName[ti.Name] = ti
	}
	concept := []string{
		"get_system_overview", "get_backup_status", "list_repositories", "get_repository_status", "list_backup_plans", "get_backup_plan",
		"list_snapshots", "get_snapshot_details", "list_snapshot_files", "list_backup_operations", "get_backup_operation", "get_backup_errors",
		"get_storage_usage", "list_docker_containers", "list_docker_services", "list_docker_volumes", "list_easypanel_projects",
		"list_detected_databases", "get_backup_coverage", "get_disaster_recovery_status",
		"test_repository_connection", "create_repository", "update_repository", "create_backup_plan", "update_backup_plan",
		"enable_backup_plan", "disable_backup_plan", "run_backup", "run_database_backup", "run_repository_check",
		"cancel_backup_operation", "create_backup_recommendations", "validate_backup_configuration", "run_restore_test",
		"restore_snapshot", "overwrite_existing_files", "delete_snapshot", "delete_repository", "prune_repository", "modify_retention_policy",
		"stop_container", "restart_container",
	}
	for _, n := range concept {
		if _, ok := byName[n]; !ok {
			t.Errorf("concept tool %s missing", n)
		}
	}
	for _, n := range []string{"restore_snapshot", "overwrite_existing_files", "delete_snapshot", "delete_repository", "prune_repository", "modify_retention_policy", "stop_container", "restart_container"} {
		if !byName[n].Restricted {
			t.Errorf("%s must need an approval", n)
		}
	}
	if byName["stop_container"].Scope != mcpauth.ScopeInfraManage {
		t.Errorf("stop_container scope %s", byName["stop_container"].Scope)
	}
}

func TestProtectedTarget(t *testing.T) {
	t.Setenv("BACKREST_CONFIG", "/config/config.json")
	t.Setenv("BACKREST_DATA", "/data")
	t.Setenv("XDG_CACHE_HOME", "/cache")
	for dir, want := range map[string]bool{"/": true, "/config": true, "/data/x": true, "/etc": true, "/usr/local": true, "/srv/shop/uploads": false, "/restore/a": false} {
		if got := protectedTarget(dir); got != want {
			t.Errorf("protectedTarget(%s) = %v", dir, got)
		}
	}
}
