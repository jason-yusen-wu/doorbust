package payments

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/stripe/stripe-go/v83"
)

// fakeLister stands in for Stripe's event service.
type fakeLister struct {
	events []*stripe.Event
	err    error

	gotSince int64
	gotTypes []string
	calls    int
}

func (f *fakeLister) List(_ context.Context, params *stripe.EventListParams) stripe.Seq2[*stripe.Event, error] {
	f.calls++
	if params.CreatedRange != nil {
		f.gotSince = params.CreatedRange.GreaterThanOrEqual
	}
	f.gotTypes = nil
	for _, t := range params.Types {
		f.gotTypes = append(f.gotTypes, *t)
	}

	return func(yield func(*stripe.Event, error) bool) {
		for _, e := range f.events {
			if !yield(e, nil) {
				return
			}
		}
		if f.err != nil {
			yield(nil, f.err)
		}
	}
}

// fakeRepo records inserts and serves a cursor. Only the two methods the
// poller uses are implemented; the rest of Querier is embedded and unused.
type fakeRepo struct {
	repo.Querier

	cursor    pgtype.Timestamptz
	cursorErr error

	inserted  []repo.InsertStripeEventParams
	insertErr error
	// seen models the ON CONFLICT DO NOTHING dedup.
	seen map[string]bool
}

func newFakeRepo() *fakeRepo { return &fakeRepo{seen: map[string]bool{}} }

func (f *fakeRepo) LatestStripeEventCreatedAt(context.Context) (pgtype.Timestamptz, error) {
	return f.cursor, f.cursorErr
}

func (f *fakeRepo) InsertStripeEvent(_ context.Context, arg repo.InsertStripeEventParams) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.inserted = append(f.inserted, arg)
	if f.seen[arg.ID] {
		return 0, nil // duplicate
	}
	f.seen[arg.ID] = true
	return 1, nil
}

func testEvent(id string, eventType stripe.EventType, created int64, intentID string) *stripe.Event {
	return &stripe.Event{
		ID:      id,
		Type:    eventType,
		Created: created,
		Data:    &stripe.EventData{Raw: json.RawMessage(`{"id":"` + intentID + `","object":"payment_intent"}`)},
	}
}

