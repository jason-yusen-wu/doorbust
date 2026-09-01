package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/jason-yusen-wu/doorbust/internal/payments"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
	"github.com/stripe/stripe-go/v83"
)

// The whole buy flow, end to end through the real router.
//
// This is what makes a production purchase smoke test unnecessary: reserve →
// checkout → payment event → worker → completed is proven on every run, with
// real HTTP, a real database and a real order state machine.

// signedWebhook builds a request carrying a valid Stripe signature. The
// signature covers the exact bytes, which is why the handler must read the raw
// body before decoding anything.
func signedWebhook(t *testing.T, secret, payload string) *http.Request {
	t.Helper()

	timestamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", timestamp, payload)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature",
		fmt.Sprintf("t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil))))
	return req
}

// paymentEventJSON builds an event body.
//
// api_version is not decoration: ConstructEvent rejects an event whose API
// version release train differs from the one stripe-go is pinned to. Omitting
// it makes every webhook fail with a bare "invalid signature"-shaped error.
// See TestWebhookRejectsIncompatibleAPIVersion.
func paymentEventJSON(eventID, eventType, intentID string) string {
	return paymentEventJSONWithVersion(eventID, eventType, intentID, stripe.APIVersion)
}

// The top-level "object":"event" is also required: stripe-go v83 refuses to
// parse a body without it, on the assumption it is a v2 EventNotification
// meant for a different API. A payload missing it fails with an error about
// ParseEventNotification rather than anything about signatures.
func paymentEventJSONWithVersion(eventID, eventType, intentID, apiVersion string) string {
	return fmt.Sprintf(
		`{"id":%q,"object":"event","type":%q,"api_version":%q,"created":%d,`+
			`"data":{"object":{"id":%q,"object":"payment_intent"}}}`,
		eventID, eventType, apiVersion, time.Now().Unix(), intentID,
	)
}

func TestFullPurchaseFlow(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	buyerToken, _ := h.issuer.Buyer(t, 1)
	productID := testsupport.SeedProduct(t, h.db, "flagship", 12999, 2)

	// --- reserve -------------------------------------------------------
	rec := h.do(t, http.MethodPost, "/orders", buyerToken, fmt.Sprintf(`{"product_id":%d}`, productID))
	testsupport.AssertStatus(t, rec, http.StatusCreated)

	var reserved struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	testsupport.DecodeJSON(t, rec, &reserved)

	if reserved.Status != orders.StatusPending {
		t.Fatalf("status after reserve = %q, want pending", reserved.Status)
	}
	testsupport.AssertStock(t, h.db, productID, 2, 1)

	// --- checkout ------------------------------------------------------
	rec = h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/checkout", reserved.ID), buyerToken, "")
	testsupport.AssertStatus(t, rec, http.StatusOK)
	testsupport.AssertJSONKeys(t, rec.Body.Bytes(), []string{"order", "client_secret"})

	var checkout struct {
		ClientSecret string `json:"client_secret"`
		Order        struct {
			Status string `json:"status"`
		} `json:"order"`
	}
	testsupport.DecodeJSON(t, rec, &checkout)

	if checkout.ClientSecret == "" {
		t.Fatal("checkout returned no client_secret; the frontend cannot confirm payment")
	}
	if checkout.Order.Status != orders.StatusAwaitingPayment {
		t.Errorf("status after checkout = %q, want %q", checkout.Order.Status, orders.StatusAwaitingPayment)
	}

	// Checkout must NOT consume stock — that is the entire point of the two
	// halves. The unit stays reserved until a payment is confirmed.
	testsupport.AssertStock(t, h.db, productID, 2, 1)

	// What we actually sent Stripe. Stubbing at the HTTP layer rather than
	// behind the gateway interface is what makes this assertable at all.
	sent := h.stripe.LastRequest(t)
	if sent.Path != "/v1/payment_intents" {
		t.Errorf("called %s, want /v1/payment_intents", sent.Path)
	}
	if got := sent.Form.Get("amount"); got != "12999" {
		t.Errorf("amount = %q, want 12999 (the price snapshotted on the order)", got)
	}
	if got := sent.Form.Get("currency"); got != "usd" {
		t.Errorf("currency = %q, want usd", got)
	}
	// Derived from the order id, so a retried checkout returns the same
	// intent instead of attaching a second chargeable one.
	if want := fmt.Sprintf("doorbust-order-%d", reserved.ID); sent.IdempotencyKey != want {
		t.Errorf("Idempotency-Key = %q, want %q", sent.IdempotencyKey, want)
	}
	if got := sent.Form.Get("metadata[order_id]"); got != fmt.Sprint(reserved.ID) {
		t.Errorf("metadata[order_id] = %q, want %d", got, reserved.ID)
	}

	// --- payment confirmed ---------------------------------------------
	order := testsupport.Order(t, h.db, reserved.ID)
	intentID := order.StripePaymentIntentID.String
	if intentID == "" {
		t.Fatal("no payment intent recorded on the order")
	}

	webhook := signedWebhook(t, testWebhookSecret,
		paymentEventJSON("evt_purchase_1", string(stripe.EventTypePaymentIntentSucceeded), intentID))

	rec = httptest.NewRecorder()
	h.router.ServeHTTP(rec, webhook)
	testsupport.AssertStatus(t, rec, http.StatusOK)

	// The webhook only records; nothing has been applied yet.
	testsupport.AssertOrderStatus(t, h.db, reserved.ID, orders.StatusAwaitingPayment)
	if n := testsupport.CountRows(t, h.db, "stripe_events"); n != 1 {
		t.Fatalf("stripe_events = %d, want 1", n)
	}

	// --- worker applies it ---------------------------------------------
	drainWorker(t, h)

	testsupport.AssertOrderStatus(t, h.db, reserved.ID, orders.StatusCompleted)
	// Now the sale is real: one unit leaves quantity and the reservation is
	// released.
	testsupport.AssertStock(t, h.db, productID, 1, 0)

	// --- redelivery is a no-op -----------------------------------------
	replay := signedWebhook(t, testWebhookSecret,
		paymentEventJSON("evt_purchase_1", string(stripe.EventTypePaymentIntentSucceeded), intentID))
	rec = httptest.NewRecorder()
	h.router.ServeHTTP(rec, replay)
	testsupport.AssertStatus(t, rec, http.StatusOK)

	if n := testsupport.CountRows(t, h.db, "stripe_events"); n != 1 {
		t.Errorf("stripe_events = %d after redelivery, want 1 (dedup on Stripe's event id)", n)
	}

	drainWorker(t, h)
	testsupport.AssertStock(t, h.db, productID, 1, 0)
}

// drainWorker runs the outbox worker until the queue is empty. Run() is a
// ticker loop, so tests drive it with a short interval and a cancelled context
// rather than sleeping.
func drainWorker(t *testing.T, h *harness) {
	t.Helper()

	worker := payments.NewWorker(
		repo.New(h.db), h.db,
		orders.NewService(repo.New(h.db), h.db, nil, 15*time.Minute),
		time.Millisecond, 5,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = worker.Run(ctx)
	}()

	// Wait for the inbox to drain rather than for a fixed duration.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var unprocessed int
		if err := h.db.QueryRow(context.Background(),
			`SELECT count(*) FROM stripe_events WHERE processed_at IS NULL`).Scan(&unprocessed); err != nil {
			t.Fatalf("count unprocessed events: %v", err)
		}
		if unprocessed == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done
}

func TestPaymentFailureReleasesStock(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	buyerToken, _ := h.issuer.Buyer(t, 1)
	productID := testsupport.SeedProduct(t, h.db, "declined-sku", 500, 1)

	rec := h.do(t, http.MethodPost, "/orders", buyerToken, fmt.Sprintf(`{"product_id":%d}`, productID))
	testsupport.AssertStatus(t, rec, http.StatusCreated)

	var reserved struct {
		ID int64 `json:"id"`
	}
	testsupport.DecodeJSON(t, rec, &reserved)

	rec = h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/checkout", reserved.ID), buyerToken, "")
	testsupport.AssertStatus(t, rec, http.StatusOK)

	intentID := testsupport.Order(t, h.db, reserved.ID).StripePaymentIntentID.String

	webhook := signedWebhook(t, testWebhookSecret,
		paymentEventJSON("evt_failed_1", string(stripe.EventTypePaymentIntentPaymentFailed), intentID))
	rec = httptest.NewRecorder()
	h.router.ServeHTTP(rec, webhook)
	testsupport.AssertStatus(t, rec, http.StatusOK)

	drainWorker(t, h)

	testsupport.AssertOrderStatus(t, h.db, reserved.ID, orders.StatusFailed)
	// The unit must go back on sale, with quantity untouched — a failed
	// payment is not a sale.
	testsupport.AssertStock(t, h.db, productID, 1, 0)
}

func TestCheckoutRejectsGatewayFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.stripe.FailPaymentIntents = true

	buyerToken, _ := h.issuer.Buyer(t, 1)
	productID := testsupport.SeedProduct(t, h.db, "gateway-down", 999, 1)

	rec := h.do(t, http.MethodPost, "/orders", buyerToken, fmt.Sprintf(`{"product_id":%d}`, productID))
	testsupport.AssertStatus(t, rec, http.StatusCreated)

	var reserved struct {
		ID int64 `json:"id"`
	}
	testsupport.DecodeJSON(t, rec, &reserved)

	rec = h.do(t, http.MethodPost, fmt.Sprintf("/orders/%d/checkout", reserved.ID), buyerToken, "")

	// A declined intent is our failure to report, not the caller's mistake.
	testsupport.AssertErrorEnvelope(t, rec, http.StatusInternalServerError, "internal_error")

	// The order must stay pending and keep its reservation: nothing was
	// charged, so nothing should have advanced.
	testsupport.AssertOrderStatus(t, h.db, reserved.ID, orders.StatusPending)
	testsupport.AssertStock(t, h.db, productID, 1, 1)

	// A 5xx must never carry the underlying error.
	if strings.Contains(rec.Body.String(), "card_declined") {
		t.Errorf("500 body leaked the gateway error: %s", rec.Body)
	}
}

