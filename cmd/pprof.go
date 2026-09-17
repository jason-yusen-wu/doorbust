package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// pprofServer exposes Go's runtime profiler on its own listener.
//
// A SEPARATE server, not a route group on the main router, and this is the
// whole design. net/http/pprof registers itself on http.DefaultServeMux as an
// import side effect, and its handlers will happily dump the heap, block for
// thirty seconds collecting a CPU profile, or print every goroutine's stack —
// to anyone who can reach them. On the main router that would be one
// misconfigured middleware away from being public, and the route-classification
// test would have to carve out an exception for it.
//
// Binding to 127.0.0.1 means the kernel refuses connections from anywhere else,
// so it is unreachable regardless of what the security group says. Reaching it
// on the deployed box is an SSM port-forward, which needs no inbound rule and
// no security-group change:
//
//	aws ssm start-session --target <instance-id> \
//	  --document-name AWS-StartPortForwardingSession \
//	  --parameters '{"portNumber":["6060"],"localPortNumber":["6060"]}'
//	go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
//
// Off unless PPROF_ADDR is set. Profiling endpoints are not free — a CPU
// profile perturbs the process it measures, which matters when the point of
// this project is a benchmark — so the process that serves buyers does not run
// one by default.
func newPprofServer(addr string) *http.Server {
	if addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	// Registered explicitly rather than by importing for the side effect on
	// DefaultServeMux. The blank-import idiom relies on a global that anything
	// else in the process could also be writing to, and it makes this listener
	// impossible to keep separate from the main one.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return &http.Server{
		Addr:    addr,
		Handler: mux,
		// No WriteTimeout. A CPU profile is a long, deliberately slow response
		// — `?seconds=30` holds the connection open for thirty seconds — and a
		// write timeout would truncate exactly the profile someone asked for.
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// runPprof serves the profiler until the context is cancelled.
//
// A failure here must never take the application down with it: the profiler is
// a diagnostic, and a port already in use is not a reason to stop selling
// things. Errors are logged and swallowed.
func runPprof(ctx context.Context, srv *http.Server) error {
	if srv == nil {
		return nil
	}

	// Bound explicitly to loopback. "localhost" can resolve to both ::1 and
	// 127.0.0.1, and an addr of ":6060" would bind every interface — which is
	// the one outcome this listener exists to prevent.
	ln, err := net.Listen("tcp", loopbackOnly(srv.Addr))
	if err != nil {
		slog.Error("pprof listener unavailable; continuing without it",
			"addr", srv.Addr, "error", err)
		return nil
	}

	slog.Info("pprof listening", "addr", ln.Addr().String())

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("pprof server stopped", "error", err)
	}
	return nil
}

// loopbackOnly forces a bare port or a wildcard host onto 127.0.0.1.
//
// Configuration should not be able to expose the profiler by accident. Someone
// setting PPROF_ADDR=:6060, which is what every Go example shows, would
// otherwise bind all interfaces.
func loopbackOnly(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not a host:port pair. A bare port ("6060") is the useful case and
		// becomes a loopback address; anything else — "::" being the one that
		// turned up in testing, since it has colons but splits into nothing
		// usable — falls back to the default rather than being concatenated
		// into a nonsense address like "127.0.0.1:::".
		if isPort(addr) {
			return "127.0.0.1:" + addr
		}
		return defaultPprofAddr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "*" {
		return "127.0.0.1:" + port
	}
	return addr
}

// defaultPprofAddr is where an unparseable configured address lands. Choosing a
// loopback default rather than erroring keeps a typo from taking the process
// down, and keeps it from opening the profiler to the network either.
const defaultPprofAddr = "127.0.0.1:6060"

func isPort(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
