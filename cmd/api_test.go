package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// These tests run the real router: the real middleware chain, the real Cognito
// verifier (against a local JWKS), a real database, and a stubbed Stripe HTTP
// API. They live in package main because mount() is a method on application —
// testing the actual composition root rather than a reassembled copy of it is
// the entire point, since the bugs that shipped were wiring bugs.

// testWebhookSecret signs the stub webhook payloads. Stripe's ConstructEvent
// computes the signature over the exact bytes, so tests must sign with the
// same secret the handler is configured with.
const testWebhookSecret = "whsec_test_secret_for_signing"

type harness struct {
	app    *application
	router http.Handler
	db     *pgxpool.Pool
	issuer *testsupport.Issuer
	stripe *testsupport.StripeAPI
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db := testsupport.DB(t)
	issuer := testsupport.NewIssuer(t)
	stripeAPI := testsupport.NewStripeAPI(t)

	app := &application{
		config: config{
			addr:            ":0",
			shutdownTimeout: time.Second,
			cognito: cognitoConfig{
				issuerURL:   issuer.URL(),
				clientID:    testsupport.TestClientID,
				vendorGroup: testsupport.VendorGroup,
			},
			stripe: stripeConfig{
				secretKey:     "sk_test_stub",
				webhookSecret: testWebhookSecret,
				currency:      "usd",
			},
			orders: ordersConfig{
				reservationTTL: 15 * time.Minute,
				sweepInterval:  time.Minute,
				sweepBatchSize: 100,
			},
			payments: paymentsConfig{
				pollInterval: time.Second,
				maxAttempts:  5,
				// Zero disables the poller goroutine. mount() only registers
				// background jobs, never starts them, but leaving it off keeps
				// the readiness expectations unambiguous.
				eventPollInterval: 0,
			},
		},
		db:                  db,
		auth:                issuer.Verifier(t),
		stripeClientOptions: stripeAPI.Options(),
	}

	return &harness{
		app:    app,
		router: app.mount(),
		db:     db,
		issuer: issuer,
		stripe: stripeAPI,
	}
}

// do sends a request through the real router.
func (h *harness) do(t *testing.T, method, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, testsupport.Request(t, method, target, token, body))
	return rec
}

// access is how a route may be reached.
type access int

const (
	public access = iota
	authenticated
	vendorOnly
)

func (a access) String() string {
	switch a {
	case public:
		return "public"
	case authenticated:
		return "authenticated"
	default:
		return "vendor-only"
	}
}

// routeAccess declares the intended access level of every route.
//
// This table is the security contract, and the test below walks the *real*
// router to check reality against it. A route added to mount() without an
// entry here fails the test — which is precisely the bug class that shipped
// POST /products with no authorization at all. Adding an entry is a deliberate
// act; forgetting to gate a route is not.
var routeAccess = map[string]access{
	"GET /health":                public,
	"GET /health/ready":          public,
	"GET /products":              public,
	"GET /products/{id}":         public,
	"POST /webhooks/stripe":      public, // authenticated by signature, not token
	"GET /me":                    authenticated,
	"GET /orders":                authenticated,
	"POST /orders":               authenticated,
	"GET /orders/{id}":           authenticated,
	"DELETE /orders/{id}":        authenticated,
	"POST /orders/{id}/checkout": authenticated,
	"POST /products":             vendorOnly,
	// The storefront. Public by necessity — it is the page that renders the
	// sign-in button, so requiring a token to fetch it could not work.
	"GET /*": public,
}