func TestCancelReleasesReservation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	buyerToken, _ := h.issuer.Buyer(t, 1)
	productID := testsupport.SeedProduct(t, h.db, "cancel-sku", 750, 1)

	rec := h.do(t, http.MethodPost, "/orders", buyerToken, fmt.Sprintf(`{"product_id":%d}`, productID))
	testsupport.AssertStatus(t, rec, http.StatusCreated)

	var reserved struct {
		ID int64 `json:"id"`
	}
	testsupport.DecodeJSON(t, rec, &reserved)
	testsupport.AssertStock(t, h.db, productID, 1, 1)

	rec = h.do(t, http.MethodDelete, fmt.Sprintf("/orders/%d", reserved.ID), buyerToken, "")
	testsupport.AssertStatus(t, rec, http.StatusOK)

	testsupport.AssertOrderStatus(t, h.db, reserved.ID, orders.StatusCancelled)
	testsupport.AssertStock(t, h.db, productID, 1, 0)

	t.Run("cancelling twice is a conflict, not a second release", func(t *testing.T) {
		rec := h.do(t, http.MethodDelete, fmt.Sprintf("/orders/%d", reserved.ID), buyerToken, "")
		testsupport.AssertErrorEnvelope(t, rec, http.StatusConflict, "order_not_pending")
		testsupport.AssertStock(t, h.db, productID, 1, 0)
	})
}

