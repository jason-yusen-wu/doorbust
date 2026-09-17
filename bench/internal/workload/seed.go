package workload

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/pressly/goose/v3"
)

// Seeding talks to the database directly rather than reusing
// internal/testsupport/seed.go, and that is deliberate.
//
// Those helpers are *testing.T-coupled, and the generated sqlc querier they
// wrap already IS the shared core — there is nothing left to extract that is
// not already shared. Meanwhile a benchmark needs things the test harness
// should never grow: TRUNCATE between arms, bulk creation of a thousand SKUs,
// and a bulk customer pre-seed. Pushing those into the test harness would add
// benchmark concerns to it for no reuse.
//
// Contrast internal/testsupport/fakeoidc, where extraction WAS right: that
// duplication was a crypto core, and the point of unifying it is that the
// tokens used here are verified by exactly the path the tests cover.

// Migrate brings the benchmark database up to the current schema.
func Migrate(ctx context.Context, dsn, migrationsDir string) error {
	// goose drives database/sql, not pgx directly. Importing pgx's stdlib
	// package registers the "pgx" driver, so migrations run over the same
	// driver the app uses rather than pulling in a second Postgres client.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open for migrations: %w", err)
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	goose.SetLogger(goose.NopLogger())

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// Reset empties every table between arms.
//
// RESTART IDENTITY matters for more than tidiness: orders_id_seq is
// non-transactional, so a doorbuster run burns ids for every failed attempt,
// not just every order. Without a restart the sequence climbs across a whole
// sweep, and anyone reading max(orders.id) as "orders created" gets a wildly
// wrong number. CASCADE is required because orders and stock both reference
// products.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx,
		`TRUNCATE orders, stock, products, customers, stripe_events RESTART IDENTITY CASCADE`)
	if err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	return nil
}

// SeedProducts creates the plan's SKUs and returns their ids in pick order.
func SeedProducts(ctx context.Context, pool *pgxpool.Pool, plan Plan) ([]int64, error) {
	q := repo.New(pool)
	ids := make([]int64, plan.SKUs)

	// start_at in the past, or the product is an upcoming sale rather than a
	// live one and nothing can be reserved against it.
	startAt := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}

	for i := range ids {
		product, err := q.CreateProduct(ctx, repo.CreateProductParams{
			Name:         fmt.Sprintf("bench-%s-%d", plan.Name, i),
			PriceInCents: 1999,
			StartAt:      startAt,
		})
		if err != nil {
			return nil, fmt.Errorf("create product %d: %w", i, err)
		}
		if _, err := q.CreateStock(ctx, repo.CreateStockParams{
			ProductID: product.ID,
			Quantity:  plan.Quantity,
		}); err != nil {
			return nil, fmt.Errorf("create stock %d: %w", i, err)
		}
		ids[i] = product.ID
	}
	return ids, nil
}

// SeedCustomers creates the customers rows for a pool of buyers up front.
//
// This puts the run's steady state on the path production actually spends its
// time in: FindCustomerBySub hitting an existing row. Without it the first
// request per buyer is an insert, which measures a different statement and
// would make the customer-cache arm look better than it is by comparing a
// cached lookup against an uncached insert.
func SeedCustomers(ctx context.Context, pool *pgxpool.Pool, buyers []Buyer) error {
	q := repo.New(pool)
	for _, b := range buyers {
		if _, err := q.LinkCustomer(ctx, repo.LinkCustomerParams{
			Email:      b.Email,
			CognitoSub: pgtype.Text{String: b.Subject, Valid: true},
		}); err != nil {
			return fmt.Errorf("seed customer %s: %w", b.Subject, err)
		}
	}
	return nil
}
