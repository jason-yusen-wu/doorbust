package loadgen

import (
	"net"
	"net/http"
	"syscall"
	"time"
)

// NewClient builds the HTTP client the generator uses.
//
// Never http.DefaultClient. Two of its defaults would silently make the
// generator the thing being measured:
//
//   - MaxIdleConnsPerHost is 2. With more in-flight requests than that, every
//     extra request opens a fresh connection and closes it on completion, so a
//     run at a few thousand rps churns through ephemeral ports and piles up
//     TIME_WAIT sockets until connections start failing. The failures look like
//     the server refusing load; they are the generator running out of ports.
//   - MaxConnsPerHost, if set, makes the client *block* waiting for a free
//     connection. That is closed-loop queueing smuggled in below the HTTP
//     layer, which is exactly what this package exists to avoid, so it is
//     explicitly left unlimited.
func NewClient(maxInFlight int, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: maxInFlight,
			MaxIdleConns:        maxInFlight,
			MaxConnsPerHost:     0,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
			// The server is plaintext HTTP/1.1; attempting HTTP/2 buys nothing
			// and multiplexes requests onto one connection, which changes the
			// concurrency being offered.
			ForceAttemptHTTP2: false,
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}
}

// RaiseFileLimit lifts RLIMIT_NOFILE to its hard limit and reports the result.
//
// macOS ships a soft limit of 256 open files. A run needing more sockets than
// that fails with EMFILE partway through, which surfaces as a burst of
// connection errors that look like the server dying. Better to raise it, and
// better still to refuse the run when even the hard limit is too low, than to
// publish a number produced by a generator that was quietly falling over.
func RaiseFileLimit() (soft, hard uint64, err error) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, 0, err
	}
	hard = uint64(lim.Max)

	lim.Cur = lim.Max
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		// Not fatal on its own — the caller decides whether the limit it got is
		// enough for the run it was asked for.
		var cur syscall.Rlimit
		_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &cur)
		return uint64(cur.Cur), hard, nil
	}

	var now syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &now); err != nil {
		return 0, hard, err
	}
	return uint64(now.Cur), hard, nil
}
