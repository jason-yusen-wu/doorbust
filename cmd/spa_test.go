package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

const shell = `<!doctype html><div id="root"></div>`

// routerWithFrontend mounts the real router over a bundle that actually exists,
// which is the configuration production runs and no other test covers — every
// other test here runs with no frontend built.
func routerWithFrontend(t *testing.T) http.Handler {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(shell), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	app := &application{
		config: config{webDistDir: dir},
		auth:   testsupport.NewIssuer(t).Verifier(t),
	}
	return app.mount()
}

// The catch-all is registered last and matches everything, so the question this
// answers is whether chi still prefers the specific patterns. If it did not,
// the API would start answering JSON requests with a page — and every client
// would break at once.
func TestStorefrontDoesNotShadowTheAPI(t *testing.T) {
	t.Parallel()

	h := routerWithFrontend(t)

	tests := []struct {
		name   string
		method string
		target string
		// The catch-all serves only GET, so a non-GET to an unknown path must
		// still fail as a method mismatch rather than render a page.
		wantShell bool
	}{
		{name: "liveness is not the page", method: http.MethodGet, target: "/health"},
		{name: "a protected route still 401s", method: http.MethodGet, target: "/orders"},
		{name: "an unknown API-shaped POST is not the page", method: http.MethodPost, target: "/nope"},
		{name: "a client-side route is the page", method: http.MethodGet, target: "/checkout/4192", wantShell: true},
		{name: "the root is the page", method: http.MethodGet, target: "/", wantShell: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, nil))

			gotShell := strings.Contains(rec.Body.String(), `id="root"`)
			if gotShell != tt.wantShell {
				t.Errorf("%s %s served shell=%v, want %v (status %d, body %q)",
					tt.method, tt.target, gotShell, tt.wantShell, rec.Code, rec.Body)
			}
		})
	}
}

// The gate has to hold with a frontend present too: the catch-all is public, so
// a mistake in ordering could let it answer a protected path before the auth
// middleware runs — turning a 401 into a 200.
func TestAuthStillAppliesWithAFrontendMounted(t *testing.T) {
	t.Parallel()

	h := routerWithFrontend(t)

	for _, target := range []string{"/orders", "/orders/1", "/me"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("GET %s returned %d with a frontend mounted, want 401", target, rec.Code)
			}
		})
	}
}
