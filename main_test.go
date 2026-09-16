package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthcheck covers the container HEALTHCHECK probe. The runtime image is
// FROM scratch, so this subcommand is the only way the health endpoint is ever
// queried from inside the container - a regression here shows up as a
// permanently unhealthy container, not as a failed build.
func TestHealthcheck(t *testing.T) {
	t.Parallel()

	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"managed":3}`))
	}))
	t.Cleanup(ready.Close)

	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(notReady.Close)

	// httptest listens on 127.0.0.1, so the address is reusable as HEALTH_ADDR.
	readyAddr := strings.TrimPrefix(ready.URL, "http://")
	notReadyAddr := strings.TrimPrefix(notReady.URL, "http://")

	tests := []struct {
		name string
		addr string
		want int
	}{
		// Disabled health endpoint must not fail the container.
		{name: "unset addr is a no-op success", addr: "", want: 0},
		{name: "readyz 200", addr: readyAddr, want: 0},
		{name: "readyz 503", addr: notReadyAddr, want: 1},
		{name: "missing port", addr: "garbage", want: 1},
		// A wildcard bind address is not dialable; it must be rewritten to
		// loopback before the request goes out.
		{name: "wildcard host is dialed on loopback", addr: ":" + portOf(t, readyAddr), want: 0},
		{name: "nothing listening", addr: "127.0.0.1:1", want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := healthcheck(tc.addr); got != tc.want {
				t.Errorf("healthcheck(%q) = %d, want %d", tc.addr, got, tc.want)
			}
		})
	}
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		t.Fatalf("no port in %q", addr)
	}
	return addr[i+1:]
}
