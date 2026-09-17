package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The profiler will dump the heap, print every goroutine's stack, and block for
// thirty seconds collecting CPU samples for anyone who can reach it. These
// tests are about who can reach it.

func TestLoopbackOnly(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		// The form every Go example shows, and the one that would otherwise
		// bind every interface on the box.
		{":6060", "127.0.0.1:6060"},
		{"0.0.0.0:6060", "127.0.0.1:6060"},
		// Colons, but nothing usable to split. Must not be concatenated into a
		// nonsense address; falls back to the loopback default.
		{"::", defaultPprofAddr},
		{"garbage", defaultPprofAddr},
		{"*:6060", "127.0.0.1:6060"},
		// A bare port, with no colon to split on.
		{"6060", "127.0.0.1:6060"},
		// Already explicit: left alone.
		{"127.0.0.1:6060", "127.0.0.1:6060"},
		{"[::1]:6060", "[::1]:6060"},
	}

	for _, c := range cases {
		if got := loopbackOnly(c.in); got != c.want {
			t.Errorf("loopbackOnly(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPprofDisabledByDefault(t *testing.T) {
	t.Parallel()

	if srv := newPprofServer(""); srv != nil {
		t.Error("an empty address produced a server; profiling must be opt-in")
	}
	// runPprof must tolerate the nil that represents "disabled".
	if err := runPprof(context.Background(), nil); err != nil {
		t.Errorf("runPprof(nil) = %v, want nil", err)
	}
}

// The listener must not be reachable from another address on the machine, which
// is what makes it safe without a security-group rule.
func TestPprofBindsLoopbackOnly(t *testing.T) {
	t.Parallel()

	srv := newPprofServer(":0")
	if srv == nil {
		t.Fatal("no server built")
	}

	ln, err := net.Listen("tcp", loopbackOnly(srv.Addr))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	host, _, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("parse bound address: %v", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Errorf("bound to %q, which is not a loopback address", host)
	}
}

func TestPprofServesProfilesAndStopsWithItsContext(t *testing.T) {
	t.Parallel()

	srv := newPprofServer("127.0.0.1:0")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.Addr = ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runPprof(ctx, srv) }()

	base := "http://" + srv.Addr
	if !waitForServer(t, base+"/debug/pprof/") {
		t.Fatal("pprof did not start serving")
	}

	// The index, and one real profile, so this is not just asserting that a
	// mux exists.
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap?debug=1"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Errorf("GET %s returned an empty body", path)
		}
	}

	// Shutdown rides on the same context as the rest of the process, so a
	// SIGTERM during a deploy does not leave the profiler holding its port.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runPprof returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("pprof did not stop when its context was cancelled")
	}
}

// A profiler that cannot bind is a diagnostic that is unavailable, not a reason
// to stop serving buyers.
func TestPprofSurvivesAPortItCannotBind(t *testing.T) {
	t.Parallel()

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer blocker.Close()

	srv := newPprofServer(blocker.Addr().String())
	if err := runPprof(context.Background(), srv); err != nil {
		t.Errorf("a busy port returned %v; it must be logged and swallowed", err)
	}
}

// The profiler must never appear on the router that faces the internet. If it
// ever did, TestEveryRouteIsClassified would demand an access level for it —
// and the honest answer would be that there isn't a safe one.
func TestPprofIsNotOnThePublicRouter(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/profile"} {
		rec := h.do(t, http.MethodGet, path, "", "")

		// The storefront catch-all answers unknown GETs, so "not 200 with a
		// profile in it" is the assertion, not a specific status.
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "goroutine profile") {
			t.Errorf("%s served a profile from the public router", path)
		}
	}
}

func waitForServer(t *testing.T, url string) bool {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
