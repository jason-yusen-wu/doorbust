package orders

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/customers"
)

// cteStrategy is R2: the whole reserve collapsed into one statement.
//
// There is no explicit transaction, because a single statement already is one.
// That removes every client round trip from inside the stock row's lock window:
// the lock is taken and released within one server execution, with no network
// in between. Six round trips become two — or one, with a warm customer cache.
//
// Note there is deliberately no Beginner here. Nothing left to group.
type cteStrategy struct {
	repo     repo.Querier
	resolver customers.Resolver
}

func (s *cteStrategy) Name() string { return StrategyCTE }

func (s *cteStrategy) Reserve(ctx context.Context, p ReserveParams) (repo.Order, error) {
	// Outside any transaction, on purpose: resolving the caller must not sit
	// inside the reserve's critical section, and with a single-statement
	// reserve there is nothing for a transaction to group anyway.
	customerID, err := s.resolver.Resolve(ctx, s.repo, p.Claims)
	if err != nil {
		return repo.Order{}, err
	}

	order, err := s.repo.ReserveAndCreateOrder(ctx, repo.ReserveAndCreateOrderParams{
		CustomerID: customerID,
		ProductID:  p.ProductID,
		ExpiresAt:  p.ExpiresAt,
	})
	if err == nil {
		return order, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return repo.Order{}, err
	}

	// Zero rows means "no such product" or "no unit free", and the API contract
	// distinguishes them (404 vs 409). One extra read decides which, and it is
	// paid ONLY on the failure path, outside any transaction, against a row
	// nothing has locked — so it cannot widen the critical section or move the
	// per-SKU ceiling.
	//
	// It does add load in a doorbuster shape, where most requests fail. That is
	// visible rather than hidden: the benchmark reports out_of_stock latency as
	// its own distribution.
	if _, ferr := s.repo.FindProductByID(ctx, p.ProductID); ferr != nil {
		return repo.Order{}, ferr // pgx.ErrNoRows -> 404 not_found
	}
	return repo.Order{}, ErrOutOfStock
}
