package payments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
	"github.com/stripe/stripe-go/v83"
)

// The worker's state machine decides whether a Stripe event is retried,
// abandoned, or flagged for a human. It runs against a real database because
// the claim uses FOR UPDATE SKIP LOCKED, which only means anything under real
// row locking.

// fakeOrders records what the worker asked for and returns a scripted result.
type fakeOrders struct {
	orders.Service

	mu        sync.Mutex
	fulfilled []string
	failed    []string

	fulfillErr error
	failErr    error
}

func (f *fakeOrders) FulfillPayment(_ context.Context, intentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fulfilled = append(f.fulfilled, intentID)
	return f.fulfillErr
}

func (f *fakeOrders) FailPayment(_ context.Context, intentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, intentID)
	return f.failErr
}

func (f *fakeOrders) calls() (fulfilled, failed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fulfilled...), append([]string(nil), f.failed...)
}

// insertEvent puts an event in the inbox exactly as the webhook or poller
// would.
func insertEvent(t *testing.T, pool *pgxpool.Pool, id, eventType, intentID string) {
	t.Helper()

	payload := fmt.Sprintf(
		`{"id":%q,"object":"event","type":%q,"created":%d,"data":{"object":{"id":%q,"object":"payment_intent"}}}`,
		id, eventType, time.Now().Unix(), intentID,
	)

	if _, err := repo.New(pool).InsertStripeEvent(context.Background(), repo.InsertStripeEventParams{
		ID:              id,
		Type:            eventType,
		Payload:         []byte(payload),
		StripeCreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("insert event %s: %v", id, err)
	}
}

func eventRow(t *testing.T, pool *pgxpool.Pool, id string) repo.StripeEvent {
	t.Helper()

	var e repo.StripeEvent
	err := pool.QueryRow(context.Background(),
		`SELECT id, type, payload, received_at, processed_at, attempts, last_error, stripe_created_at
		 FROM stripe_events WHERE id = $1`, id,
	).Scan(&e.ID, &e.Type, &e.Payload, &e.ReceivedAt, &e.ProcessedAt, &e.Attempts, &e.LastError, &e.StripeCreatedAt)
	if err != nil {
		t.Fatalf("read event %s: %v", id, err)
	}
	return e
}

func TestWorkerAppliesSucceededEvent(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	svc := &fakeOrders{}
	worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, 5)

	insertEvent(t, pool, "evt_ok", string(stripe.EventTypePaymentIntentSucceeded), "pi_ok")

	processed, err := worker.processNext(context.Background())
	if err != nil {
		t.Fatalf("processNext: %v", err)
	}
	if !processed {
		t.Fatal("processNext reported nothing to do")
	}

	fulfilled, failed := svc.calls()
	if len(fulfilled) != 1 || fulfilled[0] != "pi_ok" {
		t.Errorf("fulfilled = %v, want [pi_ok]", fulfilled)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
	}

	row := eventRow(t, pool, "evt_ok")
	if !row.ProcessedAt.Valid {
		t.Error("processed_at not stamped after a successful apply")
	}
	if row.LastError.Valid {
		t.Errorf("last_error = %q on success, want NULL", row.LastError.String)
	}
}

func TestWorkerRoutesFailureEvents(t *testing.T) {
	t.Parallel()

	for _, eventType := range []stripe.EventType{
		stripe.EventTypePaymentIntentPaymentFailed,
		// A cancelled intent means the buyer will not pay, so the unit should
		// return immediately rather than wait for the expiry sweeper.
		stripe.EventTypePaymentIntentCanceled,
	} {
		t.Run(string(eventType), func(t *testing.T) {
			t.Parallel()

			pool := testsupport.DB(t)
			svc := &fakeOrders{}
			worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, 5)

			insertEvent(t, pool, "evt_1", string(eventType), "pi_1")

			if _, err := worker.processNext(context.Background()); err != nil {
				t.Fatalf("processNext: %v", err)
			}

			_, failed := svc.calls()
			if len(failed) != 1 || failed[0] != "pi_1" {
				t.Errorf("failed = %v, want [pi_1]", failed)
			}
		})
	}
}

// An event type we do not act on must be recorded and acknowledged, not
// retried forever.
func TestWorkerIgnoresUnhandledEventTypes(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	svc := &fakeOrders{}
	worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, 5)

	insertEvent(t, pool, "evt_other", "customer.created", "cus_1")

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatalf("processNext: %v", err)
	}

	fulfilled, failed := svc.calls()
	if len(fulfilled) != 0 || len(failed) != 0 {
		t.Errorf("an unhandled type reached the order lifecycle: %v %v", fulfilled, failed)
	}
	if !eventRow(t, pool, "evt_other").ProcessedAt.Valid {
		t.Error("an ignored event was left unprocessed and will be re-claimed forever")
	}
}

