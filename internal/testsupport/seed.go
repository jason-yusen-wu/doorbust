package testsupport

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
)

// Seeding helpers. Because every test gets its own database, these do not need
// to generate unique names to avoid collisions — the previous suite's
// nanosecond-suffix trick is no longer necessary, and ids are predictable.

// SeedProduct creates a product with its stock row and returns the product id.
func SeedProduct(t *testing.T, pool *pgxpool.Pool, name string, priceInCents, quantity int32) int64 {
	t.Helper()

	ctx := context.Background()
	q := repo.New(pool)

	product, err := q.CreateProduct(ctx, repo.CreateProductParams{
		Name:         name,
		PriceInCents: priceInCents,
		// start_at is NOT NULL and named explicitly in the insert, so the
		// column default never applies.
		StartAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("seed product %q: %v", name, err)
	}

	if _, err := q.CreateStock(ctx, repo.CreateStockParams{
		ProductID: product.ID,
		Quantity:  quantity,
	}); err != nil {
		t.Fatalf("seed stock for %q: %v", name, err)
	}

	return product.ID
}

// SeedCustomer creates a customer linked to a Cognito subject.
func SeedCustomer(t *testing.T, pool *pgxpool.Pool, email, subject string) repo.Customer {
	t.Helper()

	customer, err := repo.New(pool).LinkCustomer(context.Background(), repo.LinkCustomerParams{
		Email:      email,
		CognitoSub: pgtype.Text{String: subject, Valid: subject != ""},
	})
	if err != nil {
		t.Fatalf("seed customer %q: %v", email, err)
	}
	return customer
}

// Stock reads a product's current inventory counters.
func Stock(t *testing.T, pool *pgxpool.Pool, productID int64) repo.FindProductByIDRow {
	t.Helper()

	row, err := repo.New(pool).FindProductByID(context.Background(), productID)
	if err != nil {
		t.Fatalf("read stock for product %d: %v", productID, err)
	}
	return row
}

// Order reads an order by id.
func Order(t *testing.T, pool *pgxpool.Pool, orderID int64) repo.FindOrderByIDRow {
	t.Helper()

	row, err := repo.New(pool).FindOrderByID(context.Background(), orderID)
	if err != nil {
		t.Fatalf("read order %d: %v", orderID, err)
	}
	return row
}

// AssertStock fails unless the product's counters match exactly. Taking both
// numbers together matters: quantity alone cannot distinguish "sold" from
// "still reserved", and that distinction is the entire two-phase design.
func AssertStock(t *testing.T, pool *pgxpool.Pool, productID int64, wantQuantity, wantReserved int32) {
	t.Helper()

	got := Stock(t, pool, productID)
	if got.Quantity != wantQuantity || got.NumReserved != wantReserved {
		t.Errorf("product %d: quantity=%d num_reserved=%d, want %d/%d",
			productID, got.Quantity, got.NumReserved, wantQuantity, wantReserved)
	}
	if got.NumReserved > got.Quantity {
		t.Errorf("product %d oversold: num_reserved %d > quantity %d",
			productID, got.NumReserved, got.Quantity)
	}
}

// AssertOrderStatus fails unless the order is in the expected state.
func AssertOrderStatus(t *testing.T, pool *pgxpool.Pool, orderID int64, want string) {
	t.Helper()

	if got := Order(t, pool, orderID); got.Status != want {
		t.Errorf("order %d status = %q, want %q", orderID, got.Status, want)
	}
}

// CountRows is a small escape hatch for assertions the typed queries don't
// cover, such as inspecting the stripe_events inbox.
func CountRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(context.Background(), fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
