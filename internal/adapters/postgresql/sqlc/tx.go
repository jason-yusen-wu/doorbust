package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Beginner starts a database transaction. Implemented by *pgxpool.Pool, which
// checks a connection out of the pool for the life of the transaction.
// Feature services use it to run a multi-statement operation (e.g. reserving
// stock and creating an order) atomically via repo.New(tx).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}
