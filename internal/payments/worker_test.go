package payments

import "testing"

// The worker stores the whole event as Stripe sent it, so the payment intent
// lives at data.object.id, not at the top level. Getting that nesting wrong
// would leave every webhook unmatched to an order, which is exactly the kind
// of failure that only shows up in production.
func TestPaymentIntentID(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		wantErr bool
	}{
		{
			name: "extracts id from data.object",
			payload: `{
				"id": "evt_123",
				"type": "payment_intent.succeeded",
				"data": {"object": {"id": "pi_456", "object": "payment_intent", "amount": 1999}}
			}`,
			want: "pi_456",
		},
		{
			// The event id must never be mistaken for the intent id.
			name: "does not fall back to the event id",
			payload: `{
				"id": "evt_123",
				"type": "payment_intent.succeeded",
				"data": {"object": {"object": "payment_intent"}}
			}`,
			wantErr: true,
		},
		{
			name:    "rejects an event with no data",
			payload: `{"id": "evt_123", "type": "payment_intent.succeeded"}`,
			wantErr: true,
		},
		{
			name:    "rejects malformed json",
			payload: `{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := paymentIntentID([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
