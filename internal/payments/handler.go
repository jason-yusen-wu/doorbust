package payments

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/json"
	"github.com/stripe/stripe-go/v83/webhook"
)

// maxWebhookBody caps the payload this unauthenticated endpoint will read.
const maxWebhookBody = 1 << 20 // 1 MiB

// HTTP methods live here
type handler struct {
	repo          repo.Querier
	webhookSecret string
}

func NewHandler(repo repo.Querier, webhookSecret string) *handler {
	return &handler{
		repo:          repo,
		webhookSecret: webhookSecret,
	}
}

// HandleWebhook records a verified Stripe event and returns immediately.
//
// It deliberately does no business logic. Stripe retries on a slow or failed
// response, so doing the order work inline would turn one payment into
// several delivery attempts under load — and would put an unauthenticated
// caller in charge of how long a database transaction stays open. The worker
// applies the effect instead.
//
// This route is public: Stripe has no Cognito token. The signature check is
// the authentication.
func (h *handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// The signature is computed over the exact bytes Stripe sent, so the body
	// must be read raw — decoding and re-encoding it would invalidate it.
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "could not read request body")
		return
	}

	event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), h.webhookSecret)
	if err != nil {
		// Anyone can POST here, so a bad signature is expected traffic, not
		// an incident. Log it without the payload.
		slog.Warn("rejected stripe webhook", "error", err)
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "invalid signature")
		return
	}

	// The whole event is stored, not just the object, so a failed delivery
	// can be inspected or replayed exactly as Stripe sent it.
	//
	// stripe_created_at is set here as well as in the poller: the two paths
	// share one inbox, and the poller's cursor is max() over this column, so
	// a webhook-delivered row must carry it too or it would be invisible to
	// the cursor.
	inserted, err := h.repo.InsertStripeEvent(r.Context(), repo.InsertStripeEventParams{
		ID:              event.ID,
		Type:            string(event.Type),
		Payload:         payload,
		StripeCreatedAt: pgtype.Timestamptz{Time: time.Unix(event.Created, 0).UTC(), Valid: true},
	})
	if err != nil {
		// Returning 5xx asks Stripe to retry, which is what we want: the
		// event is not durable yet.
		slog.Error("could not record stripe event", "event", event.ID, "error", err)
		json.WriteInternalError(w)
		return
	}

	if inserted == 0 {
		// ON CONFLICT DO NOTHING matched: Stripe delivered this event before.
		// Already-recorded is a success, not a conflict — telling Stripe
		// otherwise would just make it retry a third time.
		slog.Info("ignored redelivered stripe event", "event", event.ID, "type", event.Type)
	}

	json.Write(w, http.StatusOK, map[string]string{"status": "received"})
}
