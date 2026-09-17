// Package workload prepares everything a run needs before load starts: a fake
// Cognito pool, valid ID tokens, seeded inventory, and the SKU-choosing policy
// that defines the shape of the run.
package workload

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/jason-yusen-wu/doorbust/internal/testsupport/fakeoidc"
)

// Audience is the client id the benchmarked app is configured to accept.
const Audience = "bench-client-id"

// Issuer is a fake Cognito pool bound to a real port.
//
// It is the same fakeoidc the test suite uses, which is the point: the tokens a
// benchmark sends are verified by exactly the code path the tests cover, rather
// than by some benchmark-only shortcut that could drift from it. The app needs
// no change to accept them — auth.NewVerifier discovers whatever issuer URL it
// is given, and go-oidc imposes no scheme restriction, so COGNITO_ISSUER_URL
// pointed at http://127.0.0.1:<port> is the entire hook.
type Issuer struct {
	inner *fakeoidc.Issuer
	srv   *http.Server
	ln    net.Listener
}

// StartIssuer binds a loopback port and serves discovery and the JWKS.
//
// The listener is bound before serving starts so the issuer URL is known before
// any request can arrive. Doing it the other way round leaves a window in which
// discovery answers with an empty "issuer", which go-oidc rejects — a race that
// would show up as an occasional failed run rather than a clear error.
func StartIssuer() (*Issuer, error) {
	inner, err := fakeoidc.New(Audience)
	if err != nil {
		return nil, fmt.Errorf("build issuer: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind issuer: %w", err)
	}
	inner.SetURL("http://" + ln.Addr().String())

	srv := &http.Server{Handler: inner.Handler()}
	go srv.Serve(ln) //nolint:errcheck // Serve always returns on Close.

	return &Issuer{inner: inner, srv: srv, ln: ln}, nil
}

func (i *Issuer) URL() string { return i.inner.URL() }

func (i *Issuer) Close() error { return i.srv.Close() }

// Buyer is one simulated customer: the identity, and the token that proves it.
type Buyer struct {
	Subject string
	Email   string
	Token   string
}

// MintBuyers pre-mints the whole token pool.
//
// Minting is an RSA-2048 signature, on the order of a millisecond. Doing it per
// request would put a CPU-bound operation in the generator's hot loop and
// measure the benchmark rather than the server, so every token a run will use
// is made up front, with an expiry comfortably longer than any run.
func MintBuyers(iss *Issuer, n int, ttl time.Duration) ([]Buyer, error) {
	buyers := make([]Buyer, n)
	for i := range buyers {
		b := Buyer{
			Subject: fmt.Sprintf("sub-bench-%d", i),
			Email:   fmt.Sprintf("bench-%d@example.test", i),
		}

		token, err := iss.inner.Mint(
			fakeoidc.Claims{Subject: b.Subject, Email: b.Email},
			fakeoidc.MintOptions{Expiry: time.Now().Add(ttl)},
		)
		if err != nil {
			return nil, fmt.Errorf("mint token %d: %w", i, err)
		}
		b.Token = token
		buyers[i] = b
	}
	return buyers, nil
}
