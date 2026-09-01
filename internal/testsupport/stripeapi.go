package testsupport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v83"
)

// StripeAPI is a stand-in for Stripe's HTTP API.
//
// It stubs at the HTTP layer rather than behind the orders.PaymentGateway
// interface on purpose: that way tests assert what we actually put on the
// wire — the amount, the currency, the Idempotency-Key header, the order id in
// metadata. A fake interface would happily accept a checkout that sent Stripe
// the wrong amount.
type StripeAPI struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []StripeRequest
	events   []*stripe.Event

	// FailPaymentIntents makes intent creation return a Stripe error, for
	// exercising the gateway-failure path.
	FailPaymentIntents bool
}

// StripeRequest is one captured inbound call.
type StripeRequest struct {
	Method         string
	Path           string
	Form           url.Values
	IdempotencyKey string
	Authorization  string
}

// NewStripeAPI starts the stub, shut down when the test finishes.
func NewStripeAPI(t *testing.T) *StripeAPI {
	t.Helper()

	api := &StripeAPI{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/payment_intents", api.handleCreateIntent)
	mux.HandleFunc("/v1/events", api.handleListEvents)

	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)

	return api
}

// Options returns the client options that point a gateway at this stub.
func (s *StripeAPI) Options() []stripe.ClientOption {
	backends := &stripe.Backends{
		API: stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
			URL:        stripe.String(s.server.URL),
			HTTPClient: s.server.Client(),
			// Retries would multiply captured requests and make assertions
			// about what we sent ambiguous.
			MaxNetworkRetries: stripe.Int64(0),
			LeveledLogger:     nopLogger{},
		}),
	}
	return []stripe.ClientOption{stripe.WithBackends(backends)}
}

// Requests returns everything received so far.
func (s *StripeAPI) Requests() []StripeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StripeRequest(nil), s.requests...)
}

// LastRequest returns the most recent call, failing the test if none was made.
func (s *StripeAPI) LastRequest(t *testing.T) StripeRequest {
	t.Helper()

	reqs := s.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request reached the Stripe stub")
	}
	return reqs[len(reqs)-1]
}

// QueueEvent makes an event visible to the poller's /v1/events listing.
func (s *StripeAPI) QueueEvent(eventType stripe.EventType, intentID string, created time.Time) *stripe.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	event := &stripe.Event{
		ID:      fmt.Sprintf("evt_test_%d_%d", len(s.events)+1, created.UnixNano()),
		Type:    eventType,
		Created: created.Unix(),
		Data: &stripe.EventData{
			Raw: json.RawMessage(fmt.Sprintf(`{"id":%q,"object":"payment_intent"}`, intentID)),
		},
	}
	s.events = append(s.events, event)
	return event
}

func (s *StripeAPI) record(r *http.Request) {
	_ = r.ParseForm()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, StripeRequest{
		Method:         r.Method,
		Path:           r.URL.Path,
		Form:           r.PostForm,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Authorization:  r.Header.Get("Authorization"),
	})
}

func (s *StripeAPI) handleCreateIntent(w http.ResponseWriter, r *http.Request) {
	s.record(r)

	s.mu.Lock()
	fail := s.FailPaymentIntents
	n := len(s.requests)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if fail {
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"type":    "card_error",
				"code":    "card_declined",
				"message": "Your card was declined.",
			},
		})
		return
	}

	// amount must be a JSON number: the SDK unmarshals it into an int64, and
	// echoing the form value as a string fails deserialization.
	amount, _ := strconv.ParseInt(r.PostFormValue("amount"), 10, 64)

	id := fmt.Sprintf("pi_test_%d", n)
	json.NewEncoder(w).Encode(map[string]any{
		"id":            id,
		"object":        "payment_intent",
		"amount":        amount,
		"currency":      r.PostFormValue("currency"),
		"status":        "requires_payment_method",
		"client_secret": id + "_secret_test",
	})
}

func (s *StripeAPI) handleListEvents(w http.ResponseWriter, r *http.Request) {
	s.record(r)

	s.mu.Lock()
	events := append([]*stripe.Event(nil), s.events...)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object":   "list",
		"url":      "/v1/events",
		"has_more": false,
		"data":     events,
	})
}

// nopLogger silences the SDK's error logging, which otherwise writes the
// stubbed failure responses to stderr and makes real failures hard to spot.
type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}
