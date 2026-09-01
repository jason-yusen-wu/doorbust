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

	"github.com/jackc/pgx/v5/pgtype"
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

// GetOrCreate is idempotent by construction: LinkCustomer is an upsert, so
// concurrent first-requests from the same user can't race into duplicate
// rows, and a repeat call is just a read that also backfills cognito_sub.
func (s *svc) GetOrCreate(ctx context.Context, claims auth.Claims) (repo.Customer, error) {
	return s.repo.LinkCustomer(ctx, repo.LinkCustomerParams{
		Email:      claims.Email,
		CognitoSub: pgtype.Text{String: claims.Subject, Valid: claims.Subject != ""},
	})
}
