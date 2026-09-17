package orders

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/customers"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

func newIdempotentService(t *testing.T, pool *pgxpool.Pool, arm string) Service {
	t.Helper()

	inner, err := StrategyByName(arm, repo.New(pool), pool, customers.DirectResolver{})
	if err != nil {
		t.Fatalf("build arm %q: %v", arm, err)
	}
	return NewService(repo.New(pool), pool, &fakeGateway{},
		WithIdempotency(inner, repo.New(pool)), 15*time.Minute)
}

// This is the bug the whole feature exists for: before idempotency keys, a
// retried reserve took a second unit of stock, and the frontend had to guard
// against its own retries with a ref because the server could not be trusted
// with them.
func TestRetryWithSameKeyReservesOnce(t *testing.T) {
	t.Parallel()

	for _, arm := range StrategyNames() {
		t.Run(arm, func(t *testing.T) {
			t.Parallel()

			pool := testsupport.DB(t)
			productID := testsupport.SeedProduct(t, pool, "retry", 500, 10)
			service := newIdempotentService(t, pool, arm)

			p := CreateOrderParams{
				ProductID:      productID,
				Claims:         buyer(0),
				IdempotencyKey: "key-abc",
			}

			first, err := service.CreateOrder(context.Background(), p)
			if err != nil {
				t.Fatalf("first reserve: %v", err)
			}

			second, err := service.CreateOrder(context.Background(), p)
			if err != nil {
				t.Fatalf("retry should replay the first order, not fail: %v", err)
			}

			if first.ID != second.ID {
				t.Errorf("retry produced order %d, want the original %d", second.ID, first.ID)
			}
			// The point: one unit, not two.
			testsupport.AssertStock(t, pool, productID, 10, 1)
		})
	}
}

// Two simultaneous retries is the realistic shape — a double-clicked button, or
// a client retrying while the original is still in flight. Exactly one may
// reserve; the others must either replay it or be told to retry, never take a
// second unit.
func TestConcurrentRetriesReserveOnce(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "double-submit", 500, 10)
	service := newIdempotentService(t, pool, StrategyCTE)

	p := CreateOrderParams{
		ProductID:      productID,
		Claims:         buyer(1),
		IdempotencyKey: "key-race",
	}

	const attempts = 20
	var (
		ok         atomic.Int64
		inProgress atomic.Int64
		wg         sync.WaitGroup
		start      = make(chan struct{})
	)

	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			_, err := service.CreateOrder(context.Background(), p)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrReserveInProgress):
				inProgress.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}

	close(start)
	wg.Wait()

	if ok.Load()+inProgress.Load() != attempts {
		t.Fatalf("accounted for %d of %d attempts", ok.Load()+inProgress.Load(), attempts)
	}
	// Whatever the split between "here is your order" and "still in progress",
	// exactly one unit may leave the shelf.
	testsupport.AssertStock(t, pool, productID, 10, 1)

	var orders int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orders WHERE product_id = $1`, productID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 1 {
		t.Errorf("%d orders created, want exactly 1", orders)
	}
}

// Reusing a key for a different product is a client bug. Returning the first
// order would quietly sell them something they did not ask for.
func TestKeyReuseWithDifferentProductIsRejected(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	first := testsupport.SeedProduct(t, pool, "product-a", 500, 5)
	second := testsupport.SeedProduct(t, pool, "product-b", 700, 5)
	service := newIdempotentService(t, pool, StrategyCTE)

	claims := buyer(2)
	if _, err := service.CreateOrder(context.Background(), CreateOrderParams{
		ProductID: first, Claims: claims, IdempotencyKey: "key-shared",
	}); err != nil {
		t.Fatalf("first reserve: %v", err)
	}

	_, err := service.CreateOrder(context.Background(), CreateOrderParams{
		ProductID: second, Claims: claims, IdempotencyKey: "key-shared",
	})
	if !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("got %v, want ErrIdempotencyKeyReused", err)
	}
	testsupport.AssertStock(t, pool, second, 5, 0)
}

// A key is scoped to the caller. Without that, one buyer's key would collide
// with another's and hand them someone else's order.
func TestKeysAreScopedToTheCaller(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "shared-key", 500, 5)
	service := newIdempotentService(t, pool, StrategyCTE)

	one, err := service.CreateOrder(context.Background(), CreateOrderParams{
		ProductID: productID, Claims: buyer(3), IdempotencyKey: "same-key",
	})
	if err != nil {
		t.Fatalf("first buyer: %v", err)
	}

	two, err := service.CreateOrder(context.Background(), CreateOrderParams{
		ProductID: productID, Claims: buyer(4), IdempotencyKey: "same-key",
	})
	if err != nil {
		t.Fatalf("second buyer reusing the same key string: %v", err)
	}

	if one.ID == two.ID {
		t.Fatal("two different buyers using the same key string got the same order")
	}
	testsupport.AssertStock(t, pool, productID, 5, 2)
}

// A failed reserve must not burn the key: the caller's natural response is to
// retry, and a key stuck forever would make a transient failure permanent.
func TestFailedReserveReleasesTheKey(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "one-unit", 500, 1)
	service := newIdempotentService(t, pool, StrategyCTE)

	// Take the only unit as someone else.
	if _, err := service.CreateOrder(context.Background(), CreateOrderParams{
		ProductID: productID, Claims: buyer(5),
	}); err != nil {
		t.Fatalf("seed reserve: %v", err)
	}

	p := CreateOrderParams{ProductID: productID, Claims: buyer(6), IdempotencyKey: "key-retry"}

	if _, err := service.CreateOrder(context.Background(), p); !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("got %v, want ErrOutOfStock", err)
	}

	// Same key again: it must still report out of stock rather than replaying a
	// success that never happened, or reporting the key as in progress forever.
	if _, err := service.CreateOrder(context.Background(), p); !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("retry after a failed reserve gave %v, want ErrOutOfStock", err)
	}
}

// Without a key the endpoint behaves exactly as it always has. Every existing
// client sends no key, so this is the path almost all traffic takes.
func TestNoKeyKeepsOriginalBehaviour(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "no-key", 500, 5)
	service := newIdempotentService(t, pool, StrategyCTE)

	p := CreateOrderParams{ProductID: productID, Claims: buyer(7)}
	for range 2 {
		if _, err := service.CreateOrder(context.Background(), p); err != nil {
			t.Fatalf("reserve: %v", err)
		}
	}

	// Two requests, two units — unchanged, and the reason the frontend's ref
	// guard cannot be removed until it sends a key.
	testsupport.AssertStock(t, pool, productID, 5, 2)
}