// The webhook is a public, unauthenticated endpoint; its signature is the only
// thing standing between the internet and the order state machine.
func TestWebhookSignatureVerification(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	payload := paymentEventJSON("evt_sig_1", string(stripe.EventTypePaymentIntentSucceeded), "pi_whatever")

	t.Run("valid signature is accepted", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, signedWebhook(t, testWebhookSecret, payload))
		testsupport.AssertStatus(t, rec, http.StatusOK)
	})

	t.Run("missing signature is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, req)
		testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("signature from the wrong secret is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, signedWebhook(t, "whsec_not_our_secret", payload))
		testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("tampered body invalidates a good signature", func(t *testing.T) {
		// A signature computed over the original bytes, sent with modified
		// bytes — the exact attack the raw-body requirement exists to stop.
		tampered := strings.Replace(payload, "pi_whatever", "pi_attacker", 1)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(tampered))
		req.Header = signedWebhook(t, testWebhookSecret, payload).Header

		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, req)
		testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
	})
}

// A trap worth pinning: ConstructEvent rejects an event whose API version
// release train differs from the one stripe-go is compiled against, even when
// the signature is perfectly valid.
//
// This only affects the webhook path — production pulls events from the API
// instead — but it means a `stripe listen` session against an account pinned
// to an older API version will see every event rejected, with an error that
// says nothing about versions. Bumping stripe-go can start this happening
// without any change to our code.
func TestWebhookRejectsIncompatibleAPIVersion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	payload := paymentEventJSONWithVersion(
		"evt_oldver_1",
		string(stripe.EventTypePaymentIntentSucceeded),
		"pi_whatever",
		"2019-05-16", // a pre-release-train version, always incompatible
	)

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, signedWebhook(t, testWebhookSecret, payload))

	testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")

	if n := testsupport.CountRows(t, h.db, "stripe_events"); n != 0 {
		t.Errorf("stripe_events = %d; a rejected event must not be recorded", n)
	}
}
