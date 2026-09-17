package orders

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/customers"
)

// txnStrategy is the baseline and reserve-last arms, which differ by exactly
// which of two statements runs first. They share a type on purpose: written out
// twice they would be copy-paste with a divergence waiting in it, and the whole
// point of the comparison is that nothing else differs.
type txnStrategy struct {
	repo     repo.Querier
	db       repo.Beginner
	resolver customers.Resolver

	// reserveLast moves ReserveStock after CreateOrder (R0). The transaction is
	// atomic either way, so correctness is unchanged; what changes is that the
	// stock row's lock is then held across one client round trip (COMMIT)
	// instead of two (CreateOrder, COMMIT).
	reserveLast bool
}

func (s *txnStrategy) Name() string {
	if s.reserveLast {
		return StrategyReserveLast
	}
	return StrategyBaseline
}

// Reserve runs the six-round-trip transaction this project started with.
//
// Statement order matters, and is the experiment. ReserveStock takes a row lock
// on the contended stock row, and everything after it in the transaction
// extends how long that lock is held — so the customer resolution and the price
// read deliberately run first in both arms, and only the order insert moves.
//
// Two consequences of the reorder, neither a correctness problem, both worth
// knowing before reading a benchmark result:
//
//   - orders_id_seq is non-transactional, so an attempt that rolls back still
//     burns an id. Under a doorbuster, orders.id tracks demand rather than
//     successes, which makes max(id) a wrong proxy for "orders created". Count
//     rows instead.
//   - The foreign key orders.product_id -> products(id) takes a FOR KEY SHARE
//     lock on the products row, which the reorder moves inside the stock lock
//     window. Different table, and nothing in this workload writes products, so
//     it adds no contention.
func (s *txnStrategy) Reserve(ctx context.Context, p ReserveParams) (repo.Order, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repo.Order{}, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	customerID, err := s.resolver.Resolve(ctx, q, p.Claims)
	if err != nil {
		return repo.Order{}, err
	}

	product, err := q.FindProductByID(ctx, p.ProductID)
	if err != nil {
		return repo.Order{}, err
	}

	reserve := func() error {
		if _, err := q.ReserveStock(ctx, p.ProductID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrOutOfStock
			}
			return err
		}
		return nil
	}

	create := func() (repo.Order, error) {
		return q.CreateOrder(ctx, repo.CreateOrderParams{
			CustomerID:   customerID,
			ProductID:    p.ProductID,
			TotalInCents: product.PriceInCents,
			ExpiresAt:    p.ExpiresAt,
		})
	}

	var order repo.Order

	if s.reserveLast {
		// R0. The insert needs nothing from the reservation — its parameters
		// come from the customer, the product and the caller's clock — so the
		// two are freely reorderable, and putting the lock last shortens the
		// window to the commit alone.
		if order, err = create(); err != nil {
			return repo.Order{}, err
		}
		if err := reserve(); err != nil {
			return repo.Order{}, err
		}
	} else {
		if err := reserve(); err != nil {
			return repo.Order{}, err
		}
		if order, err = create(); err != nil {
			return repo.Order{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return repo.Order{}, err
	}

	return order, nil
}
