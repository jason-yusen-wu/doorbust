package products

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
)

// business logic lives here

type Service interface {
	ListProducts(ctx context.Context, limit, offset int32) ([]repo.ListProductsRow, error)
	GetProduct(ctx context.Context, id int64) (repo.FindProductByIDRow, error)
	CreateProduct(ctx context.Context, params CreateProductParams) (repo.FindProductByIDRow, error)
}

// CreateProductParams are the fields a vendor supplies to add a sale event.
type CreateProductParams struct {
	Name         string
	PriceInCents int32
	StartAt      pgtype.Timestamptz
	Quantity     int32
}

// implements Service interface
type svc struct {
	repo repo.Querier
	db   repo.Beginner
}

func NewService(repo repo.Querier, db repo.Beginner) Service {
	return &svc{
		repo: repo,
		db:   db,
	}
}

func (s *svc) ListProducts(ctx context.Context, limit, offset int32) ([]repo.ListProductsRow, error) {
	return s.repo.ListProducts(ctx, repo.ListProductsParams{Limit: limit, Offset: offset})
}

func (s *svc) GetProduct(ctx context.Context, id int64) (repo.FindProductByIDRow, error) {
	return s.repo.FindProductByID(ctx, id)
}

// CreateProduct inserts the product and its initial stock row together in one
// transaction, since every product is expected to have exactly one stock row.
func (s *svc) CreateProduct(ctx context.Context, params CreateProductParams) (repo.FindProductByIDRow, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repo.FindProductByIDRow{}, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	product, err := q.CreateProduct(ctx, repo.CreateProductParams{
		Name:         params.Name,
		PriceInCents: params.PriceInCents,
		StartAt:      params.StartAt,
	})
	if err != nil {
		return repo.FindProductByIDRow{}, err
	}

	stock, err := q.CreateStock(ctx, repo.CreateStockParams{
		ProductID: product.ID,
		Quantity:  params.Quantity,
	})
	if err != nil {
		return repo.FindProductByIDRow{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return repo.FindProductByIDRow{}, err
	}

	return repo.FindProductByIDRow{
		ID:           product.ID,
		Name:         product.Name,
		PriceInCents: product.PriceInCents,
		CreatedAt:    product.CreatedAt,
		StartAt:      product.StartAt,
		Quantity:     stock.Quantity,
		NumReserved:  stock.NumReserved,
	}, nil
}
