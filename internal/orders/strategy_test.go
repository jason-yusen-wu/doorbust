package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// TestReserveContractIsIdenticalAcrossStrategies pins the one thing the
// single-statement arm can plausibly break.
//
// The CTE returns zero rows for "no such product" and "no unit free" alike, so
// a naive implementation collapses 404 into 409 and silently changes the API.
// cmd/contract_test.go asserts both statuses through the router; this asserts
// the same distinction at the layer that actually owns it, for every arm, so a
// new arm cannot regress it without failing here first.
func TestReserveContractIsIdenticalAcrossStrategies(t *testing.T) {
	t.Parallel()

	for _, arm := range StrategyNames() {
		t.Run(arm, func(t *testing.T) {
			t.Parallel()

			pool := testsupport.DB(t)
			service, _ := newTestService(pool, 15*time.Minute, arm)
			ctx := context.Background()

			t.Run("missing product is ErrNoRows", func(t *testing.T) {
				_, err := service.CreateOrder(ctx, CreateOrderParams{ProductID: 999999, Claims: buyer(1)})
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("reserving a nonexistent product: got %v, want pgx.ErrNoRows (the handler's 404)", err)
				}
			})

			t.Run("depleted product is ErrOutOfStock", func(t *testing.T) {
				productID := testsupport.SeedProduct(t, pool, "one-unit", 100, 1)

				if _, err := service.CreateOrder(ctx, CreateOrderParams{ProductID: productID, Claims: buyer(2)}); err != nil {
					t.Fatalf("first reserve should succeed: %v", err)
				}

				_, err := service.CreateOrder(ctx, CreateOrderParams{ProductID: productID, Claims: buyer(3)})
				if !errors.Is(err, ErrOutOfStock) {
					t.Fatalf("reserving a depleted product: got %v, want ErrOutOfStock (the handler's 409)", err)
				}
			})
		})
	}
}

// TestStrategyByNameRejectsUnknown keeps a typo in RESERVE_STRATEGY from
// silently running the default. cmd turns this error into a boot panic.
func TestStrategyByNameRejectsUnknown(t *testing.T) {
	t.Parallel()

	if _, err := StrategyByName("not-a-strategy", nil, nil, nil); err == nil {
		t.Fatal("expected an unknown strategy name to be rejected")
	}
}

// TestStrategyNamesAllBuild is the other half of that discipline: every name
// the list advertises must actually construct, so the concurrency tests cannot
// be quietly skipping an arm.
func TestStrategyNamesAllBuild(t *testing.T) {
	t.Parallel()

	for _, arm := range StrategyNames() {
		s, err := StrategyByName(arm, nil, nil, nil)
		if err != nil {
			t.Fatalf("StrategyByName(%q): %v", arm, err)
		}
		if s.Name() != arm {
			t.Errorf("arm %q reports Name() = %q", arm, s.Name())
		}
	}
}
