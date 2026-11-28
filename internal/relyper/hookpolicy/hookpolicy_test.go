// Relyper-owned tests of the hook command policy.
package hookpolicy

import "testing"

func TestCheck(t *testing.T) {
	good := []string{
		"relyper-dbdump dump --dir '/data/relyper/dumps' 'compose:shop'",
		"/usr/local/bin/relyper-dbdump dump --dir '/data/relyper/dumps' 'easypanel:blog_db'",
		"relyper-appexport budget --dir '/data/relyper/app-exports/acme/budget-1b2c' '1b2c-3d4e_5'",
	}
	bad := []string{
		"id",
		"relyper-dbdump dump --dir '/data' 'compose:shop'; id",
		"relyper-dbdump dump --dir '/data' 'compose:shop'\nid",
		"relyper-dbdump dump --dir '/data' 'compose:$(id)'",
		"x;relyper-dbdump dump --dir '/data' 'compose:shop'",
		"#!/bin/sh\nrelyper-dbdump dump --dir '/data' 'compose:shop'",
		"relyper-dbdump dump --dir '/da'ta' 'compose:shop'",
		"relyper-appexport budget --dir '/data' 'u1'; id",
		"relyper-appexport budget --dir '/data' 'u$(id)'",
		"relyper-appexport other --dir '/data' 'u1'",
	}
	t.Setenv("RELYPER_HOOK_COMMANDS", "")
	for _, c := range bad {
		if Check(c) != nil {
			t.Fatalf("default mode refused %q", c)
		}
	}
	t.Setenv("RELYPER_HOOK_COMMANDS", "dbdump")
	for _, c := range good {
		if err := Check(c); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
	for _, c := range bad {
		if Check(c) == nil {
			t.Errorf("%q allowed", c)
		}
	}
	t.Setenv("RELYPER_HOOK_COMMANDS", "dbdmup") // typo: strict
	if Check("id") == nil {
		t.Error("typo in the mode must not allow everything")
	}
}
