package products

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

func TestCreateProductWritesProductAndStockAtomically(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool), pool)

	got, err := service.CreateProduct(context.Background(), CreateProductParams{
		Name:         "atomic",
		PriceInCents: 4200,
		StartAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Quantity:     9,
	})
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}

	if got.Name != "atomic" || got.PriceInCents != 4200 || got.Quantity != 9 {
		t.Errorf("got %+v, want name=atomic price=4200 quantity=9", got)
	}
	if got.NumReserved != 0 {
		t.Errorf("num_reserved = %d on a new product, want 0", got.NumReserved)
	}

	// A product is useless without its stock row: every read path joins them,
	// so a product without one would be invisible to /products entirely.
	if n := testsupport.CountRows(t, pool, "products"); n != 1 {
		t.Errorf("products = %d, want 1", n)
	}
	if n := testsupport.CountRows(t, pool, "stock"); n != 1 {
		t.Errorf("stock = %d, want 1", n)
	}
}

// The insert runs in one transaction so a failure cannot leave a product with
// no stock row. A negative quantity trips the CHECK constraint on stock, which
// happens after the product insert — exactly the window the transaction exists
// to cover.
func TestCreateProductRollsBackOnStockFailure(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool), pool)

	_, err := service.CreateProduct(context.Background(), CreateProductParams{
		Name:         "doomed",
		PriceInCents: 100,
		StartAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Quantity:     -1,
	})
	if err == nil {
		t.Fatal("expected a negative quantity to violate the stock CHECK constraint")
	}

	if n := testsupport.CountRows(t, pool, "products"); n != 0 {
		t.Errorf("products = %d after a failed create, want 0 — the transaction did not roll back", n)
	}
	if n := testsupport.CountRows(t, pool, "stock"); n != 0 {
		t.Errorf("stock = %d after a failed create, want 0", n)
	}
}

func TestListProductsOrdersSoldOutLast(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool), pool)
	ctx := context.Background()

	available := testsupport.SeedProduct(t, pool, "in-stock", 100, 5)
	soldOut := testsupport.SeedProduct(t, pool, "sold-out", 100, 0)

	got, err := service.ListProducts(ctx, 20, 0)
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d products, want 2", len(got))
	}

	// A storefront should not lead with things nobody can buy.
	if got[0].ID != available {
		t.Errorf("first product = %d, want the in-stock one (%d)", got[0].ID, available)
	}
	if got[1].ID != soldOut {
		t.Errorf("last product = %d, want the sold-out one (%d)", got[1].ID, soldOut)
	}
}

func TestListProductsPagination(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool), pool)
	ctx := context.Background()

	for i := range 5 {
		testsupport.SeedProduct(t, pool, string(rune('a'+i)), 100, 1)
	}

	page, err := service.ListProducts(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if len(page) != 2 {
		t.Errorf("got %d products, want 2", len(page))
	}

	rest, err := service.ListProducts(ctx, 10, 4)
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if len(rest) != 1 {
		t.Errorf("got %d products on the last page, want 1", len(rest))
	}

	empty, err := service.ListProducts(ctx, 10, 100)
	if err != nil {
		t.Fatalf("ListProducts past the end: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("got %d products past the end, want 0", len(empty))
	}
}

func TestGetProduct(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool), pool)
	ctx := context.Background()

	id := testsupport.SeedProduct(t, pool, "findable", 777, 3)

	got, err := service.GetProduct(ctx, id)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if got.ID != id || got.PriceInCents != 777 || got.Quantity != 3 {
		t.Errorf("got %+v", got)
	}

	if _, err := service.GetProduct(ctx, 999999); err == nil {
		t.Error("expected an error for a missing product")
	}
}
