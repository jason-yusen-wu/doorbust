package auth

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jason-yusen-wu/doorbust/internal/json"
)

// Claims is the identity of the caller of an authenticated request.
type Claims struct {
	Subject string
	Email   string
	// Groups is the caller's Cognito group membership, from the token's
	// "cognito:groups" claim. Nil for a user in no groups.
	Groups []string
}

// HasGroup reports whether the caller belongs to the named Cognito group.
func (c Claims) HasGroup(group string) bool {
	return slices.Contains(c.Groups, group)
}

type contextKey int

const claimsContextKey contextKey = iota

// FromContext returns the caller's identity, set by Middleware.
func FromContext(ctx context.Context) (Claims, bool) {
	claims, ok := ctx.Value(claimsContextKey).(Claims)
	return claims, ok
}

// Middleware requires a valid Cognito ID token on the "Authorization: Bearer
// <token>" header, and makes the caller's identity available via
// FromContext for the rest of the request.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 401s go out in the same JSON envelope as every other error. A
		// client should not have to parse text/plain for the one failure it
		// is most likely to hit.
		rawToken, ok := bearerToken(r)
		if !ok {
			json.WriteError(w, http.StatusUnauthorized, json.CodeUnauthorized, "missing bearer token")
			return
		}

		claims, err := v.Verify(r.Context(), rawToken)
		if err != nil {
			// The verification error is deliberately not echoed: it can
			// distinguish expired from malformed from wrong-audience, which
			// is detail an unauthenticated caller has no need for.
			json.WriteError(w, http.StatusUnauthorized, json.CodeUnauthorized, "invalid bearer token")
			return
		}

		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireGroup rejects a caller who is not in the named Cognito group. It must
// be layered *after* Middleware, which is what puts the claims on the context.
//
// It is a plain function rather than a *Verifier method on purpose: authorizing
// an already-verified caller needs only the context, not the JWKS.
func RequireGroup(group string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := FromContext(r.Context())
			if !ok {
				// Middleware did not run on this route — a wiring bug, not a
				// caller error. 401 rather than 403: we know nothing about
				// who is asking, so we cannot say they lack permission.
				json.WriteError(w, http.StatusUnauthorized, json.CodeUnauthorized, "unauthorized")
				return
			}

			if !claims.HasGroup(group) {
				// Deliberately does not name the required group: which roles
				// exist is not something an unprivileged caller needs to
				// learn from a rejection.
				json.WriteError(w, http.StatusForbidden, json.CodeForbidden, "caller is not permitted to perform this action")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	return strings.TrimPrefix(header, prefix), true
}
