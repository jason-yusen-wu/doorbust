package testsupport

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport/fakeoidc"
)

// TestClientID is the audience every token minted here is issued for, and the
// one Verifier is configured to accept.
const TestClientID = "test-client-id"

// VendorGroup matches the app's COGNITO_VENDOR_GROUP default.
const VendorGroup = "vendors"

// Issuer is a *testing.T-flavoured wrapper around fakeoidc.Issuer: it binds the
// fake Cognito pool to an httptest server and turns every error into t.Fatalf,
// so tests read as assertions rather than as error handling.
//
// The crypto and the two documents live in internal/testsupport/fakeoidc, which
// does not import "testing", so the load generator in bench/ can run the very
// same issuer as a standalone process. One implementation means the tokens a
// benchmark uses are verified by exactly the path these tests cover.
//
// The point of all of it is that tests use the *real* auth.Verifier rather than
// bypassing it. go-oidc exposes oidc.ClientContext, which lets the verifier's
// discovery and JWKS fetches be pointed here with no production change — so
// signature, issuer, audience and expiry checks are all genuinely exercised.
// That path is security-critical and was otherwise untestable.
type Issuer struct {
	inner  *fakeoidc.Issuer
	server *httptest.Server
}

// TokenClaims are the parts of an ID token the app actually reads.
//
// An alias rather than a distinct type: every existing
// testsupport.TokenClaims{...} literal keeps compiling, and a value passes
// straight into fakeoidc.Mint with no conversion.
type TokenClaims = fakeoidc.Claims

// NewIssuer starts a fake issuer, shut down when the test finishes.
func NewIssuer(t *testing.T) *Issuer {
	t.Helper()

	inner, err := fakeoidc.New(TestClientID)
	if err != nil {
		t.Fatalf("build fake issuer: %v", err)
	}

	// Unstarted, because the discovery document has to echo back the exact URL
	// the verifier was given and that URL is not known until a port is bound.
	// NewUnstartedServer binds the listener immediately but serves nothing, so
	// the address is readable before any request can arrive — closing the
	// window in which discovery would have answered with an empty issuer.
	// Start() computes s.URL from this same address.
	srv := httptest.NewUnstartedServer(inner.Handler())
	inner.SetURL("http://" + srv.Listener.Addr().String())
	srv.Start()
	t.Cleanup(srv.Close)

	return &Issuer{inner: inner, server: srv}
}

// URL is the issuer identifier, suitable as COGNITO_ISSUER_URL.
func (i *Issuer) URL() string { return i.inner.URL() }

// Context returns a context carrying an HTTP client that can reach this
// issuer. auth.NewVerifier must be called with it.
func (i *Issuer) Context() context.Context {
	return oidc.ClientContext(context.Background(), i.server.Client())
}

// Verifier builds the real auth.Verifier against this issuer.
func (i *Issuer) Verifier(t *testing.T) *auth.Verifier {
	t.Helper()

	verifier, err := auth.NewVerifier(i.Context(), i.URL(), TestClientID)
	if err != nil {
		t.Fatalf("build verifier against test issuer: %v", err)
	}
	return verifier
}

// Token mints a valid ID token for the given identity.
func (i *Issuer) Token(t *testing.T, c TokenClaims) string {
	t.Helper()
	return i.mint(t, c, fakeoidc.MintOptions{})
}

// Buyer is an ordinary authenticated caller — in no groups, which is how
// Cognito represents it: the claim is absent entirely, not an empty array.
func (i *Issuer) Buyer(t *testing.T, n int) (string, TokenClaims) {
	t.Helper()

	c := TokenClaims{
		Subject: fmt.Sprintf("sub-buyer-%d-%d", n, time.Now().UnixNano()),
		Email:   fmt.Sprintf("buyer-%d-%d@example.test", n, time.Now().UnixNano()),
	}
	return i.Token(t, c), c
}

// Vendor is a caller in the vendors group.
func (i *Issuer) Vendor(t *testing.T, n int) (string, TokenClaims) {
	t.Helper()

	c := TokenClaims{
		Subject: fmt.Sprintf("sub-vendor-%d-%d", n, time.Now().UnixNano()),
		Email:   fmt.Sprintf("vendor-%d-%d@example.test", n, time.Now().UnixNano()),
		Groups:  []string{VendorGroup},
	}
	return i.Token(t, c), c
}

// ExpiredToken is correctly signed but past its expiry.
func (i *Issuer) ExpiredToken(t *testing.T, c TokenClaims) string {
	t.Helper()
	return i.mint(t, c, fakeoidc.MintOptions{Expiry: time.Now().Add(-time.Minute)})
}

// WrongAudienceToken is issued for a different app client — the check that
// stops a token minted for another application being replayed at this one.
func (i *Issuer) WrongAudienceToken(t *testing.T, c TokenClaims) string {
	t.Helper()
	return i.mint(t, c, fakeoidc.MintOptions{Audience: "some-other-client"})
}

// WrongIssuerToken claims to come from a different pool.
func (i *Issuer) WrongIssuerToken(t *testing.T, c TokenClaims) string {
	t.Helper()
	return i.mint(t, c, fakeoidc.MintOptions{Issuer: "https://evil.example.test"})
}

// BadSignatureToken is signed by a key the JWKS does not advertise, standing
// in for a forged token.
func (i *Issuer) BadSignatureToken(t *testing.T, c TokenClaims) string {
	t.Helper()
	return i.mint(t, c, fakeoidc.MintOptions{RogueKey: true})
}

// mint is the one place minting can fail, so it is the one place that needs a
// *testing.T.
func (i *Issuer) mint(t *testing.T, c TokenClaims, opts fakeoidc.MintOptions) string {
	t.Helper()

	token, err := i.inner.Mint(c, opts)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return token
}
