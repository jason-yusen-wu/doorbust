package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// corsRouter builds the real router with CORS configured as given.
//
// Unlike newHarness this needs no database: a preflight is answered by the
// middleware before routing, and /health touches nothing. Keeping it
// database-free means the CORS contract is covered by `make test`, the fast
// loop that runs with no Docker at all.
func corsRouter(t *testing.T, origins ...string) http.Handler {
	t.Helper()

	app := &application{
		config: config{
			corsAllowedOrigins: origins,
			// A directory with no index.html, so the catch-all route resolves
			// without a frontend build.
			webDistDir: t.TempDir(),
		},
		auth: testsupport.NewIssuer(t).Verifier(t),
	}
	return app.mount()
}

func preflight(t *testing.T, h http.Handler, origin, method string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodOptions, "/orders", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", method)
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const devOrigin = "http://localhost:5173"

// The dev loop: Vite serves the app from :5173 and every call to :8080 is
// cross-origin. Without this the browser refuses them all.
func TestPreflightIsAllowedForAConfiguredOrigin(t *testing.T) {
	t.Parallel()

	rec := preflight(t, corsRouter(t, devOrigin), devOrigin, http.MethodPost)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 200 or 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != devOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, devOrigin)
	}

	// Authorization is the header that matters: every protected call carries
	// the Cognito ID token on it, so a policy omitting it would allow the
	// preflight and still break every authenticated request.
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "authorization") {
		t.Errorf("Access-Control-Allow-Headers = %q, want it to include Authorization", got)
	}
}

// DELETE is easy to leave out of the method list — it is used by exactly one
// route ("Release my unit" → DELETE /orders/{id}).
func TestPreflightAllowsEveryMethodTheAPIUses(t *testing.T) {
	t.Parallel()

	h := corsRouter(t, devOrigin)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			rec := preflight(t, h, devOrigin, method)
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != devOrigin {
				t.Errorf("%s preflight was refused: Access-Control-Allow-Origin = %q", method, got)
			}
		})
	}
}

func TestPreflightIsRefusedForAnUnconfiguredOrigin(t *testing.T) {
	t.Parallel()

	rec := preflight(t, corsRouter(t, devOrigin), "https://evil.example", http.MethodPost)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q for an unlisted origin; want none", got)
	}
}

// The production configuration. The frontend is served by this process, so
// nothing is cross-origin and CORS is simply not mounted — a misconfiguration
// must deny access, never widen it.
func TestNoOriginsConfiguredEmitsNoCORSHeaders(t *testing.T) {
	t.Parallel()

	h := corsRouter(t) // no origins

	t.Run("preflight", func(t *testing.T) {
		t.Parallel()

		rec := preflight(t, h, devOrigin, http.MethodPost)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Access-Control-Allow-Origin = %q with CORS disabled; want none", got)
		}
	})

	t.Run("simple request", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set("Origin", devOrigin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		for header := range rec.Header() {
			if strings.HasPrefix(header, "Access-Control-") {
				t.Errorf("unexpected %s header with CORS disabled", header)
			}
		}
	})
}
