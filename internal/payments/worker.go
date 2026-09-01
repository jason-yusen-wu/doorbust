package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/stripe/stripe-go/v83"
)

// Worker drains the stripe_events inbox and applies each event to an order.
//
// Because the effect lives here rather than in the HTTP handler, it survives
// a crash: an unprocessed row is simply claimed again on the next poll. That
// is only safe because every transition it drives is a guarded UPDATE, so
// re-applying an event that already landed changes nothing.
type Worker struct {
	repo        repo.Querier
	db          repo.Beginner
	orders      orders.Service
	interval    time.Duration
	maxAttempts int32
}

func NewWorker(repo repo.Querier, db repo.Beginner, orderService orders.Service, interval time.Duration, maxAttempts int32) *Worker {
	return &Worker{
		repo:        repo,
		db:          db,
		orders:      orderService,
		interval:    interval,
		maxAttempts: maxAttempts,
	}
}

// Run blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("stripe event worker started", "interval", w.interval, "maxAttempts", w.maxAttempts)

	for {
		select {
		case <-ctx.Done():
			slog.Info("stripe event worker stopped")
			return nil
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

// drain processes events until the queue is empty, so a backlog does not
// trickle out one event per tick.
func (w *Worker) drain(ctx context.Context) {
	for {
		processed, err := w.processNext(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("stripe event drain failed", "error", err)
			}
			return
		}
		if !processed || ctx.Err() != nil {
			return
		}
	}
}

// processNext claims at most one event and applies it, reporting whether
// there was anything to do.
//
// One event per transaction: a payload that can never be applied then fails
// only itself instead of rolling back everything claimed alongside it.
func (w *Worker) processNext(ctx context.Context) (bool, error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	event, err := q.ClaimNextStripeEvent(ctx, w.maxAttempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // queue drained
		}
		return false, err
	}

	// The claim's row lock is held for the duration of apply, which keeps a
	// second worker off this event. The order work itself runs in its own
	// transaction; if that commits and this one does not, the event is
	// retried and the guarded UPDATEs make the retry a no-op.
	applyErr := w.apply(ctx, event)

	switch {
	case applyErr == nil:
		if err := q.MarkStripeEventProcessed(ctx, repo.MarkStripeEventProcessedParams{
			ID:        event.ID,
			LastError: pgtype.Text{},
		}); err != nil {
			return false, err
		}

	case errors.Is(applyErr, orders.ErrPaidButNotFulfillable), errors.Is(applyErr, pgx.ErrNoRows):
		// Terminal: retrying cannot change the outcome. Stamp it processed so
		// the worker stops re-claiming it, but keep the reason so it can be
		// found and reconciled.
		slog.Error("stripe event needs manual reconciliation",
			"event", event.ID, "type", event.Type, "error", applyErr)
		if err := q.MarkStripeEventProcessed(ctx, repo.MarkStripeEventProcessedParams{
			ID:        event.ID,
			LastError: pgtype.Text{String: applyErr.Error(), Valid: true},
		}); err != nil {
			return false, err
		}

	default:
		// Transient: leave processed_at NULL so the next poll retries, up to
		// maxAttempts.
		slog.Warn("stripe event failed, will retry",
			"event", event.ID, "type", event.Type, "attempts", event.Attempts+1, "error", applyErr)
		if err := q.MarkStripeEventFailed(ctx, repo.MarkStripeEventFailedParams{
			ID:        event.ID,
			LastError: pgtype.Text{String: applyErr.Error(), Valid: true},
		}); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	return true, nil
}

// apply routes one Stripe event to the order lifecycle. The routing table
// itself lives in events.go, shared with the poller's type filter.
func (w *Worker) apply(ctx context.Context, event repo.StripeEvent) error {
	action := actionFor(stripe.EventType(event.Type))
	if action == actionIgnore {
		// Recorded and acknowledged, not retried.
		slog.Debug("ignoring stripe event type", "event", event.ID, "type", event.Type)
		return nil
	}

	intentID, err := paymentIntentID(event.Payload)
	if err != nil {
		return err
	}

	switch action {
	case actionFulfill:
		return w.orders.FulfillPayment(ctx, intentID)
	case actionFail:
		return w.orders.FailPayment(ctx, intentID)
	default:
		return nil
	}
}

// paymentIntentID digs the intent id out of a stored event payload. The
// payload is the raw event as Stripe sent it, so the object is nested under
// data.object.
func paymentIntentID(payload []byte) (string, error) {
	var event stripe.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", fmt.Errorf("unmarshal stripe event: %w", err)
	}
	if event.Data == nil {
		return "", errors.New("stripe event has no data object")
	}

	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(event.Data.Raw, &object); err != nil {
		return "", fmt.Errorf("unmarshal payment intent: %w", err)
	}
	if object.ID == "" {
		return "", errors.New("stripe event object has no id")
	}

	return object.ID, nil
}
