// Command relyper-healthcheck is the container HEALTHCHECK probe for
// Relyper Backup. It requests the /healthz endpoint of the local server and
// exits 0 when it answers 200, 1 otherwise.
//
// The address is derived from BACKREST_PORT (the same variable the server
// reads), so the probe follows a changed web port without extra
// configuration. It always connects to the loopback interface.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultPort = "9898"

func main() {
	url := "http://" + probeAddress(os.Getenv("BACKREST_PORT")) + "/healthz"
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health check failed: %s returned %s\n", url, resp.Status)
		os.Exit(1)
	}
}

// probeAddress turns a bind address such as "0.0.0.0:9898", ":9898",
// "[::]:9898", "127.0.0.1:8080" or "9898" into a loopback host:port to probe.
func probeAddress(bind string) string {
	bind = strings.TrimSpace(bind)
	port := defaultPort
	host := "127.0.0.1"
	if bind != "" {
		if h, p, err := net.SplitHostPort(bind); err == nil {
			port = p
			if ip := net.ParseIP(h); ip != nil && ip.To4() == nil && !ip.IsUnspecified() {
				host = "[::1]"
			} else if h == "::" {
				host = "127.0.0.1"
			}
		} else if !strings.Contains(bind, ":") {
			port = bind
		}
	}
	if port == "" {
		port = defaultPort
	}
	return host + ":" + port
}
