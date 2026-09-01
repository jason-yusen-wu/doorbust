package orders

import (
	"errors"
	"strings"
	"testing"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
)

// classifyUnfulfillable is pure, so unlike the rest of this package's tests it
// needs no database.
//
// The asymmetry it encodes is the point: exactly one status is benign. Every
// other outcome means money moved without stock being committed, and has to
// reach a human. Widening the nil case is how a real refund goes missing.
func TestClassifyUnfulfillable(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantRefund bool
	}{
		{
			// Stripe delivers at-least-once, so this is ordinary traffic.
			name:       "completed is a benign redelivery",
			status:     StatusCompleted,
			wantRefund: false,
		},
		{
			name:       "expired needs a refund",
			status:     StatusExpired,
			wantRefund: true,
		},
		{
			name:       "cancelled needs a refund",
			status:     StatusCancelled,
			wantRefund: true,
		},
		{
			name:       "failed then succeeded needs a refund",
			status:     StatusFailed,
			wantRefund: true,
		},
		{
			name:       "pending is unreachable and must be surfaced",
			status:     StatusPending,
			wantRefund: true,
		},
		{
			name:       "awaiting_payment is unreachable and must be surfaced",
			status:     StatusAwaitingPayment,
			wantRefund: true,
		},
		{
			// A state added to the CHECK constraint without updating the
			// switch must fail loudly, not fall through to success.
			name:       "unknown status must be surfaced",
			status:     "some_future_status",
			wantRefund: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyUnfulfillable(repo.FindOrderByPaymentIntentIDRow{
				ID:     42,
				Status: tt.status,
			})

			if !tt.wantRefund {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}

			if err == nil {
				t.Fatal("got nil, want an error so the payment reaches a human")
			}
			// The worker branches on this sentinel to stamp the event
			// processed-with-error instead of retrying it forever.
			if !errors.Is(err, ErrPaidButNotFulfillable) {
				t.Errorf("got %v, want it to wrap ErrPaidButNotFulfillable", err)
			}
			// The message lands in stripe_events.last_error, which is all a
			// human has to go on when reconciling.
			if !strings.Contains(err.Error(), "42") {
				t.Errorf("error %q does not name the order id", err)
			}
		})
	}
}
