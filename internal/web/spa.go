// Package web serves the compiled single-page storefront.
//
// The SPA is shipped inside the same container image as the API and served
// from the same origin, rather than from S3/CloudFront. That is not a
// preference: the EC2 box has no Elastic IP, no domain, no TLS and a security
// group admitting a single /32, so a browser on the open internet cannot reach
// :8080 at all — the same constraint that forces Stripe events to be pulled
// rather than pushed. A separately hosted bundle would load and then fail every
// API call. Same-origin also means production needs no CORS.
package web

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/jason-yusen-wu/doorbust/internal/json"
)

const indexFile = "index.html"

// Handler serves the built frontend out of distDir, falling back to
// index.html so client-side routes like /checkout/4192 survive a hard reload.
//
// Assets are read from disk rather than embedded with //go:embed on purpose:
// embedding requires web/dist to exist at `go build` time, which would break
// `go build ./...`, `make server` and the build CI job for anyone who has not
// run the frontend build. A missing directory degrades to a JSON 404 instead.
//
// distDir is resolved once, at mount. Building the frontend while the server is
// already running therefore needs a restart.
func Handler(distDir string) http.Handler {
	root, err := os.OpenRoot(distDir)
	if err != nil {
		slog.Warn("frontend assets unavailable; serving API only",
			"dir", distDir, "error", err)
		return http.HandlerFunc(notBuilt)
	}
	return &spa{root: root}
}

type spa struct {
	// root confines every open to distDir. os.Root refuses to traverse out of
	// it, so a crafted path cannot read files elsewhere on the box.
	root *os.Root
}

func (s *spa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = indexFile
	}

	f, info, ok := s.open(name)
	if !ok {
		// A path with an extension is asking for a specific file. Falling back
		// to index.html there would answer a missing .js with 200 and a body of
		// HTML, which surfaces as a confusing parse error in the browser rather
		// than the 404 it actually is. Only extensionless paths are routes.
		if path.Ext(name) != "" {
			json.WriteError(w, http.StatusNotFound, json.CodeNotFound, "not found")
			return
		}
		s.serveIndex(w, r)
		return
	}
	defer f.Close()

	setCacheControl(w, name)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// serveIndex answers a client-side route with the app shell.
func (s *spa) serveIndex(w http.ResponseWriter, r *http.Request) {
	f, info, ok := s.open(indexFile)
	if !ok {
		json.WriteError(w, http.StatusNotFound, json.CodeNotFound, "not found")
		return
	}
	defer f.Close()

	setCacheControl(w, indexFile)
	http.ServeContent(w, r, indexFile, info.ModTime(), f)
}

// open resolves name under the dist root. Directories report as missing: there
// is no listing to serve, and a request for one is a client-side route.
func (s *spa) open(name string) (*os.File, fs.FileInfo, bool) {
	f, err := s.root.Open(name)
	if err != nil {
		return nil, nil, false
	}

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		f.Close()
		return nil, nil, false
	}
	return f, info, true
}

// setCacheControl splits the bundle into the two things it actually contains.
//
// Vite fingerprints everything under assets/ with a content hash, so those may
// be cached forever. index.html must not be: it is the document naming those
// hashes, and a cached copy would keep pointing at asset filenames that the
// next deploy has already replaced.
func setCacheControl(w http.ResponseWriter, name string) {
	switch {
	case name == indexFile:
		w.Header().Set("Cache-Control", "no-store")
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
}

// notBuilt stands in when there is no frontend to serve. It answers in the same
// envelope as every other error on the API — handlers never call http.Error,
// which would write text/plain and leave the API returning two content types.
func notBuilt(w http.ResponseWriter, _ *http.Request) {
	json.WriteError(w, http.StatusNotFound, json.CodeNotFound, "not found")
}
