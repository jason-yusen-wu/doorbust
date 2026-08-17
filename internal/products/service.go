package products

import (
	"context"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
)

// business logic lives here

type Service interface {
	ListProducts(ctx context.Context) ([]repo.Product, error)
}

// implements Service interface
type svc struct {
	repo repo.Querier
}

func NewService(repo repo.Querier) Service {
	return &svc{
		repo: repo,
	}
}

func (s *svc) ListProducts(ctx context.Context) ([]repo.Product, error) {
	return s.repo.ListProducts(ctx)
}
