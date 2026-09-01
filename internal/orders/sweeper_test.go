package orders

import (
	"context"
	"testing"
	"time"

	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// The sweeper is the loop around SweepExpired. Its own job is small but not
// trivial: it must drain a backlog rather than releasing one batch per tick,
// and it must stop when the process is shutting down.

func TestSweeperDrainsBacklogLargerThanOneBatch(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)

	const (
		reservations = 25
		batchSize    = 10 // deliberately smaller, so draining takes 3 passes
	)

	productID := testsupport.SeedProduct(t, pool, "backlog", 100, reservations)
	// Negative TTL: every reservation is expired the moment it is made.
	service, _ := newTestService(pool, -time.Second)

	ctx := context.Background()
	for i := range reservations {
		if _, err := service.CreateOrder(ctx, productID, buyer(i)); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
	testsupport.AssertStock(t, pool, productID, reservations, reservations)

	sweeper := NewSweeper(service, time.Hour, batchSize)

	// sweepAll drains in batches; after an outage there may be far more
	// expired reservations than one batch holds, and stock should not trickle
	// back one batch per interval.
	sweeper.sweepAll(ctx)

	testsupport.AssertStock(t, pool, productID, reservations, 0)
}

func TestSweeperRunStopsOnCancel(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	productID := testsupport.SeedProduct(t, pool, "sweeper-run", 100, 1)
	service, _ := newTestService(pool, -time.Second)

	ctx := context.Background()
	if _, err := service.CreateOrder(ctx, productID, buyer(0)); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Run sweeps once immediately on the first tick; a short interval keeps
	// the test quick without sleeping for a fixed period.
	sweeper := NewSweeper(service, 5*time.Millisecond, 100)

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sweeper.Run(runCtx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if testsupport.Stock(t, pool, productID).NumReserved == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	testsupport.AssertStock(t, pool, productID, 1, 0)
}

// A sweep that finds nothing must not error or spin.
func TestSweeperHandlesEmptyBacklog(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service, _ := newTestService(pool, time.Hour)

	sweeper := NewSweeper(service, time.Hour, 10)
	sweeper.sweepAll(context.Background()) // must simply return
}
