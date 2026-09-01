package orders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// These tests run against a real PostgreSQL, and that is not incidental: the
// oversell guarantee lives in SQL — ReserveStock's `quantity - num_reserved
// > 0` and the status guards on the orders table — and depends on row-level
// locking under concurrent writers. A mocked repo.Querier would exercise none
// of it and would pass no matter how broken the queries were.
//
// Each test gets its own database (testsupport.DB), so they run in parallel
// and operations with global scope — the expiry sweep especially — can be
// asserted to an exact count rather than "at least one".

// fakeGateway stands in for Stripe. Payment-processor behaviour is covered at
// the HTTP level in cmd; what matters here is inventory integrity around it.
type fakeGateway struct {
	seq  atomic.Int64
	fail error
}

func (g *fakeGateway) CreatePaymentIntent(_ context.Context, p PaymentIntentParams) (PaymentIntent, error) {
	if g.fail != nil {
		return PaymentIntent{}, g.fail
	}
	// Unique per call: stripe_payment_intent_id is UNIQUE.
	id := fmt.Sprintf("pi_test_%d_%d", p.OrderID, g.seq.Add(1))
	return PaymentIntent{ID: id, ClientSecret: id + "_secret"}, nil
}

func newTestService(pool *pgxpool.Pool, ttl time.Duration) (Service, *fakeGateway) {
	gateway := &fakeGateway{}
	return NewService(repo.New(pool), pool, gateway, ttl), gateway
}

// buyer builds a distinct caller identity. Claims are constructed directly
// because the service layer never sees a token — token verification is covered
// in internal/auth and cmd.
func buyer(n int) auth.Claims {
	return auth.Claims{
		Subject: fmt.Sprintf("sub-buyer-%d", n),
		Email:   fmt.Sprintf("buyer-%d@example.test", n),
	}
}

// TestCreateOrderNeverOversells is the project's central claim: no matter how
// many buyers reserve the same product at the same instant, stock cannot be
// reserved past what exists.
func TestCreateOrderNeverOversells(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)

	const (
		buyers = 50
		stock  = 7
	)

	productID := testsupport.SeedProduct(t, pool, "doorbuster", 1999, stock)
	service, _ := newTestService(pool, 15*time.Minute)

	var (
		reserved   atomic.Int64
		outOfStock atomic.Int64
		wg         sync.WaitGroup
		start      = make(chan struct{})
	)

	for i := range buyers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Release everyone at once, so the reservations actually collide
			// rather than arriving in a queue.
			<-start

			_, err := service.CreateOrder(context.Background(), productID, buyer(i))
			switch {
			case err == nil:
				reserved.Add(1)
			case errors.Is(err, ErrOutOfStock):
				outOfStock.Add(1)
			default:
				t.Errorf("unexpected error reserving: %v", err)
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := reserved.Load(); got != stock {
		t.Errorf("reserved %d units, want exactly %d", got, stock)
	}
	if got := outOfStock.Load(); got != buyers-stock {
		t.Errorf("%d buyers got out-of-stock, want %d", got, buyers-stock)
	}

	// Reserving holds stock without consuming it.
	testsupport.AssertStock(t, pool, productID, stock, stock)
}

// TestConcurrentCancelReleasesOnce covers a way num_reserved can be
// decremented. Without the status guard on CancelPendingOrder, racing cancels
// would each release a unit and drive the counter negative.
func TestConcurrentCancelReleasesOnce(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "cancel-race", 100, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	claims := buyer(0)
	order, err := service.CreateOrder(context.Background(), productID, claims)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	const cancels = 10
	var (
		succeeded atomic.Int64
		wg        sync.WaitGroup
		start     = make(chan struct{})
	)

	for range cancels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			if _, err := service.CancelOrder(context.Background(), order.ID, claims); err == nil {
				succeeded.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := succeeded.Load(); got != 1 {
		t.Errorf("%d cancels succeeded, want exactly 1", got)
	}
	testsupport.AssertStock(t, pool, productID, 1, 0)
}

// TestRedeliveredPaymentCommitsStockOnce is the webhook idempotency claim.
// Stripe delivers at-least-once, so FulfillPayment is called repeatedly for
// the same intent on purpose.
func TestRedeliveredPaymentCommitsStockOnce(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "redelivery", 100, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	claims := buyer(0)
	ctx := context.Background()

	order, err := service.CreateOrder(ctx, productID, claims)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	result, err := service.Checkout(ctx, order.ID, claims)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if result.ClientSecret == "" {
		t.Error("checkout returned no client secret")
	}

	// Checkout must not consume stock — that is the point of the two halves.
	testsupport.AssertStock(t, pool, productID, 1, 1)

	const deliveries = 8
	var (
		failures atomic.Int64
		wg       sync.WaitGroup
		start    = make(chan struct{})
	)

	for range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := service.FulfillPayment(context.Background(), result.PaymentIntentID); err != nil {
				failures.Add(1)
				t.Errorf("redelivery reported an error: %v", err)
			}
		}()
	}

	close(start)
	wg.Wait()

	// Every delivery must report success. Only one of them actually applies —
	// the rest find the order already completed, which classifyUnfulfillable
	// treats as a benign redelivery rather than an incident. Erroring on
	// those would raise a false refund alarm on ordinary Stripe retries.
	if got := failures.Load(); got != 0 {
		t.Errorf("%d of %d redeliveries errored, want 0", got, deliveries)
	}

	// The real idempotency claim: stock moved exactly once regardless.
	testsupport.AssertStock(t, pool, productID, 0, 0)
	testsupport.AssertOrderStatus(t, pool, order.ID, StatusCompleted)
}

