// Command relyper-appexport is Relyper-owned. Relyper Backup puts it into the
// "before backup" hook of a customer's Relyper Budget plan:
//
//	relyper-appexport budget --dir /data/relyper/app-exports/acme/budget-<user> <user id>
//
// It fetches the export of that Relyper Budget user (all budgets the user
// owns, still end-to-end encrypted) through RELYPER_BUDGET_URL with
// RELYPER_BUDGET_TOKEN, checks every checksum and replaces
// <dir>/budget-export.tar only when the export is complete. A failed export
// exits non-zero, so the hook fails the backup instead of saving an old
// export silently.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/appexport"
)

const usage = "usage: relyper-appexport budget --dir <export directory> <user id>"

func main() {
	if len(os.Args) < 2 || os.Args[1] != appexport.AppBudget {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("budget", flag.ExitOnError)
	dir := fs.String("dir", "", "export directory")
	timeout := fs.Duration("timeout", 2*time.Hour, "maximum duration")
	_ = fs.Parse(os.Args[2:])
	if *dir == "" || fs.NArg() != 1 || !appexport.ValidUserID(fs.Arg(0)) {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(*dir, fs.Arg(0), *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "relyper-appexport:", err)
		os.Exit(1)
	}
}

func run(dir, user string, timeout time.Duration) error {
	cfg, err := appexport.BudgetConfigFromEnv(nil)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("RELYPER_BUDGET_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	m, err := (&appexport.Budget{Cfg: *cfg}).Export(ctx, user, dir)
	if err != nil {
		return err
	}
	docs := 0
	for _, b := range m.Budgets {
		docs += b.Documents
	}
	fmt.Printf("Relyper Budget export of user %s: %d budget(s), %d document(s), %d file(s)\n", user, len(m.Budgets), docs, len(m.Entries))
	return nil
}
