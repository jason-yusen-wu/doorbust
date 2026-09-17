// Package fakeoidc is a stand-in for a Cognito user pool: it serves OIDC
// discovery and a JWKS, and mints RS256 tokens signed by the matching key.
//
// It exists so that callers get to use the *real* auth.Verifier rather than
// bypassing it. Signature, issuer, audience and expiry checks are all genuinely
// exercised against it, with no production change: auth.NewVerifier discovers
// whatever issuer URL it is handed.
//
// This package deliberately does not import "testing". The test harness in
// internal/testsupport wraps it for *testing.T ergonomics, and the load-test
// binary in bench/ runs it as a standalone process — one implementation, so the
// tokens a benchmark uses are verified by exactly the path the tests cover.
package fakeoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	jose "gopkg.in/go-jose/go-jose.v2"
)

// DiscoveryPath and JWKSPath are where the two documents are served.
const (
	DiscoveryPath = "/.well-known/openid-configuration"
	JWKSPath      = "/.well-known/jwks.json"
)

// Claims are the parts of an ID token the app actually reads.
type Claims struct {
	Subject string
	Email   string
	Groups  []string
}

// MintOptions override the defaults for a single token. The zero value mints an
// ordinary valid token, which is what almost every caller wants; the overrides
// exist so the negative auth cases (expired, wrong audience, wrong issuer,
// forged signature) are minted by the same code path as the positive one.
type MintOptions struct {
	// Issuer overrides the "iss" claim. Empty means this issuer's own URL.
	Issuer string
	// Audience overrides the "aud" claim. Empty means the default audience.
	Audience string
	// Expiry overrides "exp". Zero means one hour from now.
	Expiry time.Time
	// RogueKey signs with a key the JWKS does not advertise, standing in for a
	// forged token. The advertised key id is still sent, so the verifier gets
	// as far as checking the signature.
	RogueKey bool
}

// Issuer serves the two documents and mints tokens against them.
type Issuer struct {
	key      *rsa.PrivateKey
	keyID    string
	audience string
	handler  http.Handler

	// The discovery document must echo back the exact URL the verifier was
	// given, or go-oidc rejects it outright. That URL is not known until
	// something binds a port, so it is set after construction and read inside
	// the handlers.
	mu  sync.RWMutex
	url string
}

// New generates a signing key and builds the handler. The issuer URL must still
// be set with SetURL before the discovery document is fetched.
func New(defaultAudience string) (*Issuer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}

	i := &Issuer{key: key, keyID: "test-key-1", audience: defaultAudience}

	mux := http.NewServeMux()
	mux.HandleFunc(DiscoveryPath, i.serveDiscovery)
	mux.HandleFunc(JWKSPath, i.serveJWKS)
	i.handler = mux

	return i, nil
}

// SetURL records the URL this issuer is reachable at, which is also the value
// of its "iss" claim.
func (i *Issuer) SetURL(u string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.url = u
}

// URL is the issuer identifier, suitable as COGNITO_ISSUER_URL.
func (i *Issuer) URL() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.url
}

// Audience is the default "aud" claim, i.e. the COGNITO_CLIENT_ID to configure.
func (i *Issuer) Audience() string { return i.audience }

// Handler serves discovery and the JWKS.
func (i *Issuer) Handler() http.Handler { return i.handler }

func (i *Issuer) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	url := i.URL()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                url,
		"jwks_uri":                              url + JWKSPath,
		"authorization_endpoint":                url + "/authorize",
		"token_endpoint":                        url + "/token",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"subject_types_supported":               []string{"public"},
		"response_types_supported":              []string{"code"},
	})
}

func (i *Issuer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       i.key.Public(),
			KeyID:     i.keyID,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}},
	})
}

// Mint issues an ID token for the given identity.
func (i *Issuer) Mint(c Claims, opts MintOptions) (string, error) {
	issuer := opts.Issuer
	if issuer == "" {
		issuer = i.URL()
	}
	audience := opts.Audience
	if audience == "" {
		audience = i.audience
	}
	expiry := opts.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(time.Hour)
	}

	signingKey := i.key
	if opts.RogueKey {
		rogue, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return "", fmt.Errorf("generate rogue key: %w", err)
		}
		signingKey = rogue
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: signingKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.keyID),
	)
	if err != nil {
		return "", fmt.Errorf("build signer: %w", err)
	}

	payload, err := json.Marshal(claimSet(c, issuer, audience, expiry))
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	signed, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	compact, err := signed.CompactSerialize()
	if err != nil {
		return "", fmt.Errorf("serialize token: %w", err)
	}

	return compact, nil
}

func claimSet(c Claims, issuer, audience string, expiry time.Time) map[string]any {
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
	// Cognito omits the claim entirely for a user in no groups; mirroring that
	// matters, because "absent" and "empty array" unmarshal the same but only
	// one of them is what production sends.
	if len(c.Groups) > 0 {
		claims["cognito:groups"] = c.Groups
	}
	return claims
}
