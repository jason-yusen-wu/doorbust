package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jason-yusen-wu/doorbust/internal/json"
	"github.com/jason-yusen-wu/doorbust/internal/payments"
)

// readinessTimeout bounds the database ping. A readiness probe that can hang
// is worse than one that fails: the deploy's smoke step would block rather
// than report.
const readinessTimeout = 2 * time.Second

// stalePollMultiple is how many poll intervals may elapse before the poller is
// considered stalled. Generous, because a single slow poll is not an outage
// and flapping readiness would be worse than the condition it reports.
const stalePollMultiple = 5

// minPollerGrace floors the startup window. With a short poll interval,
// stalePollMultiple alone could be a fraction of a second — long enough to
// flag a perfectly healthy process as failing on the first request after a
// restart.
const minPollerGrace = 30 * time.Second

// Poller states reported by readiness.
const (
	pollerDisabled = "disabled"
	pollerStarting = "starting"
	pollerFailing  = "failing"
	pollerStale    = "stale"
	pollerOK       = "ok"
)

type readyResponse struct {
	Status              string `json:"status"`
	Database            string `json:"database"`
	StripePoller        string `json:"stripe_poller"`
	StripePollAgeSecond *int64 `json:"stripe_poll_age_seconds,omitempty"`
}

// pollerStatus decides what the poller's last-success time means.
//
// Kept as a pure function because its interesting cases are all about elapsed
// time, and driving those through a live poller would mean either sleeping or
// adding a test-only hook to production code.
//
// The distinction that matters is between a poller that has not succeeded
// *yet* and one that has never succeeded *despite having had time to*. The
// second is the shape of an invalid or revoked API key; reporting it as
// "starting" forever would defeat the point of the check.
func pollerStatus(last, startedAt, now time.Time, grace time.Duration) (status string, ready bool) {
	switch {
	case last.IsZero() && now.Sub(startedAt) <= grace:
		return pollerStarting, true
	case last.IsZero():
		return pollerFailing, false
	case now.Sub(last) > grace:
		return pollerStale, false
	default:
		return pollerOK, true
	}
}

// pollerGrace is how long the poller may go without a successful poll.
func pollerGrace(interval time.Duration) time.Duration {
	if grace := stalePollMultiple * interval; grace > minPollerGrace {
		return grace
	}
	return minPollerGrace
}

// readyHandler reports whether the app can actually serve traffic, as opposed
// to merely being alive.
//
// /health returns a static string and touches nothing, so a deploy whose
// database credentials were wrong, or whose Stripe key was revoked, still goes
// green. This is what the post-deploy smoke check looks at: it fails on
// exactly the conditions that make the app useless while still running.
//
// poller may be nil when event polling is disabled, which is a valid local
// configuration rather than a fault.
func (app *application) readyHandler(poller *payments.Poller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		body := readyResponse{Status: "ok", Database: "ok", StripePoller: pollerDisabled}
		ready := true

		if err := app.db.Ping(ctx); err != nil {
			// Logged in full, reported as one word: the pgx error embeds the
			// user and database name, which must not reach an unauthenticated
			// caller.
			slog.Error("readiness: database ping failed", "error", err)
			body.Database = "unavailable"
			ready = false
		}

		if poller != nil {
			last := poller.LastSuccess()
			status, pollerReady := pollerStatus(last, poller.StartedAt(), time.Now(), pollerGrace(poller.Interval()))

			body.StripePoller = status
			if !last.IsZero() {
				age := int64(time.Since(last).Seconds())
				body.StripePollAgeSecond = &age
			}

			if !pollerReady {
				slog.Error("readiness: stripe poller unhealthy",
					"status", status, "lastSuccess", last, "startedAt", poller.StartedAt())
				ready = false
			}
		}

		if !ready {
			body.Status = "unavailable"
			json.Write(w, http.StatusServiceUnavailable, body)
			return
		}

		json.Write(w, http.StatusOK, body)
	}
}
