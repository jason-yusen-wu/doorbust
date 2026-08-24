// Package auth verifies Cognito-issued ID tokens. doorbust is a resource
// server, not an OAuth2 client: the frontend owns the Cognito Hosted UI
// login/callback flow and hands doorbust a bearer ID token on each request,
// which this package checks against Cognito's JWKS before trusting the
// caller's identity.
package auth

import (
	"context"

	"github.com/coreos/go-oidc"
)

// Verifier checks bearer ID tokens against a Cognito user pool's JWKS.
type Verifier struct {
	idTokenVerifier *oidc.IDTokenVerifier
}

// NewVerifier discovers the issuer's OIDC configuration and JWKS up front, so
// a bad issuer URL fails at startup rather than on the first request.
func NewVerifier(ctx context.Context, issuerURL, clientID string) (*Verifier, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, err
	}

	return &Verifier{
		idTokenVerifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
	}, nil
}

// Verify checks the raw ID token's signature, issuer, audience, and
// expiry, and returns the caller's identity.
func (v *Verifier) Verify(ctx context.Context, rawIDToken string) (Claims, error) {
	idToken, err := v.idTokenVerifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Claims{}, err
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&body); err != nil {
		return Claims{}, err
	}

	return Claims{Subject: idToken.Subject, Email: body.Email}, nil
}
