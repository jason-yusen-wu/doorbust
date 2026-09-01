package orders

import (
	"fmt"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
)

// Order lifecycle. These strings are also the CHECK constraint on
// orders.status (migration 00006) and part of the public API contract, so
// they cannot be renamed without a migration and a frontend change.
//
//	pending ──checkout──> awaiting_payment ──payment succeeded──> completed
//	   │                        │
//	   │                        ├──payment failed──> failed
//	   └────────────────────────┴──sweeper / DELETE──> expired | cancelled
//
// Every transition out of a non-terminal state is a guarded UPDATE, so the
// losing side of any race sees zero rows rather than corrupting the counter.
const (
	StatusPending         = "pending"
	StatusAwaitingPayment = "awaiting_payment"
	StatusCompleted       = "completed"
	StatusFailed          = "failed"
	StatusExpired         = "expired"
	StatusCancelled       = "cancelled"
)

// classifyUnfulfillable decides what a *successful* payment means for an order
// that CompleteOrder refused to complete (it matched zero rows, so the order
// was not in 'awaiting_payment').
//
// The caller — FulfillPayment, driven by the Stripe webhook worker — treats
// the result as follows:
//
//	nil    the event is marked processed cleanly. Nothing happened, nothing
//	       is owed. This is the right answer for a benign redelivery.
//	error  the event is marked processed WITH last_error set and logged at
//	       ERROR for a human to reconcile. Use this when money moved but no
//	       stock was committed, i.e. a refund is owed.
//
// Getting this wrong is expensive in both directions: return an error too
// eagerly and every at-least-once redelivery of an already-completed order
// raises a false refund alarm; return nil too eagerly and a genuinely
// unfulfilled payment disappears silently.
//
// order.Status is the current state — one of the constants above.
func classifyUnfulfillable(order repo.FindOrderByPaymentIntentIDRow) error {
	switch order.Status {
	case StatusCompleted:
		// The common case, and the only benign one. Stripe guarantees
		// at-least-once delivery, so a repeat of an event we already applied
		// is ordinary traffic. Alarming here would bury the real signals
		// below under noise proportional to Stripe's retry rate.
		return nil

	case StatusExpired, StatusCancelled:
		// The reservation was released while the payment was in flight — by
		// the sweeper, or by the buyer. The unit is back on sale and may
		// already belong to someone else, so it cannot be handed over: the
		// money has to go back instead.
		return fmt.Errorf("%w: order %d is %s", ErrPaidButNotFulfillable, order.ID, order.Status)

	case StatusFailed:
		// A failure and a success for the same intent. Whichever arrived
		// first, stock was already released, so the same refund is owed —
		// but the contradiction is worth naming separately, since it usually
		// means events were applied out of order rather than late.
		return fmt.Errorf("%w: order %d was marked failed but its payment succeeded", ErrPaidButNotFulfillable, order.ID)

	case StatusPending, StatusAwaitingPayment:
		// Unreachable by construction: stripe_payment_intent_id is only ever
		// set by MarkOrderAwaitingPayment, and this function is only called
		// after CompleteOrder found no row in 'awaiting_payment'. Reaching
		// either means the state machine is not behaving as designed, which
		// is worse than a late payment, not better.
		return fmt.Errorf("%w: order %d is unexpectedly %s", ErrPaidButNotFulfillable, order.ID, order.Status)

	default:
		// A status the CHECK constraint permits but this code does not know
		// about — almost certainly a migration that added a state without
		// updating this switch. Unknowns around money get surfaced.
		return fmt.Errorf("%w: order %d has unrecognized status %q", ErrPaidButNotFulfillable, order.ID, order.Status)
	}
}
