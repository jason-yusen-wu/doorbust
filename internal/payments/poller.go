package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/stripe/stripe-go/v83"
)

// Poller pulls events from Stripe's API into the stripe_events inbox, instead
// of waiting for Stripe to push them.
//
// This is the production delivery path, and it exists because of what this
// deployment is: one EC2 box on an ephemeral public IP, with no Elastic IP, no
// domain, no TLS terminator, and a security group that admits a single /32.
// A pushed webhook would need all four of those; pulling needs none of them,
// and needs nothing configured on Stripe's side at all.
//
// It is also strictly more robust here. A webhook's delivery depends on the
// sender's retry schedule and on us being reachable at the moment it fires;
// polling catches up by construction after a redeploy, a crash, or an outage
// of any length.
//
// The webhook handler is kept for local development (`stripe listen`). Running
// both at once is safe: they share one inbox and InsertStripeEvent's
// ON CONFLICT (id) DO NOTHING makes a duplicate a no-op.
type Poller struct {
	repo   repo.Querier
	events eventLister

	interval time.Duration
	// overlap re-scans a window behind the cursor on every tick. See sinceFor.
	overlap time.Duration
	// initialLookback bounds the very first poll, when the inbox is empty, so
	// a fresh database doesn't replay Stripe's entire retained history.
	initialLookback time.Duration
}

// eventLister is the slice of the Stripe client this poller needs. Declared
// here as an interface — the same way internal/orders declares PaymentGateway
// rather than importing a concrete client — so the polling logic is testable
// without a network or an API key.
type eventLister interface {
	List(ctx context.Context, params *stripe.EventListParams) stripe.Seq2[*stripe.Event, error]
}

func NewPoller(repo repo.Querier, events eventLister, interval, overlap, initialLookback time.Duration) *Poller {
	return &Poller{
		repo:            repo,
		events:          events,
		interval:        interval,
		overlap:         overlap,
		initialLookback: initialLookback,
	}
}

// Run blocks until ctx is cancelled. Errors are logged and retried on the next
// tick: a Stripe blip or a database hiccup should not take the process down,
// and nothing is lost by waiting, because the cursor is derived from what was
// actually stored.
func (p *Poller) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	slog.Info("stripe event poller started",
		"interval", p.interval, "overlap", p.overlap, "initialLookback", p.initialLookback)

	// Poll once immediately rather than waiting a full interval, so a restart
	// picks up anything missed while the process was down without delay.
	p.pollOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("stripe event poller stopped")
			return nil
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context) {
	fetched, inserted, err := p.poll(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("stripe event poll failed", "error", err)
		}
		return
	}
	if inserted > 0 {
		slog.Info("recorded stripe events", "inserted", inserted, "fetched", fetched)
	}
}

// poll fetches everything created since the cursor and records what is new.
func (p *Poller) poll(ctx context.Context) (fetched, inserted int, err error) {
	cursor, err := p.repo.LatestStripeEventCreatedAt(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read poll cursor: %w", err)
	}

	since := p.sinceFor(cursor, time.Now())

	params := &stripe.EventListParams{
		Types:        eventTypeFilter(),
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()},
	}

	// List auto-paginates; the iterator yields one event at a time.
	for event, listErr := range p.events.List(ctx, params) {
		if listErr != nil {
			// Return what was already recorded rather than discarding it —
			// those rows are durable and the next tick resumes past them.
			return fetched, inserted, fmt.Errorf("list stripe events: %w", listErr)
		}
		fetched++

		payload, err := eventPayload(event)
		if err != nil {
			// One malformed event must not stall the whole poll.
			slog.Error("could not encode stripe event", "event", event.ID, "error", err)
			continue
		}

		rows, err := p.repo.InsertStripeEvent(ctx, repo.InsertStripeEventParams{
			ID:              event.ID,
			Type:            string(event.Type),
			Payload:         payload,
			StripeCreatedAt: pgtype.Timestamptz{Time: time.Unix(event.Created, 0).UTC(), Valid: true},
		})
		if err != nil {
			return fetched, inserted, fmt.Errorf("record stripe event %s: %w", event.ID, err)
		}
		inserted += int(rows)
	}

	return fetched, inserted, nil
}

// sinceFor turns the stored cursor into the lower bound for the next fetch.
//
// The overlap is load-bearing, not padding. The cursor is a timestamp rather
// than a strict sequence, and Stripe's list can reflect an event slightly
// after another with an earlier created time; resuming from exactly the last
// timestamp would step over such an event permanently. Re-scanning a window
// costs nothing because the insert dedups on Stripe's own event id.
//
// When the inbox is empty the cursor is NULL, and the window is bounded by
// initialLookback so a fresh database does not replay all retained history.
func (p *Poller) sinceFor(cursor pgtype.Timestamptz, now time.Time) time.Time {
	if !cursor.Valid {
		return now.Add(-p.initialLookback)
	}
	return cursor.Time.Add(-p.overlap)
}

// eventPayload renders an event into the same shape the webhook handler
// stores, which is what the worker's paymentIntentID reads (data.object.id).
//
// The stripe.Event struct is not marshalled directly: it embeds APIResource
// and carries the raw object under a field tagged "-" for output, so a
// round-trip through it would not reproduce data.object.
func eventPayload(event *stripe.Event) ([]byte, error) {
	if event.Data == nil || len(event.Data.Raw) == 0 {
		return nil, fmt.Errorf("stripe event %s has no data object", event.ID)
	}

	return json.Marshal(struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Created int64  `json:"created"`
		Data    struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}{
		ID:      event.ID,
		Type:    string(event.Type),
		Created: event.Created,
		Data: struct {
			Object json.RawMessage `json:"object"`
		}{Object: json.RawMessage(event.Data.Raw)},
	})
}
