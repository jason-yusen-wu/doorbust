package customers

import (
	"context"
	"sync"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

// Resolver maps a verified Cognito caller to a customers row id.
//
// It exists because resolving the caller is on the reserve hot path, where it
// costs a round trip inside — or, with the single-statement reserve, beside —
// the stock row's lock window. Track B measures what taking it off that path
// buys, which needs two implementations behind one interface.
//
// Resolve takes a Querier rather than closing over one so the caller decides
// scope: the transactional reserve arms hand it the transaction's querier, so
// resolution happens inside the transaction exactly as it does today; the
// single-statement arm hands it the pool, so resolution happens outside the
// critical section entirely.
type Resolver interface {
	Resolve(ctx context.Context, q repo.Querier, claims auth.Claims) (int64, error)
}

// DirectResolver calls Link on every request. This is production's default and
// today's behaviour: correct, and one round trip in the steady state.
type DirectResolver struct{}

func (DirectResolver) Resolve(ctx context.Context, q repo.Querier, claims auth.Claims) (int64, error) {
	customer, err := Link(ctx, q, claims)
	if err != nil {
		return 0, err
	}
	return customer.ID, nil
}

// CachedResolver memoises cognito sub -> customers.id in process.
//
// Sound because a customers row's id is immutable once created. Link may adopt
// a changed *email* onto an existing row, but the reserve path reads only the
// id, so a stale entry cannot produce a wrong answer. That is also why this is
// deliberately NOT used by GET /me or ListOrders: those are where an email
// change still has to be noticed and written.
//
// A caller with no subject is never cached — there is no stable key for one —
// and falls through to the direct path.
//
// Unbounded on purpose, for the experiment: it holds one small entry per
// distinct buyer and the benchmark records the entry count alongside the
// result. Shipping it as a default needs a bound (an LRU, or a TTL); see B3.
type CachedResolver struct {
	ids sync.Map // string (cognito sub) -> int64 (customers.id)
}

func (c *CachedResolver) Resolve(ctx context.Context, q repo.Querier, claims auth.Claims) (int64, error) {
	if claims.Subject == "" {
		return DirectResolver{}.Resolve(ctx, q, claims)
	}

	if id, ok := c.ids.Load(claims.Subject); ok {
		return id.(int64), nil
	}

	id, err := DirectResolver{}.Resolve(ctx, q, claims)
	if err != nil {
		return 0, err
	}

	c.ids.Store(claims.Subject, id)
	return id, nil
}

// Len reports how many identities are memoised. The benchmark records it so a
// run's cache hit rate can be reasoned about after the fact.
func (c *CachedResolver) Len() int {
	n := 0
	c.ids.Range(func(_, _ any) bool { n++; return true })
	return n
}
