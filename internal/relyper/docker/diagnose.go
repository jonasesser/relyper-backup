// Relyper-owned: plain-language explanation of a failed Docker connection.

package docker

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// Diagnose explains why the Docker endpoint could not be reached and what
// to change. It returns "" when it has nothing better than the raw error.
func Diagnose(endpoint string, err error) string {
	if err == nil {
		return ""
	}
	host := endpoint
	if u, perr := url.Parse(endpoint); perr == nil && u.Host != "" {
		host = u.Host
	}
	name := host
	if h, _, serr := net.SplitHostPort(host); serr == nil {
		name = h
	}
	msg := err.Error()
	var api *APIError
	if errors.As(err, &api) {
		switch {
		case api.Status == 401:
			return "The Docker guard rejected the token. RELYPER_GUARD_TOKEN must have exactly the same value on this service and on the docker-guard service (no quotes, no spaces)."
		case api.Status == 502 || strings.Contains(api.Message, "docker is not reachable"):
			return "The Docker guard is running but cannot reach Docker. Add the bind mount /var/run/docker.sock → /var/run/docker.sock to the docker-guard service (not to this one) and redeploy it."
		case api.Status == 403:
			return "The endpoint refused the request. If it is the Docker guard, update the guard to the same version as Relyper Backup."
		}
		return ""
	}
	var dnsErr *net.DNSError
	switch {
	case strings.HasPrefix(endpoint, "unix://") && (strings.Contains(msg, "no such file") || strings.Contains(msg, "connect: permission denied")):
		return "No Docker guard is configured in this container: RELYPER_GUARD_URL is not set, so it tried the local Docker socket, which is (correctly) not mounted. Set RELYPER_GUARD_URL=tcp://<project>_docker-guard:2375 and RELYPER_GUARD_TOKEN on this service and redeploy it."
	case errors.As(err, &dnsErr) || strings.Contains(msg, "no such host") || strings.Contains(msg, "server misbehaving"):
		return "The host name " + name + " is unknown here. Check the guard's service name: on Easypanel it is <project>_<service>, e.g. myproject_docker-guard, and both services must be in the same project. The docker-guard service must be deployed and running."
	case strings.Contains(msg, "connection refused"):
		return "The host " + name + " exists but nothing answers on that port. Check that the docker-guard service is running (its log shows \"listening on :2375\") and that the port in RELYPER_GUARD_URL is 2375."
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "No answer from " + name + ". The services are probably in different networks; put both into the same Easypanel project."
	}
	return ""
}