func TestFailPaymentReleasesStock(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "declined", 100, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	claims := buyer(0)
	ctx := context.Background()

	order, err := service.CreateOrder(ctx, productID, claims)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	result, err := service.Checkout(ctx, order.ID, claims)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if err := service.FailPayment(ctx, result.PaymentIntentID); err != nil {
		t.Fatalf("fail payment: %v", err)
	}

	testsupport.AssertOrderStatus(t, pool, order.ID, StatusFailed)
	// A failed payment is not a sale: the unit goes back on sale untouched.
	testsupport.AssertStock(t, pool, productID, 1, 0)

	t.Run("redelivered failure releases at most once", func(t *testing.T) {
		if err := service.FailPayment(ctx, result.PaymentIntentID); err != nil {
			t.Errorf("second failure returned %v, want nil (already handled)", err)
		}
		testsupport.AssertStock(t, pool, productID, 1, 0)
	})
}

// TestSweeperReleasesExpiredReservation checks the expiry path, including the
// case the design has to be honest about: a payment that succeeds after its
// reservation was already swept.
func TestSweeperReleasesExpiredReservation(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "expiring", 100, 1)

	// A negative TTL puts expires_at in the past, so the reservation is
	// already expired the moment it is created — deterministic, no sleeping.
	service, _ := newTestService(pool, -time.Second)

	claims := buyer(0)
	ctx := context.Background()

	order, err := service.CreateOrder(ctx, productID, claims)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	result, err := service.Checkout(ctx, order.ID, claims)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	released, err := service.SweepExpired(ctx, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// An isolated database means this is exact rather than "at least one".
	if released != 1 {
		t.Fatalf("sweep released %d reservations, want exactly 1", released)
	}

	testsupport.AssertOrderStatus(t, pool, order.ID, StatusExpired)
	testsupport.AssertStock(t, pool, productID, 1, 0)

	t.Run("a second sweep finds nothing", func(t *testing.T) {
		again, err := service.SweepExpired(ctx, 100)
		if err != nil {
			t.Fatalf("second sweep: %v", err)
		}
		if again != 0 {
			t.Errorf("second sweep released %d, want 0", again)
		}
	})

	t.Run("a late payment must not commit stock", func(t *testing.T) {
		// Money moved, but the unit is back on sale and may already belong to
		// someone else. This must not quietly complete, and must not report
		// success — or the mismatch is invisible.
		err := service.FulfillPayment(ctx, result.PaymentIntentID)
		if err == nil {
			t.Error("a payment for an expired order reported success; it needs to surface for refund")
		}
		if !errors.Is(err, ErrPaidButNotFulfillable) {
			t.Errorf("got %v, want it to wrap ErrPaidButNotFulfillable", err)
		}
		testsupport.AssertStock(t, pool, productID, 1, 0)
	})
}

func TestSweeperLeavesLiveReservationsAlone(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "not-yet-expired", 100, 1)
	service, _ := newTestService(pool, time.Hour)

	ctx := context.Background()
	order, err := service.CreateOrder(ctx, productID, buyer(0))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	released, err := service.SweepExpired(ctx, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if released != 0 {
		t.Errorf("swept %d live reservations, want 0", released)
	}

	testsupport.AssertOrderStatus(t, pool, order.ID, StatusPending)
	testsupport.AssertStock(t, pool, productID, 1, 1)
}

