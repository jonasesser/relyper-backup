// Relyper-owned: keeps the Docker guard buildable from its Dockerfile.
package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportsOnlyRelyperPackages fails when the guard (or a package it
// imports) starts to depend on code outside internal/relyper:
// Dockerfile.docker-guard copies only cmd/relyper-docker-guard and
// internal/relyper, so such an import breaks the guard image build.
func TestImportsOnlyRelyperPackages(t *testing.T) {
	const mod = "github.com/garethgeorge/backrest/"
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if !strings.HasPrefix(p, mod) {
			continue
		}
		rel := strings.TrimPrefix(p, mod)
		if rel != "cmd/relyper-docker-guard" && rel != "internal/relyper" && !strings.HasPrefix(rel, "internal/relyper/") {
			t.Errorf("the Docker guard imports %s, which Dockerfile.docker-guard does not copy", p)
		}
	}
}
