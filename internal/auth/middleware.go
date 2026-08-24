package auth

import (
	"context"
	"net/http"
	"strings"
)

// Claims is the identity of the caller of an authenticated request.
type Claims struct {
	Subject string
	Email   string
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
		rawToken, ok := bearerToken(r)
		if !ok {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}

		claims, err := v.Verify(r.Context(), rawToken)
		if err != nil {
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	return strings.TrimPrefix(header, prefix), true
}
