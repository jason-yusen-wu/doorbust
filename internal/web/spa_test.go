package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jason-yusen-wu/doorbust/internal/web"
)

// distDir writes a minimal Vite-shaped bundle: a hashed asset plus the shell
// that names it.
func distDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}

	files := map[string]string{
		"index.html":             `<!doctype html><div id="root"></div>`,
		"favicon.ico":            "icon",
		"assets/index-abc123.js": "console.log(1)",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServesBundledFiles(t *testing.T) {
	t.Parallel()

	h := web.Handler(distDir(t))

	tests := []struct {
		name   string
		target string
		body   string
		cache  string
	}{
		{"root serves the shell", "/", `<!doctype html><div id="root"></div>`, "no-store"},
		{"index by name", "/index.html", `<!doctype html><div id="root"></div>`, "no-store"},
		{
			"hashed asset is immutable",
			"/assets/index-abc123.js",
			"console.log(1)",
			"public, max-age=31536000, immutable",
		},
		{"unhashed root file is briefly cacheable", "/favicon.ico", "icon", "public, max-age=300"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := get(t, h, tt.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.body {
				t.Errorf("body = %q, want %q", got, tt.body)
			}
			// index.html must never be cached: it is the document naming the
			// hashed assets, and a stale copy points at files the next deploy
			// has already replaced.
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
		})
	}
}

// A client-side route has no file behind it, so a hard reload of /checkout/4192
// must still return the shell rather than 404.
func TestClientSideRoutesFallBackToTheShell(t *testing.T) {
	t.Parallel()

	h := web.Handler(distDir(t))

	for _, target := range []string{"/checkout/4192", "/orders", "/sale/1041", "/assets"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			rec := get(t, h, target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if rec.Body.String() != `<!doctype html><div id="root"></div>` {
				t.Errorf("did not serve the shell; body = %q", rec.Body.String())
			}
		})
	}
}

// A missing file with an extension is a missing file, not a route. Falling back
// would answer a deleted .js with 200 and a body of HTML, which surfaces in the
// browser as a syntax error instead of the 404 it is.
func TestMissingAssetIsNotRoutedToTheShell(t *testing.T) {
	t.Parallel()

	h := web.Handler(distDir(t))

	for _, target := range []string{"/assets/gone-000000.js", "/missing.css", "/nope.png"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			rec := get(t, h, target)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			assertErrorEnvelope(t, rec)
		})
	}
}

// os.Root is what confines reads to the bundle. Without it a crafted path could
// read arbitrary files off the box.
func TestPathTraversalIsRefused(t *testing.T) {
	t.Parallel()

	dir := distDir(t)
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("STRIPE_SECRET_KEY=sk_live"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	h := web.Handler(dir)

	// The first is normalised away by path.Clean before it reaches the root;
	// the second is what an encoded traversal decodes to, and is refused by
	// os.Root itself.
	for _, target := range []string{"/../secret.txt", "/assets/..%2f..%2fsecret.txt"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			rec := get(t, h, target)
			if body := rec.Body.String(); strings.Contains(body, "sk_live") {
				t.Fatalf("traversal leaked a file outside the bundle: %q", body)
			}
		})
	}
}

// Building the API without building the frontend is a supported state: it is
// what `go run ./cmd`, `go build ./...` and the whole Go test suite do.
func TestMissingBundleDegradesToAJSON404(t *testing.T) {
	t.Parallel()

	h := web.Handler(filepath.Join(t.TempDir(), "never-built"))

	rec := get(t, h, "/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertErrorEnvelope(t, rec)
}

// A directory that exists but holds no index.html is the same situation.
func TestEmptyBundleDegradesToAJSON404(t *testing.T) {
	t.Parallel()

	rec := get(t, web.Handler(t.TempDir()), "/orders")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertErrorEnvelope(t, rec)
}

// Every failure on this API uses one envelope. Handlers never call http.Error,
// which would write text/plain and leave the API returning two content types
// depending on the outcome.
func assertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the error envelope: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Code != "not_found" {
		t.Errorf("error.code = %q, want not_found", body.Error.Code)
	}
}
