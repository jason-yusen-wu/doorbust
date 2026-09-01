// Package payments is the Stripe adapter: it implements the payment gateway
// interface internal/orders declares, receives Stripe's webhooks, and drains
// them from a durable inbox.
//
// The split into two halves is the point. The synchronous half (checkout)
// only creates a PaymentIntent and hands its client secret back; it never
// touches inventory. The asynchronous half (webhook + worker) is what turns a
// confirmed payment into a completed order. Nothing in this package holds a
// database transaction open across a call to Stripe.
package payments

import (
	"context"
	"fmt"

	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/stripe/stripe-go/v83"
)

// StripeGateway implements orders.PaymentGateway.
type StripeGateway struct {
	client   *stripe.Client
	currency string
}

func NewStripeGateway(secretKey, currency string) *StripeGateway {
	return &StripeGateway{
		client:   stripe.NewClient(secretKey),
		currency: currency,
	}
}

// Events exposes the client's event-list service to the poller. Returning the
// narrow eventLister rather than the concrete service keeps the dependency
// pointing at the interface this package declares.
//
// The compile-time assertion is the point: it fails here, at wiring time, if
// the SDK's signature drifts from what the poller expects.
func (g *StripeGateway) Events() eventLister {
	var lister eventLister = g.client.V1Events
	return lister
}

// CreatePaymentIntent asks Stripe for an intent the frontend can confirm.
//
// The idempotency key is derived from the order id, not generated per call.
// A retried checkout — client retry, proxy timeout, restart mid-flight — then
// returns the *same* intent rather than creating a second one, so a single
// order can never end up with two chargeable intents attached to it.
func (g *StripeGateway) CreatePaymentIntent(ctx context.Context, p orders.PaymentIntentParams) (orders.PaymentIntent, error) {
	params := &stripe.PaymentIntentCreateParams{
		Amount:   stripe.Int64(p.AmountInCents),
		Currency: stripe.String(g.currency),
		AutomaticPaymentMethods: &stripe.PaymentIntentCreateAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
		Description: stripe.String(fmt.Sprintf("doorbust order %d", p.OrderID)),
	}
	params.SetIdempotencyKey(idempotencyKey(p.OrderID))

	// Metadata is what makes a Stripe dashboard row traceable back to an
	// order without a lookup table.
	params.AddMetadata("order_id", fmt.Sprintf("%d", p.OrderID))
	if p.CustomerEmail != "" {
		params.ReceiptEmail = stripe.String(p.CustomerEmail)
	}

	intent, err := g.client.V1PaymentIntents.Create(ctx, params)
	if err != nil {
		return orders.PaymentIntent{}, err
	}

	return orders.PaymentIntent{
		ID:           intent.ID,
		ClientSecret: intent.ClientSecret,
	}, nil
}

func idempotencyKey(orderID int64) string {
	return fmt.Sprintf("doorbust-order-%d", orderID)
}