func TestEveryRouteIsClassified(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	seen := map[string]bool{}
	err := chi.Walk(h.router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// chi reports the root pattern with a trailing slash.
		route = strings.TrimSuffix(route, "/")
		if route == "" {
			route = "/"
		}

		key := method + " " + route
		seen[key] = true

		if _, ok := routeAccess[key]; !ok {
			t.Errorf("route %q is mounted but has no entry in routeAccess.\n"+
				"Add one declaring whether it is public, authenticated, or vendor-only — "+
				"and make sure mount() actually applies that middleware.", key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	for key := range routeAccess {
		if !seen[key] {
			t.Errorf("routeAccess declares %q but no such route is mounted", key)
		}
	}
}

// TestRouteAccessIsEnforced drives every declared route with an anonymous, a
// buyer, and a vendor caller, and asserts the gate behaves as declared.
//
// The table above says what we intend; this says what actually happens.
func TestRouteAccessIsEnforced(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	buyerToken, _ := h.issuer.Buyer(t, 1)
	vendorToken, _ := h.issuer.Vendor(t, 1)

	// A concrete request per route. Bodies and ids need not resolve — we are
	// asserting the route gate, so anything other than 401/403 counts as
	// "allowed through", including 404 and 400.
	//
	// The {id} routes deliberately target an id that does not exist. Those
	// handlers also return 403 for an order the caller does not own, which
	// would be indistinguishable from the middleware rejecting them; a
	// missing order yields 404 for every authenticated caller, so 403 here
	// can only mean the gate. Ownership is covered in the endpoint tests,
	// where the distinction is the point rather than a confounder.
	const missingID = "999999"

	probes := []struct {
		key    string
		method string
		target string
		body   string
	}{
		{"GET /health", http.MethodGet, "/health", ""},
		{"GET /health/ready", http.MethodGet, "/health/ready", ""},
		{"GET /products", http.MethodGet, "/products", ""},
		{"GET /products/{id}", http.MethodGet, "/products/" + missingID, ""},
		{"POST /products", http.MethodPost, "/products", `{"name":"x","price_in_cents":1,"quantity":1}`},
		{"GET /me", http.MethodGet, "/me", ""},
		{"GET /orders", http.MethodGet, "/orders", ""},
		{"POST /orders", http.MethodPost, "/orders", `{"product_id":` + missingID + `}`},
		{"GET /orders/{id}", http.MethodGet, "/orders/" + missingID, ""},
		{"DELETE /orders/{id}", http.MethodDelete, "/orders/" + missingID, ""},
		{"POST /orders/{id}/checkout", http.MethodPost, "/orders/" + missingID + "/checkout", ""},
		{"POST /webhooks/stripe", http.MethodPost, "/webhooks/stripe", `{}`},
		// No frontend is built during tests, so this 404s — which is exactly
		// what the gate assertion needs: anything other than 401/403 counts as
		// having been let through.
		{"GET /*", http.MethodGet, "/some-client-side-route", ""},
	}

	if len(probes) != len(routeAccess) {
		t.Fatalf("%d probes for %d declared routes; every route needs one", len(probes), len(routeAccess))
	}

	for _, p := range probes {
		want, ok := routeAccess[p.key]
		if !ok {
			t.Fatalf("probe %q has no routeAccess entry", p.key)
		}

		t.Run(p.key, func(t *testing.T) {
			assertGate := func(t *testing.T, caller, token string, shouldPass bool) {
				t.Helper()

				rec := h.do(t, p.method, p.target, token, p.body)
				blocked := rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden

				if shouldPass && blocked {
					t.Errorf("%s caller was blocked with %d on a %s route; body: %s",
						caller, rec.Code, want, rec.Body)
				}
				if !shouldPass && !blocked {
					t.Errorf("%s caller reached a %s route (status %d); the gate is not applied",
						caller, want, rec.Code)
				}
			}

			assertGate(t, "anonymous", "", want == public)
			assertGate(t, "buyer", buyerToken, want == public || want == authenticated)
			assertGate(t, "vendor", vendorToken, true)
		})
	}
}

// A route that requires auth must reject a forged or expired token, not just a
// missing one. Cheap to get wrong by mounting the wrong middleware.
func TestProtectedRoutesRejectInvalidTokens(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	identity := testsupport.TokenClaims{Subject: "sub-x", Email: "x@example.test"}

	tokens := map[string]string{
		"expired":        h.issuer.ExpiredToken(t, identity),
		"wrong audience": h.issuer.WrongAudienceToken(t, identity),
		"wrong issuer":   h.issuer.WrongIssuerToken(t, identity),
		"forged":         h.issuer.BadSignatureToken(t, identity),
	}

	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, "/me", token, "")
			testsupport.AssertErrorEnvelope(t, rec, http.StatusUnauthorized, "unauthorized")
		})
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	t.Run("unknown path is 404", func(t *testing.T) {
		if rec := h.do(t, http.MethodGet, "/nope", "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("wrong method is 405", func(t *testing.T) {
		if rec := h.do(t, http.MethodPatch, "/products", "", ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
	})
}

func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	t.Run("liveness touches nothing", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/health", "", "")
		testsupport.AssertStatus(t, rec, http.StatusOK)
		if got := strings.TrimSpace(rec.Body.String()); got != "all good" {
			t.Errorf("body = %q, want %q", got, "all good")
		}
	})

	t.Run("readiness reports a live database", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/health/ready", "", "")
		testsupport.AssertStatus(t, rec, http.StatusOK)

		var body struct {
			Status       string `json:"status"`
			Database     string `json:"database"`
			StripePoller string `json:"stripe_poller"`
		}
		testsupport.DecodeJSON(t, rec, &body)

		if body.Status != "ok" || body.Database != "ok" {
			t.Errorf("got %+v, want status and database ok", body)
		}
		// The poller is switched off in this harness, which is a valid
		// configuration and must not fail readiness.
		if body.StripePoller != "disabled" {
			t.Errorf("stripe_poller = %q, want disabled", body.StripePoller)
		}
	})

	t.Run("readiness fails when the database is gone", func(t *testing.T) {
		// A separate app over a pool pointed at a closed database: readiness
		// must report unavailable rather than lying or hanging.
		dead := newHarness(t)
		dead.db.Close()

		rec := dead.do(t, http.MethodGet, "/health/ready", "", "")
		testsupport.AssertStatus(t, rec, http.StatusServiceUnavailable)

		var body struct {
			Status   string `json:"status"`
			Database string `json:"database"`
		}
		testsupport.DecodeJSON(t, rec, &body)

		if body.Database != "unavailable" {
			t.Errorf("database = %q, want unavailable", body.Database)
		}
		// The pgx error embeds the database user and name; it must not leak.
		if strings.Contains(rec.Body.String(), "postgres://") || strings.Contains(rec.Body.String(), "password") {
			t.Errorf("readiness body leaked connection detail: %s", rec.Body)
		}
	})
}

// postgresql.NewPool is the startup path that makes a bad DSN fail fast rather
// than surfacing on the first request.
func TestPoolFailsFastOnBadDSN(t *testing.T) {
	t.Parallel()

	_, err := postgresql.NewPool(t.Context(), postgresql.Config{
		DSN:      "postgres://nobody:nobody@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1",
		MaxConns: 1,
		MinConns: 0,
	})
	if err == nil {
		t.Error("expected an unreachable DSN to fail at pool construction")
	}
}