func TestCheckoutSurfacesGatewayFailure(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "gateway-down", 100, 1)
	service, gateway := newTestService(pool, 15*time.Minute)

	ctx := context.Background()
	claims := buyer(0)

	order, err := service.CreateOrder(ctx, productID, claims)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	gateway.fail = errors.New("stripe unavailable")

	if _, err := service.Checkout(ctx, order.ID, claims); err == nil {
		t.Fatal("expected checkout to fail when the gateway does")
	}

	// Nothing was charged, so nothing should have advanced: the order stays
	// pending and keeps its reservation, leaving the buyer able to retry.
	testsupport.AssertOrderStatus(t, pool, order.ID, StatusPending)
	testsupport.AssertStock(t, pool, productID, 1, 1)

	if got := testsupport.Order(t, pool, order.ID); got.StripePaymentIntentID.Valid {
		t.Errorf("a payment intent was recorded despite the gateway failing: %q", got.StripePaymentIntentID.String)
	}
}

func TestListOrders(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "listed", 250, 10)
	service, _ := newTestService(pool, 15*time.Minute)

	ctx := context.Background()
	mine, theirs := buyer(1), buyer(2)

	for range 3 {
		if _, err := service.CreateOrder(ctx, productID, mine); err != nil {
			t.Fatalf("reserve: %v", err)
		}
	}
	if _, err := service.CreateOrder(ctx, productID, theirs); err != nil {
		t.Fatalf("reserve for other buyer: %v", err)
	}

	t.Run("returns only the caller's orders", func(t *testing.T) {
		got, err := service.ListOrders(ctx, mine, 20, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d orders, want 3", len(got))
		}
		for _, o := range got {
			if o.ProductName != "listed" {
				t.Errorf("product_name = %q, want the joined product name", o.ProductName)
			}
		}
	})

	t.Run("newest first", func(t *testing.T) {
		got, err := service.ListOrders(ctx, mine, 20, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].ID < got[i].ID {
				t.Errorf("orders are not newest-first: %d before %d", got[i-1].ID, got[i].ID)
			}
		}
	})

	t.Run("honours limit and offset", func(t *testing.T) {
		page, err := service.ListOrders(ctx, mine, 2, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(page) != 2 {
			t.Errorf("got %d orders, want 2", len(page))
		}

		rest, err := service.ListOrders(ctx, mine, 2, 2)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(rest) != 1 {
			t.Errorf("got %d orders on page 2, want 1", len(rest))
		}
	})

	t.Run("a customer with no orders gets an empty list", func(t *testing.T) {
		// Must not error: a signed-up user who has never ordered is normal.
		got, err := service.ListOrders(ctx, buyer(99), 20, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d orders, want 0", len(got))
		}
	})
}

func TestOwnershipIsEnforced(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "owned", 100, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	owner, stranger := buyer(1), buyer(2)
	ctx := context.Background()

	order, err := service.CreateOrder(ctx, productID, owner)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if _, err := service.GetOrder(ctx, order.ID, stranger); !errors.Is(err, ErrForbidden) {
		t.Errorf("GetOrder by a stranger returned %v, want ErrForbidden", err)
	}
	if _, err := service.Checkout(ctx, order.ID, stranger); !errors.Is(err, ErrForbidden) {
		t.Errorf("Checkout by a stranger returned %v, want ErrForbidden", err)
	}
	if _, err := service.CancelOrder(ctx, order.ID, stranger); !errors.Is(err, ErrForbidden) {
		t.Errorf("CancelOrder by a stranger returned %v, want ErrForbidden", err)
	}

	if _, err := service.GetOrder(ctx, order.ID, owner); err != nil {
		t.Errorf("GetOrder by the owner failed: %v", err)
	}
}

// Ownership prefers the immutable Cognito subject over the mutable email. A
// customer row created before cognito_sub existed falls back to email, and
// that fallback must keep working until every row is backfilled.
func TestOwnershipFallsBackToEmailForLegacyRows(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "legacy", 100, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	ctx := context.Background()

	// A row with no subject, as migration 00005 leaves pre-existing rows.
	legacy := testsupport.SeedCustomer(t, pool, "legacy@example.test", "")
	if legacy.CognitoSub.Valid {
		t.Fatal("expected a customer with no cognito_sub")
	}

	order, err := service.CreateOrder(ctx, productID, auth.Claims{Email: "legacy@example.test"})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if _, err := service.GetOrder(ctx, order.ID, auth.Claims{Email: "legacy@example.test"}); err != nil {
		t.Errorf("owner matched by email was rejected: %v", err)
	}
	if _, err := service.GetOrder(ctx, order.ID, auth.Claims{Email: "someone-else@example.test"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a different email was accepted; got %v", err)
	}
}
