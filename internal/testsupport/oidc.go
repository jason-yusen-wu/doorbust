package testsupport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	jose "gopkg.in/go-jose/go-jose.v2"
)

// TestClientID is the audience every token minted here is issued for, and the
// one Verifier is configured to accept.
const TestClientID = "test-client-id"

// VendorGroup matches the app's COGNITO_VENDOR_GROUP default.
const VendorGroup = "vendors"

// Issuer is a stand-in for a Cognito user pool: it serves OIDC discovery and a
// JWKS, and mints tokens signed by the matching key.
//
// The point is that tests get to use the *real* auth.Verifier rather than
// bypassing it. go-oidc exposes oidc.ClientContext, which lets the verifier's
// discovery and JWKS fetches be pointed at this server with no production
// change — so signature, issuer, audience and expiry checks are all genuinely
// exercised. That path is security-critical and was otherwise untestable.
type Issuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
}

// TokenClaims are the parts of an ID token the app actually reads.
type TokenClaims struct {
	Subject string
	Email   string
	Groups  []string
}

// NewIssuer starts a fake issuer, shut down when the test finishes.
func NewIssuer(t *testing.T) *Issuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	iss := &Issuer{key: key, keyID: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The issuer value must equal the URL the verifier was given, or
		// go-oidc rejects the discovery document outright.
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                iss.server.URL,
			"jwks_uri":                              iss.server.URL + "/.well-known/jwks.json",
			"authorization_endpoint":                iss.server.URL + "/authorize",
			"token_endpoint":                        iss.server.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"subject_types_supported":               []string{"public"},
			"response_types_supported":              []string{"code"},
		})
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{{
				Key:       key.Public(),
				KeyID:     iss.keyID,
				Algorithm: string(jose.RS256),
				Use:       "sig",
			}},
		})
	})

	iss.server = httptest.NewServer(mux)
	t.Cleanup(iss.server.Close)

	return iss
}

// URL is the issuer identifier, suitable as COGNITO_ISSUER_URL.
func (i *Issuer) URL() string { return i.server.URL }

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
	return i.sign(t, i.claims(c, i.URL(), TestClientID, time.Now().Add(time.Hour)))
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
	return i.sign(t, i.claims(c, i.URL(), TestClientID, time.Now().Add(-time.Minute)))
}

// WrongAudienceToken is issued for a different app client — the check that
// stops a token minted for another application being replayed at this one.
func (i *Issuer) WrongAudienceToken(t *testing.T, c TokenClaims) string {
	t.Helper()
	return i.sign(t, i.claims(c, i.URL(), "some-other-client", time.Now().Add(time.Hour)))
}

// WrongIssuerToken claims to come from a different pool.
func (i *Issuer) WrongIssuerToken(t *testing.T, c TokenClaims) string {
	t.Helper()
	return i.sign(t, i.claims(c, "https://evil.example.test", TestClientID, time.Now().Add(time.Hour)))
}

// BadSignatureToken is signed by a key the JWKS does not advertise, standing
// in for a forged token.
func (i *Issuer) BadSignatureToken(t *testing.T, c TokenClaims) string {
	t.Helper()

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rogue key: %v", err)
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: other},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.keyID),
	)
	if err != nil {
		t.Fatalf("build rogue signer: %v", err)
	}

	return serialize(t, signer, i.claims(c, i.URL(), TestClientID, time.Now().Add(time.Hour)))
}

func (i *Issuer) claims(c TokenClaims, issuer, audience string, expiry time.Time) map[string]any {
	claims := map[string]any{
		"iss":            issuer,
		"aud":            audience,
		"sub":            c.Subject,
		"email":          c.Email,
		"email_verified": true,
		"token_use":      "id",
		"iat":            time.Now().Add(-time.Minute).Unix(),
		"exp":            expiry.Unix(),
	}
	// Cognito omits the claim entirely for a user in no groups; mirroring
	// that matters, because "absent" and "empty array" unmarshal the same but
	// only one of them is what production sends.
	if len(c.Groups) > 0 {
		claims["cognito:groups"] = c.Groups
	}
	return claims
}

func (i *Issuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.keyID),
	)
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}

	return serialize(t, signer, claims)
}

func serialize(t *testing.T, signer jose.Signer, claims map[string]any) string {
	t.Helper()

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	compact, err := signed.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize token: %v", err)
	}

	return compact
}
