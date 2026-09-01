package payments

import "github.com/stripe/stripe-go/v83"

// HandledEventTypes is the set of Stripe events that actually drive an order
// transition. The poller uses it to filter what it fetches; actionFor decides
// what each one does.
//
// These two must not drift: an event type fetched but not routed would be
// stored and silently ignored, and one routed but not fetched would never
// arrive under polling. TestHandledEventTypesAreRouted pins that.
var HandledEventTypes = []stripe.EventType{
	stripe.EventTypePaymentIntentSucceeded,
	stripe.EventTypePaymentIntentPaymentFailed,
	stripe.EventTypePaymentIntentCanceled,
}

// eventAction is what the worker does with an event.
type eventAction int

const (
	// actionIgnore records and acknowledges the event without touching an
	// order. Stripe sends whatever an endpoint is subscribed to, and the
	// poller can be pointed at wider filters, so this is normal.
	actionIgnore eventAction = iota
	actionFulfill
	actionFail
)

func actionFor(eventType stripe.EventType) eventAction {
	switch eventType {
	case stripe.EventTypePaymentIntentSucceeded:
		return actionFulfill

	case stripe.EventTypePaymentIntentPaymentFailed, stripe.EventTypePaymentIntentCanceled:
		// Both mean the reservation should be released. A canceled intent is
		// treated as a failure rather than ignored: the buyer is not going to
		// pay, so the unit should go back on sale rather than wait for the
		// expiry sweeper.
		return actionFail

	default:
		return actionIgnore
	}
}

// eventTypeFilter renders HandledEventTypes in the []*string shape Stripe's
// list params expect.
func eventTypeFilter() []*string {
	types := make([]*string, 0, len(HandledEventTypes))
	for _, t := range HandledEventTypes {
		types = append(types, stripe.String(string(t)))
	}
	return types
}
