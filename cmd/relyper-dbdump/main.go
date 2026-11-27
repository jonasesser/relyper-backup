// Command relyper-dbdump is Relyper-owned. Relyper Backup puts it into the
// "before backup" hook of app backup plans:
//
//	relyper-dbdump dump --dir /data/relyper/dumps compose:shop
//
// It finds the app's database containers through the Docker API, dumps every
// database with its own tool and writes the dumps and a manifest to
// <dir>/<app>. The backup then picks them up. A failed dump exits non-zero,
// so the hook fails the backup instead of saving a stale dump silently.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/dbdump"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "dump" {
		fmt.Fprintln(os.Stderr, "usage: relyper-dbdump dump --dir <dump directory> <app id>")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	dir := fs.String("dir", "", "dump directory")
	timeout := fs.Duration("timeout", 2*time.Hour, "maximum duration")
	_ = fs.Parse(os.Args[2:])
	if *dir == "" || fs.NArg() != 1 || !discovery.ValidAppID(fs.Arg(0)) {
		fmt.Fprintln(os.Stderr, "usage: relyper-dbdump dump --dir <dump directory> <app id>")
		os.Exit(2)
	}
	if err := run(*dir, fs.Arg(0), *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "relyper-dbdump:", err)
		os.Exit(1)
	}
}

func run(dir, appID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := docker.New(docker.EndpointFromEnv())
	if err != nil {
		return err
	}
	pm, err := discovery.PathMapFromEnv()
	if err != nil {
		return err
	}
	inv := discovery.Discover(ctx, c, discovery.Options{PathMap: pm, SelfID: discovery.SelfContainerID()})
	if !inv.Docker.Available {
		return fmt.Errorf("docker not available at %s: %s", inv.Docker.Endpoint, inv.Docker.Error)
	}
	app := inv.Find(appID)
	if app == nil {
		return fmt.Errorf("app %s not found on this Docker host", appID)
	}
	m, err := dbdump.DumpApp(ctx, c, app, dir, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Printf("%d database dump(s) of %s written to %s\n", len(m.Dumps), appID, dbdump.Dir(dir, appID))
	return nil
}
