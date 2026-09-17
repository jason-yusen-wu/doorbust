// Package verify asserts, after the load stops, that the inventory the server
// was hammered with is still internally consistent.
//
// This is what makes the benchmark a correctness test under load rather than a
// speed test, which is the whole thesis of the project: a fast reserve path
// that oversells is not a result.
package verify

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jason-yusen-wu/doorbust/bench/internal/report"
)

// Check runs every invariant and returns them individually, so a failure says
// which property broke rather than just that something did.
func Check(ctx context.Context, pool *pgxpool.Pool, productIDs []int64, initialQty int32, clientReserved int64) report.Invariants {
	var checks []report.InvariantCheck

	add := func(name string, ok bool, format string, args ...any) {
		c := report.InvariantCheck{Name: name, OK: ok}
		if !ok {
			c.Detail = fmt.Sprintf(format, args...)
		}
		checks = append(checks, c)
	}

	// The load-bearing assertion, and the one only a load generator can make.
	//
	// Every other check below can also be reached from a unit test. This one
	// compares what the CLIENT observed against what the database holds, so it
	// catches a 201 whose order is not there, and an order created for a
	// request that reported an error. Nothing inside the server can see that
	// class of bug, because the server is the thing under suspicion.
	var orders int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&orders); err != nil {
		add("client_server_order_count", false, "counting orders: %v", err)
	} else {
		add("client_server_order_count", orders == clientReserved,
			"client saw %d successful reserves but the orders table holds %d", clientReserved, orders)
	}

	for _, id := range productIDs {
		var quantity, reserved int32
		err := pool.QueryRow(ctx,
			`SELECT quantity, num_reserved FROM stock WHERE product_id = $1`, id).
			Scan(&quantity, &reserved)
		if err != nil {
			add(fmt.Sprintf("stock_readable_%d", id), false, "reading stock: %v", err)
			continue
		}

		// Both of these are also CHECK constraints on the stock table, so an
		// oversell could never have been silent — it would have failed the
		// write and surfaced during the run as a 5xx. Asserting them anyway is
		// cheap, and it documents what the run was trying to break.
		add("num_reserved_non_negative", reserved >= 0,
			"product %d has num_reserved = %d", id, reserved)
		add("num_reserved_within_quantity", reserved <= quantity,
			"product %d reserved %d of %d", id, reserved, quantity)

		// The accounting identity: a reservation is either still held by a
		// live order or it has been released. A mismatch means a unit leaked —
		// reserved and then forgotten — which is the failure mode that quietly
		// takes stock off sale forever.
		var live int64
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM orders
			WHERE product_id = $1 AND status IN ('pending', 'awaiting_payment')`, id).Scan(&live); err != nil {
			add("reserved_matches_live_orders", false, "counting live orders: %v", err)
			continue
		}
		add("reserved_matches_live_orders", int64(reserved) == live,
			"product %d: num_reserved = %d but %d orders are live", id, reserved, live)

		// The oversell claim itself, stated in terms of the product rather than
		// the counter: no more orders can exist than there were units to sell.
		var sold int64
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM orders
			WHERE product_id = $1 AND status IN ('pending', 'awaiting_payment', 'completed')`, id).Scan(&sold); err != nil {
			add("orders_within_initial_quantity", false, "counting orders: %v", err)
			continue
		}
		add("orders_within_initial_quantity", sold <= int64(initialQty),
			"product %d: %d orders against an initial quantity of %d", id, sold, initialQty)
	}

	passed := true
	for _, c := range checks {
		if !c.OK {
			passed = false
		}
	}
	return report.Invariants{Passed: passed, Checks: checks}
}
