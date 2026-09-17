// Package ratelimit bounds how fast one caller may hit an endpoint.
//
// A doorbuster endpoint without a rate limit is an invitation: the whole point
// of the product is that far more people want a unit than there are units, so
// the difference between a keen buyer and a script is entirely a matter of
// request rate. Limiting is also the cheap half of what a virtual waiting room
// does properly — shedding load before it reaches the database beats absorbing
// it, even when the database could have coped.
package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/json"
	"golang.org/x/time/rate"
)

// Limiter holds one token bucket per key.
//
// Buckets are evicted after an idle period. Without that the map is an
// unbounded allocation driven by unauthenticated input — every distinct source
// address gets an entry — which turns a defence against load into a way to
// exhaust memory.
type Limiter struct {
	rps   rate.Limit
	burst int
	idle  time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time // injectable so the eviction test needs no sleeps
}

type bucket struct {
	limiter *rate.Limiter
	seen    time.Time
}

// New builds a limiter allowing rps sustained requests per key, tolerating
// bursts of burst. A non-positive rps disables limiting entirely, which is how
// benchmark runs take this middleware off the measured path.
func New(rps float64, burst int, idle time.Duration) *Limiter {
	return &Limiter{
		rps:     rate.Limit(rps),
		burst:   burst,
		idle:    idle,
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// Enabled reports whether this limiter does anything.
func (l *Limiter) Enabled() bool { return l != nil && l.rps > 0 && l.burst > 0 }

// Allow consumes a token for key.
func (l *Limiter) Allow(key string) bool {
	if !l.Enabled() {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	b, ok := l.buckets[key]
	if !ok {
		// Sweep BEFORE inserting, not after. A freshly built bucket has a zero
		// seen time, so sweeping afterwards deletes the very bucket just
		// created — and every request then gets a brand new bucket with a full
		// burst, which is a limiter that does not limit.
		//
		// Sweeping on creation rather than on a timer is deliberate: the map
		// only grows when a new key arrives, so that is exactly when old
		// entries are worth collecting, and it keeps this package free of a
		// goroutine whose lifetime somebody has to own.
		l.evictLocked(now)

		b = &bucket{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.buckets[key] = b
	}
	b.seen = now

	return b.limiter.AllowN(now, 1)
}

func (l *Limiter) evictLocked(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.seen) > l.idle {
			delete(l.buckets, key)
		}
	}
}

// Len reports how many buckets are held, for tests and diagnostics.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Middleware limits by key, returning 429 when a caller is over their budget.
//
// keyFor returning "" means "do not limit this request" — used where the key is
// the caller's identity and the request is unauthenticated, since there is
// nothing meaningful to charge.
func (l *Limiter) Middleware(keyFor func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Enabled() {
				next.ServeHTTP(w, r)
				return
			}

			key := keyFor(r)
			if key == "" || l.Allow(key) {
				next.ServeHTTP(w, r)
				return
			}

			// Retry-After is advisory but cheap, and a client that honours it
			// stops making the problem worse.
			w.Header().Set("Retry-After", "1")
			json.WriteError(w, http.StatusTooManyRequests, json.CodeRateLimited,
				"too many requests")
		})
	}
}

// ByIP keys on the caller's address.
//
// chi's RealIP middleware has already resolved X-Forwarded-For where a proxy
// set it, so this reads RemoteAddr rather than parsing headers a second time.
// The port is stripped: a client's source port changes per connection, and
// keying on it would give every connection its own fresh budget.
func ByIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// BySubject keys on the authenticated caller, so one signed-in user cannot
// spread their load across addresses. Only meaningful behind auth.Middleware;
// an unauthenticated request yields "" and is left to the IP limiter.
func BySubject(r *http.Request) string {
	claims, ok := auth.FromContext(r.Context())
	if !ok || claims.Subject == "" {
		return ""
	}
	return claims.Subject
}
