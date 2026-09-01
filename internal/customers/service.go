// Package customers bridges Cognito identity to app identity.
//
// A user who signs up through the Cognito Hosted UI exists in the user pool
// but has no row in our customers table — the two identity stores are
// separate, and nothing used to connect them until the user's first
// successful order. That made "who am I" an implicit side effect of buying
// something. GET /me makes it an explicit part of the contract.
package customers

import (
	"context"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

// business logic lives here

type Service interface {
	GetOrCreate(ctx context.Context, claims auth.Claims) (repo.Customer, error)
}

// implements Service interface
type svc struct {
	repo repo.Querier
}

func NewService(repo repo.Querier) Service {
	return &svc{repo: repo}
}

// GetOrCreate is idempotent, safe against concurrent first-requests, and
// tolerant of a Cognito email change. See Link for why that takes more than a
// single upsert.
func (s *svc) GetOrCreate(ctx context.Context, claims auth.Claims) (repo.Customer, error) {
	return Link(ctx, s.repo, claims)
}
