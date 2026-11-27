// Package mcpauth holds the credentials MCP clients use: personal access
// tokens created in the web UI and grants issued by the built-in OAuth 2.1
// authorization server. Secrets are random, shown once and stored only as
// SHA-256 hashes.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package mcpauth

import (
	"fmt"
	"slices"
	"strings"
)

// Scopes limit what an MCP client may do. A token carries a subset.
const (
	ScopeStatusRead     = "status:read"     // status, configuration, operations, snapshots
	ScopeContentsBrowse = "contents:browse" // file names inside snapshots
	ScopeReposManage    = "repos:manage"    // create/update repositories, SFTP setup
	ScopePlansManage    = "plans:manage"    // create/update/enable/disable plans and schedules
	ScopeBackupsRun     = "backups:run"     // start backups and checks, cancel operations
	ScopeRestore        = "restore"         // restore snapshots (each call needs approval)
	ScopeDataDelete     = "data:delete"     // forget, prune, retention, removal (each call needs approval)
	ScopeInfraManage    = "infra:manage"    // stop or restart app containers (each call needs approval)
)

// ScopeInfo describes a scope for the UI and the OAuth consent page.
type ScopeInfo struct {
	Name        string
	Description string
	// Restricted scopes only allow requesting an operation; every single
	// call still has to be approved by a signed-in user.
	Restricted bool
}

// AllScopes lists every scope in display order.
var AllScopes = []ScopeInfo{
	{ScopeStatusRead, "Read status, configuration (without secrets), operations and snapshots", false},
	{ScopeContentsBrowse, "List file names inside snapshots", false},
	{ScopeReposManage, "Create and change repositories, set up SFTP keys and test connections", false},
	{ScopePlansManage, "Create and change backup plans, schedules and paths", false},
	{ScopeBackupsRun, "Start backups, repository checks and statistics; cancel operations", false},
	{ScopeRestore, "Request restores (every restore needs your approval)", true},
	{ScopeDataDelete, "Request deleting snapshots, pruning, retention changes and removing repositories or plans (every request needs your approval)", true},
	{ScopeInfraManage, "Request stopping or restarting app containers (every request needs your approval)", true},
}

// DefaultScopes is used when an OAuth client does not ask for scopes.
var DefaultScopes = []string{ScopeStatusRead}

// ScopeNames returns the names of AllScopes.
func ScopeNames() []string {
	out := make([]string, 0, len(AllScopes))
	for _, s := range AllScopes {
		out = append(out, s.Name)
	}
	return out
}

// NormalizeScopes validates, de-duplicates and orders scopes. Unknown scopes
// are an error.
func NormalizeScopes(scopes []string) ([]string, error) {
	known := ScopeNames()
	var out []string
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !slices.Contains(known, s) {
			return nil, fmt.Errorf("unknown scope %q", s)
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(known, a) - slices.Index(known, b) })
	return out, nil
}

// HasScope reports whether scopes contains want.
func HasScope(scopes []string, want string) bool {
	return slices.Contains(scopes, want)
}
