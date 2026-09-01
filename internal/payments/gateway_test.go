package payments

import (
	"context"
	"strings"
	"testing"

	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// The gateway is stubbed at the HTTP layer, so these assert the request we
// actually put on the wire. A fake PaymentGateway would accept a checkout that
// charged the wrong amount or omitted the idempotency key; this would not.

func TestCreatePaymentIntent(t *testing.T) {
	t.Parallel()

	api := testsupport.NewStripeAPI(t)
	gateway := NewStripeGateway("sk_test_stub", "usd", api.Options()...)

	intent, err := gateway.CreatePaymentIntent(context.Background(), orders.PaymentIntentParams{
		OrderID:       42,
		AmountInCents: 1999,
		CustomerEmail: "buyer@example.test",
	})
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}

	if intent.ID == "" || intent.ClientSecret == "" {
		t.Errorf("got %+v, want an id and a client secret", intent)
	}

	sent := api.LastRequest(t)
	if sent.Path != "/v1/payment_intents" {
		t.Errorf("path = %q, want /v1/payment_intents", sent.Path)
	}
	if got := sent.Form.Get("amount"); got != "1999" {
		t.Errorf("amount = %q, want 1999", got)
	}
	if got := sent.Form.Get("currency"); got != "usd" {
		t.Errorf("currency = %q, want usd", got)
	}
	// Derived from the order id, so a retried checkout returns the same intent
	// instead of attaching a second chargeable one to the order.
	if got, want := sent.IdempotencyKey, idempotencyKey(42); got != want {
		t.Errorf("Idempotency-Key = %q, want %q", got, want)
	}
	// What makes a Stripe dashboard row traceable back to an order.
	if got := sent.Form.Get("metadata[order_id]"); got != "42" {
		t.Errorf("metadata[order_id] = %q, want 42", got)
	}
	if got := sent.Form.Get("receipt_email"); got != "buyer@example.test" {
		t.Errorf("receipt_email = %q, want the buyer's address", got)
	}
	// Lets Stripe offer whatever methods are enabled in the dashboard rather
	// than hardcoding a list here.
	if got := sent.Form.Get("automatic_payment_methods[enabled]"); got != "true" {
		t.Errorf("automatic_payment_methods[enabled] = %q, want true", got)
	}
	if !strings.HasPrefix(sent.Authorization, "Bearer ") {
		t.Errorf("Authorization = %q, want a bearer key", sent.Authorization)
	}
}

func TestCreatePaymentIntentWithoutAnEmail(t *testing.T) {
	t.Parallel()

	api := testsupport.NewStripeAPI(t)
	gateway := NewStripeGateway("sk_test_stub", "usd", api.Options()...)

	if _, err := gateway.CreatePaymentIntent(context.Background(), orders.PaymentIntentParams{
		OrderID:       7,
		AmountInCents: 100,
	}); err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}

	// An absent address must be omitted rather than sent empty, which Stripe
	// rejects.
	if got := api.LastRequest(t).Form.Get("receipt_email"); got != "" {
		t.Errorf("receipt_email = %q, want it omitted", got)
	}
}

func TestCreatePaymentIntentSurfacesStripeErrors(t *testing.T) {
	t.Parallel()

	api := testsupport.NewStripeAPI(t)
	api.FailPaymentIntents = true
	gateway := NewStripeGateway("sk_test_stub", "usd", api.Options()...)

	_, err := gateway.CreatePaymentIntent(context.Background(), orders.PaymentIntentParams{
		OrderID:       1,
		AmountInCents: 100,
	})
	if err == nil {
		t.Fatal("expected a declined intent to surface as an error")
	}
}

// The idempotency key must be stable for an order and distinct between orders,
// or a retry either creates a second intent or reuses another order's.
func TestIdempotencyKey(t *testing.T) {
	t.Parallel()

	if idempotencyKey(1) != idempotencyKey(1) {
		t.Error("the key is not stable for one order")
	}
	if idempotencyKey(1) == idempotencyKey(2) {
		t.Error("two orders share an idempotency key")
	}
}

// Events is the seam the poller is built on. The compile-time assertion inside
// it is the real check; this proves the returned lister actually works.
func TestGatewayEvents(t *testing.T) {
	t.Parallel()

	api := testsupport.NewStripeAPI(t)
	gateway := NewStripeGateway("sk_test_stub", "usd", api.Options()...)

	lister := gateway.Events()
	if lister == nil {
		t.Fatal("Events returned nil; the poller could not be wired")
	}

	seen := 0
	for _, err := range lister.List(context.Background(), nil) {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		seen++
	}
	if seen != 0 {
		t.Errorf("listed %d events from an empty stub, want 0", seen)
	}
	if api.LastRequest(t).Path != "/v1/events" {
		t.Errorf("path = %q, want /v1/events", api.LastRequest(t).Path)
	}
}
