package customers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

// uniqueViolation is Postgres' error code for a duplicate key.
const uniqueViolation = "23505"

// linkAttempts bounds the retry below. One retry is enough in practice — the
// loser of a race finds the winner's row on its second pass — but a small
// budget costs nothing and avoids a hard-coded assumption about scheduling.
const linkAttempts = 3

// Link resolves a verified Cognito caller to a customers row, creating it if
// this is their first request.
//
// It lives here rather than being inlined at each call site because both
// GET /me and POST /orders need it, and the correct version is not a single
// statement. The customers table carries *two* unique constraints — email and
// cognito_sub — and a plain `INSERT ... ON CONFLICT (email)` only arbitrates
// one of them. That left two real failures:
//
//   - A user who changes their email in Cognito keeps their subject. The
//     upsert sees no email conflict, inserts, and violates the cognito_sub
//     constraint — permanently, on every subsequent request.
//   - Two concurrent first-requests (the frontend calls /me on load, so two
//     tabs is enough) can race such that the loser trips the cognito_sub
//     constraint rather than the email one, and gets a 500.
//
// So: resolve by subject first, since that is the identity that does not
// change; fall back to the email upsert for callers with no subject and for
// rows created before cognito_sub existed; and retry a lost race rather than
// surfacing it.
func Link(ctx context.Context, q repo.Querier, claims auth.Claims) (repo.Customer, error) {
	var lastErr error

	for range linkAttempts {
		customer, err := linkOnce(ctx, q, claims)
		if err == nil {
			return customer, nil
		}
		if !isUniqueViolation(err) {
			return repo.Customer{}, err
		}
		// Someone else created the row between our lookup and our insert.
		// The next pass finds it.
		lastErr = err
	}

	return repo.Customer{}, lastErr
}

func linkOnce(ctx context.Context, q repo.Querier, claims auth.Claims) (repo.Customer, error) {
	sub := pgtype.Text{String: claims.Subject, Valid: claims.Subject != ""}

	if sub.Valid {
		existing, err := q.FindCustomerBySub(ctx, sub)
		switch {
		case err == nil:
			if existing.Email == claims.Email || claims.Email == "" {
				return existing, nil
			}
			// The email changed in Cognito. Adopt it onto the row the subject
			// already owns rather than trying to insert a second one.
			return q.UpdateCustomerEmail(ctx, repo.UpdateCustomerEmailParams{
				ID:    existing.ID,
				Email: claims.Email,
			})
		case !errors.Is(err, pgx.ErrNoRows):
			return repo.Customer{}, err
		}
	}

	// No row for this subject yet: upsert on email, which also adopts rows
	// created before cognito_sub existed.
	return q.LinkCustomer(ctx, repo.LinkCustomerParams{
		Email:      claims.Email,
		CognitoSub: sub,
	})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
