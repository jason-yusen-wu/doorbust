package run

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
)

// execTimeRE pulls the server-side execution time out of EXPLAIN ANALYZE.
var execTimeRE = regexp.MustCompile(`Execution Time: ([0-9.]+) ms`)

// ProbeExec measures how long the server spends executing the arm's critical
// statement — the third term of the model, and the only one that does not
// vanish when the client network does.
//
// It matters most for the CTE arm. That arm holds the lock for server execution
// alone, so exec time IS its predicted ceiling; without this the prediction is a
// division by zero rather than a number.
//
// Run inside a transaction that is always rolled back. EXPLAIN ANALYZE really
// executes the statement, so probing would otherwise reserve stock and create
// orders before the run starts, and those would then show up in the invariant
// cross-check as orders the client never asked for.
func ProbeExec(ctx context.Context, pool *pgxpool.Pool, arm string, productID, customerID int64) (float64, error) {
	stmt, args := criticalStatement(arm, productID, customerID)

	samples := make([]float64, 0, 32)
	for range 32 {
		ms, err := explainOnce(ctx, pool, stmt, args)
		if err != nil {
			return 0, err
		}
		samples = append(samples, ms*1000) // ms -> us
	}

	slices.Sort(samples)
	return samples[len(samples)/2], nil
}

// criticalStatement is the statement whose execution the stock row's lock is
// held across, per arm. For the transactional arms that is the guarded UPDATE
// alone; for the CTE arm it is the whole reserve-and-insert.
func criticalStatement(arm string, productID, customerID int64) (string, []any) {
	switch arm {
	case orders.StrategyCTE:
		return `
			WITH reserved AS (
			    UPDATE stock SET num_reserved = num_reserved + 1
			    WHERE product_id = $1::bigint AND quantity - num_reserved > 0
			    RETURNING product_id
			), priced AS (
			    SELECT p.id, p.price_in_cents FROM products p
			    JOIN reserved r ON r.product_id = p.id
			)
			INSERT INTO orders (customer_id, product_id, total_in_cents, expires_at)
			SELECT $2::bigint, priced.id, priced.price_in_cents, $3::timestamptz
			FROM priced RETURNING *`,
			[]any{productID, customerID, time.Now().Add(time.Hour)}

	default:
		return `
			UPDATE stock SET num_reserved = num_reserved + 1
			WHERE product_id = $1::bigint AND quantity - num_reserved > 0
			RETURNING *`,
			[]any{productID}
	}
}

func explainOnce(ctx context.Context, pool *pgxpool.Pool, stmt string, args []any) (float64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is the point

	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, TIMING ON) "+stmt, args...)
	if err != nil {
		return 0, fmt.Errorf("explain: %w", err)
	}

	var plan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			return 0, err
		}
		plan += line + "\n"
	}
	rows.Close()
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return 0, err
	}

	m := execTimeRE.FindStringSubmatch(plan)
	if m == nil {
		return 0, fmt.Errorf("no execution time in plan:\n%s", plan)
	}
	return strconv.ParseFloat(m[1], 64)
}
