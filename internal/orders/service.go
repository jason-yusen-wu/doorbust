package orders

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
)

// business logic lives here

var (
	ErrOutOfStock      = errors.New("product is out of stock")
	ErrOrderNotPending = errors.New("order is not pending")
)

type Service interface {
	GetOrder(ctx context.Context, id int64) (repo.Order, error)
	CreateOrder(ctx context.Context, productID int64, customerEmail string) (repo.Order, error)
	Checkout(ctx context.Context, orderID int64) (repo.Order, error)
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

func (s *svc) GetOrder(ctx context.Context, id int64) (repo.Order, error) {
	return s.repo.FindOrderByID(ctx, id)
}

// CreateOrder is the reserve half of the two-phase buy flow: it finds/creates
// the customer, reserves one unit of stock, and creates a 'pending' order, all
// in one transaction. A separate call to Checkout finalizes the sale.
func (s *svc) CreateOrder(ctx context.Context, productID int64, customerEmail string) (repo.Order, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repo.Order{}, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	customer, err := q.FindOrCreateCustomer(ctx, customerEmail)
	if err != nil {
		return repo.Order{}, err
	}

	if _, err := q.ReserveStock(ctx, productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Order{}, ErrOutOfStock
		}
		return repo.Order{}, err
	}

	order, err := q.CreateOrder(ctx, repo.CreateOrderParams{
		CustomerID: customer.ID,
		ProductID:  productID,
	})
	if err != nil {
		return repo.Order{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return repo.Order{}, err
	}

	return order, nil
}

// Checkout is a stand-in for a real payment gateway: it marks a pending order
// completed and commits its reserved stock, atomically and idempotently
// (guarded by CompleteOrder's status = 'pending' check, so a duplicate/racing
// call can't double-commit stock).
func (s *svc) Checkout(ctx context.Context, orderID int64) (repo.Order, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return repo.Order{}, err
	}
	defer tx.Rollback(ctx)

	q := repo.New(tx)

	order, err := q.CompleteOrder(ctx, orderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Order{}, ErrOrderNotPending
		}
		return repo.Order{}, err
	}

	if _, err := q.CommitStock(ctx, order.ProductID); err != nil {
		return repo.Order{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return repo.Order{}, err
	}

	return order, nil
}
