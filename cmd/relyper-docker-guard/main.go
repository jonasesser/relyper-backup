// Command relyper-docker-guard is Relyper-owned: the only process that
// holds the Docker socket in a Relyper Backup deployment. It lets Relyper
// Backup read the Docker host and run database dumps and restore tests, and
// refuses everything else. See internal/relyper/dockerguard and
// DEPLOYMENT.md.
//
//	relyper-docker-guard          serve (configured by environment)
//	relyper-docker-guard health   container health check
//
// Environment:
//
//	RELYPER_GUARD_LISTEN         listen address (default :2375)
//	RELYPER_GUARD_UPSTREAM       Docker endpoint (default unix:///var/run/docker.sock)
//	RELYPER_GUARD_TOKEN          bearer token Relyper Backup must send (RELYPER_DOCKER_TOKEN there)
//	RELYPER_GUARD_DUMPS          1: allow the fixed database dump scripts
//	RELYPER_GUARD_VERIFY         1: allow restore-test containers
//	RELYPER_GUARD_LIFECYCLE      1: allow stopping and restarting containers
//	RELYPER_GUARD_REQUIRE_LABEL  1: dumps only for containers labelled relyper.dump=true
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/dockerguard"
)

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func flag(name string) bool {
	switch strings.ToLower(env(name, "")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func main() {
	listen := env("RELYPER_GUARD_LISTEN", ":2375")
	if len(os.Args) > 1 && os.Args[1] == "health" {
		os.Exit(health(listen))
	}
	logger := log.New(os.Stdout, "relyper-docker-guard: ", log.LstdFlags|log.LUTC)
	cfg := dockerguard.Config{
		Upstream:     env("RELYPER_GUARD_UPSTREAM", "unix:///var/run/docker.sock"),
		Token:        env("RELYPER_GUARD_TOKEN", ""),
		Dumps:        flag("RELYPER_GUARD_DUMPS"),
		Verify:       flag("RELYPER_GUARD_VERIFY"),
		Lifecycle:    flag("RELYPER_GUARD_LIFECYCLE"),
		RequireLabel: flag("RELYPER_GUARD_REQUIRE_LABEL"),
		Log:          logger,
	}
	g, err := dockerguard.New(cfg)
	if err != nil {
		logger.Fatal(err)
	}
	if cfg.Token == "" {
		logger.Print("warning: RELYPER_GUARD_TOKEN is not set; every container on the guard's network can use it")
	}
	features := g.Features()
	if len(features) == 0 {
		features = []string{"read-only"}
	}
	logger.Printf("listening on %s, upstream %s, enabled: %s", listen, cfg.Upstream, strings.Join(features, ", "))
	srv := &http.Server{Addr: listen, Handler: g, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal(err)
	}
}

// health checks the guard's own endpoint on loopback.
func health(listen string) int {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+"/relyper/guard", nil)
	if t := env("RELYPER_GUARD_TOKEN", ""); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.StatusCode)
		return 1
	}
	return 0
}
