package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

func TestDisabledLimiterAllowsEverything(t *testing.T) {
	t.Parallel()

	// Zero is the default, and the default must never start rejecting traffic
	// on an existing deployment.
	l := New(0, 20, time.Minute)
	if l.Enabled() {
		t.Fatal("a zero rate should disable the limiter")
	}
	for range 1000 {
		if !l.Allow("anyone") {
			t.Fatal("a disabled limiter rejected a request")
		}
	}
}

func TestBurstThenThrottle(t *testing.T) {
	t.Parallel()

	l := New(1, 5, time.Minute)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	for i := range 5 {
		if !l.Allow("a") {
			t.Fatalf("request %d within the burst was rejected", i)
		}
	}
	if l.Allow("a") {
		t.Error("the request past the burst was allowed")
	}

	// A second later, one token has refilled at 1/s — and only one.
	l.now = func() time.Time { return frozen.Add(time.Second) }
	if !l.Allow("a") {
		t.Error("no token had refilled after a second")
	}
	if l.Allow("a") {
		t.Error("more than one token refilled in a second at 1 rps")
	}
}

func TestBudgetsArePerKey(t *testing.T) {
	t.Parallel()

	l := New(1, 2, time.Minute)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	for range 2 {
		l.Allow("a")
	}
	if l.Allow("a") {
		t.Fatal("key a should be exhausted")
	}
	// One caller exhausting their budget must not affect anyone else.
	if !l.Allow("b") {
		t.Error("key b was throttled by key a's traffic")
	}
}

// The bucket map is keyed by unauthenticated input, so without eviction it is
// an unbounded allocation driven by a stranger — a defence against load that
// becomes a way to exhaust memory.
func TestIdleBucketsAreEvicted(t *testing.T) {
	t.Parallel()

	l := New(10, 10, time.Minute)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	for _, k := range []string{"a", "b", "c"} {
		l.Allow(k)
	}
	if got := l.Len(); got != 3 {
		t.Fatalf("holding %d buckets, want 3", got)
	}

	// Long past the idle window, a new key triggers the sweep.
	l.now = func() time.Time { return frozen.Add(2 * time.Minute) }
	l.Allow("d")

	if got := l.Len(); got != 1 {
		t.Errorf("holding %d buckets after eviction, want only the new one", got)
	}
}

func TestMiddlewareReturns429WithTheErrorEnvelope(t *testing.T) {
	t.Parallel()

	l := New(1, 1, time.Minute)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	h := l.Middleware(ByIP)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/products", nil)
		req.RemoteAddr = "203.0.113.7:54321"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("first request got %d, want 200", rec.Code)
	}

	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request got %d, want 429", rec.Code)
	}
	// Every non-2xx body in this API is the same envelope; a limiter that
	// answered in plain text would break that contract.
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q, want application/json", ct)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	if body := rec.Body.String(); !strings.Contains(body, `"rate_limited"`) {
		t.Errorf("body %q does not carry the rate_limited code", body)
	}
}

// A client's source port changes per connection, so keying on it would hand
// every new connection a fresh budget and make the limiter decorative.
func TestByIPIgnoresThePort(t *testing.T) {
	t.Parallel()

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "198.51.100.4:1111"
	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = "198.51.100.4:2222"

	if ByIP(first) != ByIP(second) {
		t.Errorf("same address on two ports gave different keys: %q and %q", ByIP(first), ByIP(second))
	}
}

func TestBySubject(t *testing.T) {
	t.Parallel()

	// Unauthenticated: nothing meaningful to charge, so the IP limiter owns it.
	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := BySubject(plain); got != "" {
		t.Errorf("unauthenticated request keyed as %q, want empty", got)
	}

	ctx := auth.NewContext(context.Background(), auth.Claims{Subject: "sub-9"})
	authed := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	if got := BySubject(authed); got != "sub-9" {
		t.Errorf("keyed as %q, want sub-9", got)
	}
}
