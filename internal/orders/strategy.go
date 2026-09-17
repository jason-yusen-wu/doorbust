package orders

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/customers"
)

// ReserveStrategy is how one unit of stock becomes a pending order.
//
// It exists because the reserve path is the subject of an experiment, not
// because the app needs polymorphism. ReserveStock's comment in queries.sql has
// always promised the contention strategy stays swappable; this is where that
// promise is collected.
//
// Every implementation is oversell-free. They differ only in how many client
// round trips the stock row's lock is held across, which is the quantity
// Track B measures:
//
//	baseline      ReserveStock -> CreateOrder -> COMMIT    2 round trips locked
//	reserve-last  CreateOrder -> ReserveStock -> COMMIT    1 round trip locked
//	cte           one implicit transaction                 0 round trips locked
type ReserveStrategy interface {
	// Name identifies the arm in benchmark output and run metadata.
	Name() string

	// Reserve reserves one unit and returns the resulting pending order.
	//
	// The error contract is the handler's, and it is identical for every arm:
	//
	//	ErrOutOfStock   the product exists but has no unit free   -> 409
	//	pgx.ErrNoRows   no such product                           -> 404
	//
	// Collapsing those two is an API regression, not an implementation
	// detail — cmd/contract_test.go asserts both, and the single-statement
	// arm has to work to keep them apart.
	Reserve(ctx context.Context, p ReserveParams) (repo.Order, error)
}

// ReserveParams is everything an arm needs to reserve a unit. ExpiresAt is
// computed by the caller so that every arm stamps the same TTL from the same
// clock read, rather than each one reaching for time.Now separately.
type ReserveParams struct {
	ProductID int64
	Claims    auth.Claims
	ExpiresAt pgtype.Timestamptz

	// IdempotencyKey is the caller's Idempotency-Key header, empty when they
	// did not send one. Only the idempotent wrapper reads it; the arms
	// themselves neither see nor care about it.
	IdempotencyKey string
}

// The arm names. These are configuration values (RESERVE_STRATEGY) and appear
// in committed benchmark results, so they are part of a contract: renaming one
// invalidates the recorded runs that reference it.
const (
	StrategyBaseline    = "baseline"     // B1 — today's statement order
	StrategyReserveLast = "reserve-last" // B2 / R0
	StrategyCTE         = "cte"          // B4 / R2
)

// StrategyNames is the authoritative list of arms.
//
// The oversell and contract tests range over it, so an arm cannot be added
// without being hammered by both — the same discipline routeAccess enforces for
// routes. Adding a name here and no implementation fails StrategyByName; adding
// an implementation and not the name leaves it untested. Do not shorten this
// list to skip a failing arm.
func StrategyNames() []string {
	return []string{StrategyBaseline, StrategyReserveLast, StrategyCTE}
}

// StrategyByName builds an arm.
//
// An *unrecognised* name is an error rather than a silent fallback: a typo in
// RESERVE_STRATEGY must not quietly benchmark the wrong thing, and cmd turns
// this into a boot panic the same way a bad DSN fails at startup.
//
// The empty string is different from a typo — it means "unset", which has a
// documented default here exactly as it does for every other knob in this
// codebase. That matters because config structs are also built by hand in
// tests, and requiring each one to name an arm would make adding an arm a
// change to every harness.
func StrategyByName(
	name string,
	q repo.Querier,
	db repo.Beginner,
	resolver customers.Resolver,
) (ReserveStrategy, error) {
	switch name {
	case "":
		return StrategyByName(StrategyBaseline, q, db, resolver)
	case StrategyBaseline:
		return &txnStrategy{repo: q, db: db, resolver: resolver, reserveLast: false}, nil
	case StrategyReserveLast:
		return &txnStrategy{repo: q, db: db, resolver: resolver, reserveLast: true}, nil
	case StrategyCTE:
		return &cteStrategy{repo: q, resolver: resolver}, nil
	default:
		return nil, fmt.Errorf("unknown reserve strategy %q, want one of %v", name, StrategyNames())
	}
}
