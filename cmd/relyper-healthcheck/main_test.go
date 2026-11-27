package main

import "testing"

func TestProbeAddress(t *testing.T) {
	tests := map[string]string{
		"":               "127.0.0.1:9898",
		"0.0.0.0:9898":   "127.0.0.1:9898",
		":8080":          "127.0.0.1:8080",
		"127.0.0.1:7000": "127.0.0.1:7000",
		"[::]:9898":      "127.0.0.1:9898",
		"[::1]:9898":     "[::1]:9898",
		"9999":           "127.0.0.1:9999",
	}
	for in, want := range tests {
		if got := probeAddress(in); got != want {
			t.Errorf("probeAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
