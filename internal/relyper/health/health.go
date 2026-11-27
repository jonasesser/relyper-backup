// Package health provides the unauthenticated liveness endpoint used by
// container health checks.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package health

import (
	"net/http"
)

// Path is the route the liveness endpoint is served on.
const Path = "/healthz"

// Handler reports that the HTTP server is up and serving requests.
//
// It deliberately returns no version, configuration or backup state: the
// endpoint is reachable without authentication, so it must not disclose
// anything beyond "the process answers HTTP".
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
		}
	})
}
