package testsupport

import (
	"context"
	"sync"
	"testing"
)

// The harness is load-bearing for every database-backed test in the repo, so
// it gets tested itself. If isolation silently stops working, every downstream
// test becomes order-dependent without failing.

func TestDBIsMigrated(t *testing.T) {
	t.Parallel()

	pool := DB(t)

	var tables int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('products','stock','customers','orders','stripe_events')
	`).Scan(&tables)
	if err != nil {
		t.Fatalf("query schema: %v", err)
	}

	if tables != 5 {
		t.Errorf("found %d of the 5 expected tables; migrations did not fully apply", tables)
	}
}

func TestDBStartsEmpty(t *testing.T) {
	t.Parallel()

	pool := DB(t)

	for _, table := range []string{"products", "stock", "customers", "orders", "stripe_events"} {
		var count int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s has %d rows in a fresh database, want 0", table, count)
		}
	}
}

// The property the whole design rests on: one test's writes must be invisible
// to another, even running concurrently.
func TestDBIsolatesConcurrentTests(t *testing.T) {
	t.Parallel()

	const writers = 4
	var wg sync.WaitGroup

	for i := range writers {
		wg.Add(1)
		t.Run("writer", func(t *testing.T) {
			defer wg.Done()

			pool := DB(t)
			ctx := context.Background()

			// Every writer inserts the same number of rows into its own
			// database. If they shared one, the counts would disagree.
			for range i + 1 {
				if _, err := pool.Exec(ctx,
					`INSERT INTO products (name, price_in_cents) VALUES ($1, 100)`,
					"isolation-probe",
				); err != nil {
					t.Fatalf("insert: %v", err)
				}
			}

			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&count); err != nil {
				t.Fatalf("count: %v", err)
			}
			if count != i+1 {
				t.Errorf("saw %d products, want %d — databases are not isolated", count, i+1)
			}
		})
	}

	wg.Wait()
}

// Identity columns must restart per database, so tests can assume ids are
// predictable rather than carrying over from a previous run.
func TestDBHasFreshIdentitySequences(t *testing.T) {
	t.Parallel()

	pool := DB(t)

	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO products (name, price_in_cents) VALUES ('first', 1) RETURNING id`,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if id != 1 {
		t.Errorf("first product id = %d, want 1; the template carried state", id)
	}
}

func TestReplaceDBName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "keeps credentials host and query",
			dsn:  "postgres://u:p@localhost:5432/doorbust_test?sslmode=disable",
			want: "postgres://u:p@localhost:5432/target?sslmode=disable",
		},
		{
			name: "no query string",
			dsn:  "postgres://u:p@localhost:5432/doorbust_test",
			want: "postgres://u:p@localhost:5432/target",
		},
		{
			name: "no database path at all",
			dsn:  "postgres://u:p@localhost:5432",
			want: "postgres://u:p@localhost:5432/target",
		},
		{
			// Neon DSNs carry several parameters; dropping one would send
			// tests at a differently-configured connection.
			name: "multiple query parameters survive",
			dsn:  "postgres://u:p@host/db?sslmode=require&channel_binding=require",
			want: "postgres://u:p@host/target?sslmode=require&channel_binding=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := replaceDBName(tt.dsn, "target"); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
