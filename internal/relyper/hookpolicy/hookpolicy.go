// Package hookpolicy is Relyper-owned: limits which shell commands plan and
// repository hooks may run.
//
// Upstream Backrest runs any command an administrator puts into a hook, so
// whoever can edit a plan can run commands inside the container. With
// RELYPER_HOOK_COMMANDS=dbdump only the database dump command that Relyper
// Backup writes into app plans (and the app export command of customer
// plans) is allowed; every other command hook fails
// before anything is executed. The default ("all") keeps upstream's
// behaviour.
package hookpolicy

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Modes of RELYPER_HOOK_COMMANDS.
const (
	ModeAll    = "all"
	ModeDBDump = "dbdump"
)

// dbdumpCommand is exactly what internal/relyper/infra writes into app
// plans: relyper-dbdump dump --dir '<dir>' '<app id>'.
var dbdumpCommand = regexp.MustCompile(`^(/[A-Za-z0-9_./-]+/)?relyper-dbdump dump --dir '/[A-Za-z0-9_./-]+' '[a-z_]+:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}'$`)

// appexportCommand is what internal/relyper/portal writes into a customer's
// Relyper Budget plan: relyper-appexport budget --dir '<dir>' '<user id>'.
var appexportCommand = regexp.MustCompile(`^(/[A-Za-z0-9_./-]+/)?relyper-appexport budget --dir '/[A-Za-z0-9_./-]+' '[A-Za-z0-9_-]{1,128}'$`)

// Mode returns the configured mode.
func Mode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RELYPER_HOOK_COMMANDS"))) {
	case "", ModeAll:
		return ModeAll
	default:
		// Anything else, including typos, is the strict mode.
		return ModeDBDump
	}
}

// Check returns an error when the rendered hook command must not run.
func Check(command string) error {
	if Mode() == ModeAll {
		return nil
	}
	if c := strings.TrimSpace(command); dbdumpCommand.MatchString(c) || appexportCommand.MatchString(c) {
		return nil
	}
	return fmt.Errorf("command hook refused: RELYPER_HOOK_COMMANDS=%s allows only the relyper-dbdump command of app plans and the relyper-appexport command of customer plans", ModeDBDump)
}
