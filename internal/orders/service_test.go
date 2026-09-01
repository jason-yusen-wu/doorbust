package orders

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

// These tests run against a real PostgreSQL. That is not incidental: the
// oversell guarantee lives in SQL — ReserveStock's `quantity - num_reserved
// > 0` and the status guards on the orders table — and depends on row-level
// locking under concurrent writers. A mocked repo.Querier would exercise none
// of it and would pass no matter how broken the SQL was.
//
// CI provides TEST_DATABASE_URL alongside a postgres:17-alpine service
// container; locally, point it at a scratch database (never a real one — the
// tests write freely).

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed tests")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// seedProduct creates a product and its stock row, returning the product id.
// Every test seeds its own so tests never contend with each other.
func seedProduct(t *testing.T, pool *pgxpool.Pool, quantity int32) int64 {
	t.Helper()

	ctx := context.Background()
	q := repo.New(pool)

	product, err := q.CreateProduct(ctx, repo.CreateProductParams{
		Name:         fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano()),
		PriceInCents: 1999,
		// start_at is NOT NULL and the insert names it explicitly, so the
		// column default never applies here.
		StartAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}

	if _, err := q.CreateStock(ctx, repo.CreateStockParams{
		ProductID: product.ID,
		Quantity:  quantity,
	}); err != nil {
		t.Fatalf("seed stock: %v", err)
	}

	return product.ID
}

func readStock(t *testing.T, pool *pgxpool.Pool, productID int64) repo.FindProductByIDRow {
	t.Helper()

	row, err := repo.New(pool).FindProductByID(context.Background(), productID)
	if err != nil {
		t.Fatalf("read stock: %v", err)
	}
	return row
}

// claimsFor builds a distinct caller identity per test buyer.
func claimsFor(t *testing.T, n int) auth.Claims {
	t.Helper()

	unique := fmt.Sprintf("%s-%d-%d", t.Name(), n, time.Now().UnixNano())
	return auth.Claims{
		Subject: "sub-" + unique,
		Email:   unique + "@example.test",
	}
}

// fakeGateway stands in for Stripe. Payment-processor behaviour is not what
// these tests are about; inventory integrity around it is.
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

// TestCreateOrderNeverOversells is the project's central claim: no matter how
// many buyers reserve the same product at the same instant, stock cannot be
// reserved past what exists.
func TestCreateOrderNeverOversells(t *testing.T) {
	pool := testPool(t)

	const (
		buyers = 50
		stock  = 7
	)

	productID := seedProduct(t, pool, stock)
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

			_, err := service.CreateOrder(context.Background(), productID, claimsFor(t, i))
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

	final := readStock(t, pool, productID)
	if final.NumReserved != stock {
		t.Errorf("num_reserved = %d, want %d", final.NumReserved, stock)
	}
	if final.Quantity != stock {
		t.Errorf("quantity = %d, want %d (reserving must not consume stock)", final.Quantity, stock)
	}
	// The CHECK constraint is the backstop; it should never have been what
	// stopped an oversell.
	if final.NumReserved > final.Quantity {
		t.Fatalf("oversold: num_reserved %d > quantity %d", final.NumReserved, final.Quantity)
	}
}

// TestConcurrentCancelReleasesOnce covers the new way num_reserved can be
// decremented. Without the status guard on CancelPendingOrder, racing cancels
// would each release a unit and drive the counter negative.
func TestConcurrentCancelReleasesOnce(t *testing.T) {
	pool := testPool(t)

	productID := seedProduct(t, pool, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	claims := claimsFor(t, 0)
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

	final := readStock(t, pool, productID)
	if final.NumReserved != 0 {
		t.Errorf("num_reserved = %d after cancel, want 0", final.NumReserved)
	}
	if final.Quantity != 1 {
		t.Errorf("quantity = %d, want 1 (a cancel must not consume stock)", final.Quantity)
	}
}

// TestRedeliveredPaymentCommitsStockOnce is the webhook idempotency claim.
// Stripe delivers at-least-once, so FulfillPayment is called repeatedly for
// the same intent on purpose.
func TestRedeliveredPaymentCommitsStockOnce(t *testing.T) {
	pool := testPool(t)

	productID := seedProduct(t, pool, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	claims := claimsFor(t, 0)
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

	// Checkout must not have consumed stock — that is the whole point of the
	// two halves.
	afterCheckout := readStock(t, pool, productID)
	if afterCheckout.Quantity != 1 || afterCheckout.NumReserved != 1 {
		t.Fatalf("after checkout quantity=%d num_reserved=%d, want 1/1 (payment not yet confirmed)",
			afterCheckout.Quantity, afterCheckout.NumReserved)
	}

	const deliveries = 8
	var wg sync.WaitGroup
	start := make(chan struct{})

	for range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Errors are expected on all but one: the guard rejects the rest.
			_ = service.FulfillPayment(context.Background(), result.PaymentIntentID)
		}()
	}

	close(start)
	wg.Wait()

	final := readStock(t, pool, productID)
	if final.Quantity != 0 {
		t.Errorf("quantity = %d after payment, want 0", final.Quantity)
	}
	if final.NumReserved != 0 {
		t.Errorf("num_reserved = %d after payment, want 0", final.NumReserved)
	}
}

// TestSweeperReleasesExpiredReservation checks the expiry path end to end,
// including the case the whole design has to be honest about: a payment that
// succeeds after its reservation was already swept.
func TestSweeperReleasesExpiredReservation(t *testing.T) {
	pool := testPool(t)

	productID := seedProduct(t, pool, 1)
	// A negative TTL puts expires_at in the past, so the reservation is
	// already expired the moment it is created.
	service, _ := newTestService(pool, -time.Second)

	claims := claimsFor(t, 0)
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
	if released < 1 {
		t.Fatalf("sweep released %d reservations, want at least 1", released)
	}

	afterSweep := readStock(t, pool, productID)
	if afterSweep.NumReserved != 0 {
		t.Errorf("num_reserved = %d after sweep, want 0", afterSweep.NumReserved)
	}
	if afterSweep.Quantity != 1 {
		t.Errorf("quantity = %d after sweep, want 1 (expiry must put the unit back on sale)", afterSweep.Quantity)
	}

	// The payment lands late. Money moved, but the unit is back on sale and
	// may already belong to someone else, so this must NOT quietly commit
	// stock — and it must not report success, or the mismatch is invisible.
	err = service.FulfillPayment(ctx, result.PaymentIntentID)
	if err == nil {
		t.Error("a payment for an expired order reported success; it needs to surface for refund")
	}

	afterLatePayment := readStock(t, pool, productID)
	if afterLatePayment.Quantity != 1 || afterLatePayment.NumReserved != 0 {
		t.Errorf("late payment changed stock to quantity=%d num_reserved=%d, want 1/0",
			afterLatePayment.Quantity, afterLatePayment.NumReserved)
	}
}

// TestOwnershipIsEnforced guards the authorization half of the contract.
func TestOwnershipIsEnforced(t *testing.T) {
	pool := testPool(t)

	productID := seedProduct(t, pool, 1)
	service, _ := newTestService(pool, 15*time.Minute)

	owner := claimsFor(t, 0)
	stranger := claimsFor(t, 1)
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