// The overlap is the whole reason events don't get skipped. If this arithmetic
// is wrong the failure is silent and permanent — a payment simply never lands.
func TestPollerSinceFor(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	p := NewPoller(nil, nil, 10*time.Second, 2*time.Minute, time.Hour)

	t.Run("empty inbox uses the bounded initial lookback", func(t *testing.T) {
		got := p.sinceFor(pgtype.Timestamptz{}, now)
		want := now.Add(-time.Hour)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("existing cursor is rewound by the overlap", func(t *testing.T) {
		cursor := now.Add(-30 * time.Second)
		got := p.sinceFor(pgtype.Timestamptz{Time: cursor, Valid: true}, now)
		want := cursor.Add(-2 * time.Minute)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
		if !got.Before(cursor) {
			t.Error("since must be strictly before the cursor, or events can be skipped")
		}
	})

	t.Run("a long outage still resumes from the cursor, not from now", func(t *testing.T) {
		// The property webhooks would not give us: after being down for a
		// week, the window starts where we left off.
		cursor := now.Add(-7 * 24 * time.Hour)
		got := p.sinceFor(pgtype.Timestamptz{Time: cursor, Valid: true}, now)
		if !got.Before(now.Add(-6 * 24 * time.Hour)) {
			t.Errorf("got %v; a long outage must not collapse the window to recent history", got)
		}
	})
}

func TestPollerRecordsEvents(t *testing.T) {
	ctx := context.Background()
	cursor := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

	r := newFakeRepo()
	r.cursor = pgtype.Timestamptz{Time: cursor, Valid: true}

	lister := &fakeLister{events: []*stripe.Event{
		testEvent("evt_1", stripe.EventTypePaymentIntentSucceeded, cursor.Unix()+10, "pi_1"),
		testEvent("evt_2", stripe.EventTypePaymentIntentPaymentFailed, cursor.Unix()+20, "pi_2"),
	}}

	p := NewPoller(r, lister, time.Second, 2*time.Minute, time.Hour)

	fetched, inserted, err := p.poll(ctx)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if fetched != 2 || inserted != 2 {
		t.Errorf("fetched=%d inserted=%d, want 2/2", fetched, inserted)
	}

	// Only the types the worker can route are requested.
	if len(lister.gotTypes) != len(HandledEventTypes) {
		t.Errorf("requested %d types, want %d", len(lister.gotTypes), len(HandledEventTypes))
	}

	if want := cursor.Add(-2 * time.Minute).Unix(); lister.gotSince != want {
		t.Errorf("since = %d, want %d", lister.gotSince, want)
	}

	// Stripe's timestamp must be stored, not ours — it is the next cursor.
	if got := r.inserted[0].StripeCreatedAt; !got.Valid || got.Time.Unix() != cursor.Unix()+10 {
		t.Errorf("stripe_created_at = %v, want unix %d", got, cursor.Unix()+10)
	}
}

// Re-scanning the overlap window must be free. If a redelivered event counted
// as new, the poller would look like it was making progress when it wasn't.
func TestPollerDedupsOnRescan(t *testing.T) {
	ctx := context.Background()
	created := time.Now().Add(-time.Minute)

	r := newFakeRepo()
	lister := &fakeLister{events: []*stripe.Event{
		testEvent("evt_dup", stripe.EventTypePaymentIntentSucceeded, created.Unix(), "pi_1"),
	}}
	p := NewPoller(r, lister, time.Second, 2*time.Minute, time.Hour)

	if _, inserted, err := p.poll(ctx); err != nil || inserted != 1 {
		t.Fatalf("first poll: inserted=%d err=%v, want 1/nil", inserted, err)
	}
	if _, inserted, err := p.poll(ctx); err != nil || inserted != 0 {
		t.Fatalf("re-scan: inserted=%d err=%v, want 0/nil", inserted, err)
	}
}

// A mid-page Stripe error must not discard the rows already written.
func TestPollerKeepsProgressOnListError(t *testing.T) {
	r := newFakeRepo()
	lister := &fakeLister{
		events: []*stripe.Event{
			testEvent("evt_1", stripe.EventTypePaymentIntentSucceeded, time.Now().Unix(), "pi_1"),
		},
		err: errors.New("stripe unavailable"),
	}
	p := NewPoller(r, lister, time.Second, 2*time.Minute, time.Hour)

	_, inserted, err := p.poll(context.Background())
	if err == nil {
		t.Fatal("expected the list error to surface")
	}
	if inserted != 1 {
		t.Errorf("inserted = %d, want 1 (progress before the error must be kept)", inserted)
	}
}

// The stored payload must match what the worker parses, or every polled event
// would be recorded and then fail to match an order.
func TestEventPayloadMatchesWorkerExpectation(t *testing.T) {
	event := testEvent("evt_1", stripe.EventTypePaymentIntentSucceeded, 1_700_000_000, "pi_abc123")

	payload, err := eventPayload(event)
	if err != nil {
		t.Fatalf("eventPayload: %v", err)
	}

	// This is the actual coupling being tested: the worker's own parser.
	got, err := paymentIntentID(payload)
	if err != nil {
		t.Fatalf("worker could not parse the poller's payload: %v", err)
	}
	if got != "pi_abc123" {
		t.Errorf("payment intent id = %q, want pi_abc123", got)
	}

	var envelope struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}
	if envelope.ID != "evt_1" || envelope.Type != string(stripe.EventTypePaymentIntentSucceeded) {
		t.Errorf("envelope = %+v, want the event id and type preserved", envelope)
	}
}

func TestEventPayloadRejectsMissingData(t *testing.T) {
	if _, err := eventPayload(&stripe.Event{ID: "evt_1"}); err == nil {
		t.Error("expected an error for an event with no data object")
	}
}

// The poller's fetch filter and the worker's routing table are two halves of
// one decision. If they drift, an event is either fetched and never acted on,
// or acted on but never fetched.
func TestHandledEventTypesAreRouted(t *testing.T) {
	for _, eventType := range HandledEventTypes {
		if actionFor(eventType) == actionIgnore {
			t.Errorf("%s is fetched by the poller but ignored by the worker", eventType)
		}
	}

	if actionFor("customer.created") != actionIgnore {
		t.Error("an unrelated event type should be ignored, not routed")
	}
}
