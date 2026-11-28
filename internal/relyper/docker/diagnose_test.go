// Relyper-owned tests for Diagnose.
package docker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiagnose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	check := func(endpoint, want string) {
		t.Helper()
		c, err := New(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Ping(ctx)
		if h := Diagnose(endpoint, err); !strings.Contains(h, want) {
			t.Errorf("%s: %v -> %q, want %q", endpoint, err, h, want)
		}
	}
	check("unix:///nonexistent/docker.sock", "RELYPER_GUARD_URL is not set")
	check("tcp://relyper-no-such-host.invalid:2375", "is unknown here")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	check("tcp://"+addr, "nothing answers")
	for status, want := range map[int]string{401: "rejected the token", 502: "cannot reach Docker"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"message":"x"}`)
		}))
		check("tcp://"+strings.TrimPrefix(srv.URL, "http://"), want)
		srv.Close()
	}
}
