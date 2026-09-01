package postgresql_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
	"github.com/pressly/goose/v3"
)

// Migrations must reverse as well as apply. The deploy runs `goose up` against
// the production database before the container restarts, so a migration that
// cannot be rolled back leaves no way out of a bad release except a
// hand-written repair.

func TestMigrationsRoundTrip(t *testing.T) {
	t.Parallel()

	// A disposable database of its own: this test migrates it all the way
	// down, which would destroy the schema every other test depends on.
	pool := testsupport.DB(t)
	dsn := pool.Config().ConnString()
	pool.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// goose's logger and dialect are package globals; configuring them here
	// would race with the harness and with the other parallel test.
	if err := testsupport.SetupGoose(); err != nil {
		t.Fatalf("set dialect: %v", err)
	}

	dir := testsupport.MigrationsDir()

	applied, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if applied == 0 {
		t.Fatal("expected the template to arrive already migrated")
	}

	// All the way down. Each Down block must actually work, including the
	// data fix-ups in 00006 that fold newer order statuses back into the
	// narrower CHECK constraint the older schema allows.
	if err := goose.DownTo(db, dir, 0); err != nil {
		t.Fatalf("migrate down to zero: %v", err)
	}

	if version, err := goose.GetDBVersion(db); err != nil || version != 0 {
		t.Fatalf("version after down = %d (err %v), want 0", version, err)
	}

	for _, table := range []string{"products", "stock", "customers", "orders", "stripe_events"} {
		var exists bool
		if err := db.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = $1)`, table,
		).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if exists {
			t.Errorf("%s survived a full rollback", table)
		}
	}

	// And back up, to the same version.
	if err := goose.Up(db, dir); err != nil {
		t.Fatalf("migrate back up: %v", err)
	}

	if version, err := goose.GetDBVersion(db); err != nil || version != applied {
		t.Fatalf("version after re-up = %d (err %v), want %d", version, err, applied)
	}
}

// A migration that only reverses on an empty database is not reversible in
// practice — production has rows. 00006 in particular has to fold statuses
// that its own Up added back into the older CHECK constraint.
func TestMigrationsReverseWithDataPresent(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	ctx := context.Background()

	productID := testsupport.SeedProduct(t, pool, "rollback", 1500, 5)
	customer := testsupport.SeedCustomer(t, pool, "rollback@example.test", "sub-rollback")

	// One order in each of the statuses the newer schema introduced.
	for _, status := range []string{"pending", "awaiting_payment", "completed", "failed", "expired", "cancelled"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO orders (customer_id, product_id, status, total_in_cents, expires_at)
			 VALUES ($1, $2, $3, 1500, now())`,
			customer.ID, productID, status,
		); err != nil {
			t.Fatalf("insert %s order: %v", status, err)
		}
	}

	dsn := pool.Config().ConnString()
	pool.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := testsupport.SetupGoose(); err != nil {
		t.Fatalf("set dialect: %v", err)
	}

	// Down one step at a time to the schema before the payment columns.
	if err := goose.DownTo(db, testsupport.MigrationsDir(), 5); err != nil {
		t.Fatalf("roll back the payment migration with data present: %v", err)
	}

	// Every row must have landed in a status the older constraint permits.
	var bad int
	if err := db.QueryRow(
		`SELECT count(*) FROM orders WHERE status NOT IN ('pending','completed')`,
	).Scan(&bad); err != nil {
		t.Fatalf("count: %v", err)
	}
	if bad != 0 {
		t.Errorf("%d orders left in a status the rolled-back schema forbids", bad)
	}
}

// NewPool pings on construction so a bad DSN fails at startup rather than on
// the first request — main.go panics on this path.
func TestNewPool(t *testing.T) {
	t.Parallel()

	t.Run("succeeds against a live database", func(t *testing.T) {
		t.Parallel()

		if testsupport.DatabaseURL() == "" {
			t.Skip("TEST_DATABASE_URL not set")
		}
		pool := testsupport.DB(t)
		dsn := pool.Config().ConnString()
		pool.Close()

		got, err := postgresql.NewPool(context.Background(), postgresql.Config{
			DSN:             dsn,
			MaxConns:        4,
			MinConns:        1,
			MinIdleConns:    1,
			MaxConnLifetime: time.Hour,
			MaxConnIdleTime: time.Minute,
		})
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		defer got.Close()

		if got.Config().MaxConns != 4 {
			t.Errorf("MaxConns = %d, want 4", got.Config().MaxConns)
		}
	})

	t.Run("rejects an unparseable DSN", func(t *testing.T) {
		t.Parallel()

		if _, err := postgresql.NewPool(context.Background(), postgresql.Config{DSN: "not a dsn"}); err == nil {
			t.Error("expected a malformed DSN to fail")
		}
	})

	t.Run("fails when the database is unreachable", func(t *testing.T) {
		t.Parallel()

		_, err := postgresql.NewPool(context.Background(), postgresql.Config{
			DSN:      "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1",
			MaxConns: 1,
		})
		if err == nil {
			t.Error("expected an unreachable database to fail at construction")
		}
	})
}
