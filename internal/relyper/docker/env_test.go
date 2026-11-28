// Relyper-owned tests for the endpoint and token variables.
package docker

import "testing"

func TestEnvNames(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("RELYPER_GUARD_URL", "")
	t.Setenv("RELYPER_GUARD_TOKEN", "")
	t.Setenv("RELYPER_DOCKER_HOST", "tcp://old:2375")
	t.Setenv("RELYPER_DOCKER_TOKEN", "old-token")
	if EndpointFromEnv() != "tcp://old:2375" || tokenFromEnv() != "old-token" {
		t.Fatal("deprecated names are not read")
	}
	t.Setenv("RELYPER_GUARD_URL", "tcp://docker-guard:2375")
	t.Setenv("RELYPER_GUARD_TOKEN", "new-token")
	if EndpointFromEnv() != "tcp://docker-guard:2375" || tokenFromEnv() != "new-token" {
		t.Fatal("RELYPER_GUARD_* must win over the deprecated names")
	}
}
