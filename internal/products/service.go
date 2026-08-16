package products

import "context"

// business logic lives here

type Service interface {
	ListProducts(ctx context.Context) error
}

// implements Service interface
type svc struct {
	// repository
}

func NewService() Service {
	return &svc{}
}

func (s *svc) ListProducts(ctx context.Context) error {
	return nil
}
