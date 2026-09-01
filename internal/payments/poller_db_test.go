package payments

import (
	"context"
	"testing"
	"time"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
	"github.com/stripe/stripe-go/v83"
)

// The poller against a real database. The pure cursor arithmetic is covered in
// poller_test.go; what needs a database is the cursor actually advancing from
// what was stored, which is the property that makes the poller crash-safe.

func TestPollerAdvancesCursorAcrossRuns(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	queries := repo.New(pool)

	created := time.Now().Add(-time.Minute).Truncate(time.Second)
	lister := &fakeLister{events: []*stripe.Event{
		testEvent("evt_a", stripe.EventTypePaymentIntentSucceeded, created.Unix(), "pi_a"),
	}}

	poller := NewPoller(queries, lister, time.Second, 2*time.Minute, time.Hour)
	ctx := context.Background()

	// First poll: empty inbox, so the window is the initial lookback.
	if _, inserted, err := poller.poll(ctx); err != nil || inserted != 1 {
		t.Fatalf("first poll: inserted=%d err=%v, want 1/nil", inserted, err)
	}
	firstSince := lister.gotSince

	// Second poll: the cursor now comes from the stored event, so the window
	// starts near it rather than an hour back.
	if _, inserted, err := poller.poll(ctx); err != nil || inserted != 0 {
		t.Fatalf("second poll: inserted=%d err=%v, want 0/nil (dedup)", inserted, err)
	}

	if lister.gotSince <= firstSince {
		t.Errorf("cursor did not advance: since went %d -> %d", firstSince, lister.gotSince)
	}
	if want := created.Add(-2 * time.Minute).Unix(); lister.gotSince != want {
		t.Errorf("since = %d, want %d (stored event minus the overlap)", lister.gotSince, want)
	}

	if n := testsupport.CountRows(t, pool, "stripe_events"); n != 1 {
		t.Errorf("stripe_events = %d, want 1", n)
	}
}

// The stored payload must be the shape the worker parses, end to end through a
// real insert and read.
func TestPollerStoresWorkerReadablePayload(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	queries := repo.New(pool)

	lister := &fakeLister{events: []*stripe.Event{
		testEvent("evt_shape", stripe.EventTypePaymentIntentSucceeded, time.Now().Unix(), "pi_shape"),
	}}

	poller := NewPoller(queries, lister, time.Second, time.Minute, time.Hour)
	if _, _, err := poller.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	stored := eventRow(t, pool, "evt_shape")

	got, err := paymentIntentID(stored.Payload)
	if err != nil {
		t.Fatalf("the worker cannot parse what the poller stored: %v", err)
	}
	if got != "pi_shape" {
		t.Errorf("payment intent id = %q, want pi_shape", got)
	}
	if !stored.StripeCreatedAt.Valid {
		t.Error("stripe_created_at was not stored; the cursor would never advance")
	}
}

// LastSuccess backs the readiness endpoint: a poller that is running but
// failing every tick must be distinguishable from a healthy one.
func TestPollerReportsLastSuccess(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	queries := repo.New(pool)

	lister := &fakeLister{}
	poller := NewPoller(queries, lister, 5*time.Millisecond, time.Minute, time.Hour)

	if !poller.LastSuccess().IsZero() {
		t.Error("LastSuccess is set before any poll has run")
	}
	if poller.Interval() != 5*time.Millisecond {
		t.Errorf("Interval = %v, want 5ms", poller.Interval())
	}
	// Readiness needs this to tell "not polled yet" from "never managed to".
	if poller.StartedAt().IsZero() {
		t.Error("StartedAt is zero; readiness could not judge a poller that never succeeds")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !poller.LastSuccess().IsZero() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v, want nil on cancellation", err)
	}

	if poller.LastSuccess().IsZero() {
		t.Error("LastSuccess never advanced despite successful polls")
	}
}

// A poller that cannot reach Stripe must not report success, or readiness
// would call a dead integration healthy.
func TestPollerDoesNotRecordSuccessOnFailure(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	queries := repo.New(pool)

	lister := &fakeLister{err: errStripeDown}
	poller := NewPoller(queries, lister, time.Second, time.Minute, time.Hour)

	poller.pollOnce(context.Background())

	if !poller.LastSuccess().IsZero() {
		t.Error("a failed poll recorded success; readiness would report a broken integration as healthy")
	}
}

var errStripeDown = &stripeDownError{}

type stripeDownError struct{}

func (*stripeDownError) Error() string { return "stripe unreachable" }
