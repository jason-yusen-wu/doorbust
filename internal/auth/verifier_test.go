// This file is package auth_test rather than package auth because it imports
// internal/testsupport, which itself imports internal/auth. An external test
// package breaks what would otherwise be an import cycle.
package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// protected wraps a handler that records the claims the middleware resolved.
func protected(v *auth.Verifier, got *auth.Claims, reached *bool) http.Handler {
	return v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		if c, ok := auth.FromContext(r.Context()); ok {
			*got = c
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// The middleware is the app's entire authentication boundary and was
// completely untested. These cases run the real verifier against a real JWKS,
// so signature, issuer, audience and expiry are genuinely checked rather than
// stubbed away.
func TestMiddlewareRejectsBadTokens(t *testing.T) {
	t.Parallel()

	issuer := testsupport.NewIssuer(t)
	verifier := issuer.Verifier(t)
	identity := testsupport.TokenClaims{Subject: "sub-1", Email: "user@example.test"}

	tests := []struct {
		name   string
		header string
	}{
		{"no authorization header", ""},
		{"wrong scheme", "Basic abcdef"},
		{"bearer with no token", "Bearer "},
		{"lowercase scheme is not accepted", "bearer " + issuer.Token(t, identity)},
		{"garbage token", "Bearer not-a-jwt"},
		{"expired token", "Bearer " + issuer.ExpiredToken(t, identity)},
		{"token for another app client", "Bearer " + issuer.WrongAudienceToken(t, identity)},
		{"token from another issuer", "Bearer " + issuer.WrongIssuerToken(t, identity)},
		{"forged signature", "Bearer " + issuer.BadSignatureToken(t, identity)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				got     auth.Claims
				reached bool
			)

			req := httptest.NewRequest(http.MethodGet, "/orders", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			rec := httptest.NewRecorder()
			protected(verifier, &got, &reached).ServeHTTP(rec, req)

			if reached {
				t.Fatal("request reached the handler; it must be rejected")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}

			// Rejections must use the JSON envelope like every other error.
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("not the error envelope: %v (%s)", err, rec.Body)
			}
			if body.Error.Code != "unauthorized" {
				t.Errorf("code = %q, want unauthorized", body.Error.Code)
			}
			// The reason must not be disclosed: expired, wrong-audience and
			// forged all look the same from outside.
			if body.Error.Message != "missing bearer token" && body.Error.Message != "invalid bearer token" {
				t.Errorf("message %q leaks why verification failed", body.Error.Message)
			}
		})
	}
}

func TestMiddlewareAcceptsValidTokenAndExtractsClaims(t *testing.T) {
	t.Parallel()

	issuer := testsupport.NewIssuer(t)
	verifier := issuer.Verifier(t)

	t.Run("buyer has no groups", func(t *testing.T) {
		t.Parallel()

		token, want := issuer.Buyer(t, 1)

		var (
			got     auth.Claims
			reached bool
		)
		req := httptest.NewRequest(http.MethodGet, "/orders", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		rec := httptest.NewRecorder()
		protected(verifier, &got, &reached).ServeHTTP(rec, req)

		if !reached || rec.Code != http.StatusOK {
			t.Fatalf("valid token rejected: status %d, body %s", rec.Code, rec.Body)
		}
		if got.Subject != want.Subject {
			t.Errorf("subject = %q, want %q", got.Subject, want.Subject)
		}
		if got.Email != want.Email {
			t.Errorf("email = %q, want %q", got.Email, want.Email)
		}
		// Cognito omits cognito:groups entirely for a user in no groups.
		if len(got.Groups) != 0 {
			t.Errorf("groups = %v, want none", got.Groups)
		}
		if got.HasGroup(testsupport.VendorGroup) {
			t.Error("a buyer must not be reported as a vendor")
		}
	})

	t.Run("vendor carries the group claim", func(t *testing.T) {
		t.Parallel()

		token, want := issuer.Vendor(t, 1)

		var (
			got     auth.Claims
			reached bool
		)
		req := httptest.NewRequest(http.MethodPost, "/products", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		rec := httptest.NewRecorder()
		protected(verifier, &got, &reached).ServeHTTP(rec, req)

		if !reached || rec.Code != http.StatusOK {
			t.Fatalf("valid vendor token rejected: status %d, body %s", rec.Code, rec.Body)
		}
		if !slices.Contains(got.Groups, testsupport.VendorGroup) {
			t.Errorf("groups = %v, want to contain %q", got.Groups, testsupport.VendorGroup)
		}
		if got.Email != want.Email {
			t.Errorf("email = %q, want %q", got.Email, want.Email)
		}
	})
}

// NewVerifier does OIDC discovery eagerly so a bad issuer fails at startup
// rather than on the first request — main.go panics on this path.
func TestNewVerifierFailsFastOnBadIssuer(t *testing.T) {
	t.Parallel()

	issuer := testsupport.NewIssuer(t)

	if _, err := auth.NewVerifier(issuer.Context(), issuer.URL()+"/nonexistent", testsupport.TestClientID); err == nil {
		t.Error("expected discovery against a bad issuer URL to fail")
	}
}