func TestWorkerRetriesTransientFailures(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	svc := &fakeOrders{fulfillErr: errors.New("database is briefly unavailable")}
	worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, 5)

	insertEvent(t, pool, "evt_flaky", string(stripe.EventTypePaymentIntentSucceeded), "pi_flaky")

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatalf("processNext: %v", err)
	}

	row := eventRow(t, pool, "evt_flaky")
	// Left unprocessed on purpose so the next poll retries it.
	if row.ProcessedAt.Valid {
		t.Error("a transient failure was stamped processed; it will never be retried")
	}
	if row.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", row.Attempts)
	}
	if !row.LastError.Valid {
		t.Error("last_error not recorded for a failed apply")
	}
}

// An event that can never succeed must stop being re-claimed, or the worker
// spins on it every poll forever.
func TestWorkerStopsAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	svc := &fakeOrders{fulfillErr: errors.New("permanently broken")}

	const maxAttempts = 3
	worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, maxAttempts)

	insertEvent(t, pool, "evt_poison", string(stripe.EventTypePaymentIntentSucceeded), "pi_poison")

	ctx := context.Background()
	for i := range maxAttempts {
		processed, err := worker.processNext(ctx)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if !processed {
			t.Fatalf("attempt %d found nothing to claim, want the poison event", i+1)
		}
	}

	if row := eventRow(t, pool, "evt_poison"); row.Attempts != maxAttempts {
		t.Errorf("attempts = %d, want %d", row.Attempts, maxAttempts)
	}

	// The cap has been reached: the event is now skipped by the claim query.
	processed, err := worker.processNext(ctx)
	if err != nil {
		t.Fatalf("processNext past the cap: %v", err)
	}
	if processed {
		t.Error("the worker re-claimed an event past max_attempts; it would spin forever")
	}
}

// A terminal outcome — a payment for an order that can no longer be fulfilled
// — must be stamped processed so it stops retrying, but keep its reason so a
// human can find it and refund.
func TestWorkerFlagsUnfulfillablePaymentsForReconciliation(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	svc := &fakeOrders{fulfillErr: fmt.Errorf("order 7 is expired: %w", orders.ErrPaidButNotFulfillable)}
	worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, 5)

	insertEvent(t, pool, "evt_refund", string(stripe.EventTypePaymentIntentSucceeded), "pi_refund")

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatalf("processNext: %v", err)
	}

	row := eventRow(t, pool, "evt_refund")
	if !row.ProcessedAt.Valid {
		t.Error("a terminal outcome was left for retry; retrying cannot change it")
	}
	if !row.LastError.Valid {
		t.Fatal("last_error is NULL; the payment needing a refund would be invisible")
	}
	if row.LastError.String == "" {
		t.Error("last_error is empty")
	}
}

// FOR UPDATE SKIP LOCKED is what allows more than one worker — or a future
// second box — to drain the queue without applying an event twice.
func TestConcurrentWorkersDoNotDoubleProcess(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)

	const events = 12
	for i := range events {
		insertEvent(t, pool,
			fmt.Sprintf("evt_%02d", i),
			string(stripe.EventTypePaymentIntentSucceeded),
			fmt.Sprintf("pi_%02d", i),
		)
	}

	svc := &fakeOrders{}

	var (
		wg      sync.WaitGroup
		claimed atomic.Int64
		start   = make(chan struct{})
	)

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, 5)
			<-start

			for {
				processed, err := worker.processNext(context.Background())
				if err != nil {
					t.Errorf("processNext: %v", err)
					return
				}
				if !processed {
					return
				}
				claimed.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := claimed.Load(); got != events {
		t.Errorf("claimed %d events, want %d", got, events)
	}

	fulfilled, _ := svc.calls()
	if len(fulfilled) != events {
		t.Fatalf("applied %d events, want %d", len(fulfilled), events)
	}

	// Each intent exactly once — the property that makes multiple workers safe.
	seen := map[string]int{}
	for _, id := range fulfilled {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("intent %s applied %d times, want 1", id, n)
		}
	}
}

func TestWorkerDrainsEmptyQueueWithoutError(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	worker := NewWorker(repo.New(pool), pool, &fakeOrders{}, time.Millisecond, 5)

	processed, err := worker.processNext(context.Background())
	if err != nil {
		t.Fatalf("processNext on an empty queue: %v", err)
	}
	if processed {
		t.Error("processNext reported work on an empty queue")
	}
}

// Run is a ticker loop; it must drain what is queued and stop on cancellation.
func TestWorkerRunDrainsAndStops(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	svc := &fakeOrders{}
	worker := NewWorker(repo.New(pool), pool, svc, time.Millisecond, 5)

	for i := range 5 {
		insertEvent(t, pool,
			fmt.Sprintf("evt_run_%d", i),
			string(stripe.EventTypePaymentIntentSucceeded),
			fmt.Sprintf("pi_run_%d", i),
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fulfilled, _ := svc.calls(); len(fulfilled) == 5 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v, want nil on cancellation", err)
	}

	if fulfilled, _ := svc.calls(); len(fulfilled) != 5 {
		t.Errorf("applied %d events, want 5", len(fulfilled))
	}
}
