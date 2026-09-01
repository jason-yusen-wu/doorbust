-- name: ListProducts :many
-- Sold-out products sort last; everything else sorts by start_at (already-started
-- sales first, soonest-upcoming next). Among sold-out products, the one that
-- started most recently sorts last, standing in for "most recently ended last"
-- since there's no explicit end time.
SELECT
    p.id, p.name, p.price_in_cents, p.created_at, p.start_at,
    s.quantity, s.num_reserved
FROM products p
JOIN stock s ON s.product_id = p.id
ORDER BY (s.quantity - s.num_reserved <= 0), p.start_at ASC
LIMIT $1 OFFSET $2;

-- name: FindProductByID :one
SELECT
    p.id, p.name, p.price_in_cents, p.created_at, p.start_at,
    s.quantity, s.num_reserved
FROM products p
JOIN stock s ON s.product_id = p.id
WHERE p.id = $1;

-- name: CreateProduct :one
INSERT INTO products (name, price_in_cents, start_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: CreateStock :one
INSERT INTO stock (product_id, quantity)
VALUES ($1, $2)
RETURNING *;

-- name: ReserveStock :one
-- Atomic check-and-increment: only succeeds while quantity - num_reserved > 0,
-- so concurrent callers can never over-reserve a product's stock.
--
-- This statement is the project's core correctness claim. Deliberately left
-- untouched by the payment/expiry work so the contention strategy stays
-- swappable (Redis counters, advisory locks, SERIALIZABLE) after profiling.
UPDATE stock
SET num_reserved = num_reserved + 1
WHERE product_id = $1 AND quantity - num_reserved > 0
RETURNING *;

-- name: CommitStock :one
-- Finalizes a reservation at checkout: moves one unit out of quantity and
-- releases the matching reservation.
UPDATE stock
SET quantity = quantity - 1, num_reserved = num_reserved - 1
WHERE product_id = $1 AND num_reserved > 0
RETURNING *;

-- name: ReleaseStock :one
-- The other way a reservation ends: cancelled, payment failed, or expired.
-- Puts the unit back on sale without touching quantity. Guarded on
-- num_reserved > 0 so a double release can't drive the counter negative.
--
-- Always call this in the same transaction as the orders-table guard that
-- authorized it (CancelPendingOrder / FailOrder / ExpireOrders). That guard
-- is what makes the release happen at most once per order; this WHERE clause
-- is only the backstop.
UPDATE stock
SET num_reserved = num_reserved - 1
WHERE product_id = $1 AND num_reserved > 0
RETURNING *;

-- name: FindCustomerBySub :one
-- Looked up before any insert is attempted. The Cognito subject is the stable
-- identity; email is not, and a user who changes it in Cognito would otherwise
-- reach the insert below with a new email, conflict on cognito_sub rather than
-- email, and be permanently unable to use the app.
SELECT * FROM customers WHERE cognito_sub = $1;

-- name: UpdateCustomerEmail :one
-- Adopts a changed email onto the row the subject already owns.
UPDATE customers SET email = $2 WHERE id = $1 RETURNING *;

-- name: LinkCustomer :one
-- Bridges a Cognito identity to a customers row, and backfills cognito_sub on
-- rows created before that column existed.
--
-- Still upserts on email (the pre-existing unique constraint) so no data
-- migration is needed. COALESCE keeps the first sub ever recorded rather than
-- letting a later caller overwrite it. This is the expand phase: once every
-- row has a sub, the conflict target can flip to cognito_sub, which closes
-- the hole that email is mutable in Cognito and sub is not.
INSERT INTO customers (email, cognito_sub)
VALUES ($1, $2)
ON CONFLICT (email) DO UPDATE
    SET cognito_sub = COALESCE(customers.cognito_sub, EXCLUDED.cognito_sub)
RETURNING *;

-- name: FindOrderByID :one
-- Joins the owning customer's identity so callers can check ownership without
-- a second round trip. cognito_sub is preferred over email for that check;
-- it is NULL only for customers who predate the column.
SELECT o.*, c.email AS customer_email, c.cognito_sub AS customer_cognito_sub
FROM orders o
JOIN customers c ON c.id = o.customer_id
WHERE o.id = $1;

-- name: FindOrderByPaymentIntentID :one
-- The Stripe webhook's only handle back to an order.
SELECT o.*, c.email AS customer_email, c.cognito_sub AS customer_cognito_sub
FROM orders o
JOIN customers c ON c.id = o.customer_id
WHERE o.stripe_payment_intent_id = $1;

-- name: ListOrdersByCustomer :many
-- Backs GET /orders. Joins the product name so a list view doesn't need N+1
-- follow-up requests.
SELECT o.*, p.name AS product_name, p.price_in_cents
FROM orders o
JOIN products p ON p.id = o.product_id
WHERE o.customer_id = $1
ORDER BY o.created_at DESC
LIMIT $2 OFFSET $3;

-- name: CreateOrder :one
-- total_in_cents snapshots the product price at reserve time so a later price
-- change can't move the amount charged. expires_at bounds how long the
-- reservation may hold stock.
INSERT INTO orders (customer_id, product_id, total_in_cents, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: MarkOrderAwaitingPayment :one
-- Checkout's half: records the PaymentIntent and hands the order to Stripe.
-- Guarded on 'pending' so two concurrent checkouts can't attach two intents
-- to one order — the loser sees no rows and is rejected.
UPDATE orders
SET status = 'awaiting_payment', stripe_payment_intent_id = $2, expires_at = $3
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: CompleteOrder :one
-- Guarded by status = 'awaiting_payment' so a redelivered webhook can only
-- ever commit stock once for a given order. Stripe delivers at-least-once;
-- this WHERE clause is what makes that safe without a distributed lock.
--
-- Note the guard is 'awaiting_payment', not 'pending': an order can only be
-- completed by a confirmed payment, never straight from reservation.
UPDATE orders
SET status = 'completed'
WHERE id = $1 AND status = 'awaiting_payment'
RETURNING *;

-- name: CancelPendingOrder :one
-- Backs DELETE /orders/{id}. Restricted to 'pending' on purpose: once an
-- order is 'awaiting_payment' a PaymentIntent exists and the customer may
-- still pay it, so cancelling would need a refund path. Those orders are left
-- to expire instead.
UPDATE orders
SET status = 'cancelled'
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: FailOrder :one
-- The payment_intent.payment_failed path. Guarded so a redelivered failure
-- releases stock at most once.
UPDATE orders
SET status = 'failed'
WHERE id = $1 AND status = 'awaiting_payment'
RETURNING *;

-- name: ExpireOrders :many
-- The sweeper's claim-and-mark. Driven by our own clock and our own table,
-- deliberately independent of Stripe: if Stripe never calls back, stock must
-- still come back on sale.
--
-- FOR UPDATE SKIP LOCKED makes this safe to run from more than one process —
-- a second sweeper skips rows already claimed rather than blocking on them.
-- The caller must ReleaseStock for every returned row in this same
-- transaction, so an order can never be marked expired without its unit
-- being returned.
UPDATE orders
SET status = 'expired'
WHERE id IN (
    SELECT id FROM orders
    WHERE status IN ('pending', 'awaiting_payment')
      AND expires_at IS NOT NULL
      AND expires_at < now()
    ORDER BY expires_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: InsertStripeEvent :execrows
-- The entry point for both delivery paths — the webhook handler and the event
-- poller. id is Stripe's own evt_... identifier, so ON CONFLICT DO NOTHING
-- turns at-least-once delivery into exactly-once processing, and makes it safe
-- to run both paths at the same time or to re-scan an overlapping window.
-- Returns 0 rows when the event was already recorded, which callers log as a
-- duplicate rather than an error.
INSERT INTO stripe_events (id, type, payload, stripe_created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO NOTHING;

-- name: LatestStripeEventCreatedAt :one
-- The poller's resume point, in Stripe's clock.
--
-- Reading it from the table on every tick rather than holding it in memory is
-- what makes the poller crash-safe and stateless: after a restart, a deploy,
-- or an outage of any length it resumes from the last event actually stored.
-- Returns NULL when nothing has been recorded yet, which the caller turns into
-- a bounded initial lookback so a fresh box doesn't replay all of history.
SELECT max(stripe_created_at)::timestamptz FROM stripe_events;

-- name: ClaimNextStripeEvent :one
-- One event per transaction: a poisoned payload then fails only itself
-- instead of rolling back a whole batch. SKIP LOCKED lets concurrent workers
-- (or a future second box) drain the queue without double-processing.
--
-- The attempts cap is what stops an event that can never succeed from being
-- re-claimed every poll forever. Such rows stay unprocessed with last_error
-- set, which is the signal to go look at them.
SELECT * FROM stripe_events
WHERE processed_at IS NULL AND attempts < sqlc.arg(max_attempts)
ORDER BY received_at
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: MarkStripeEventProcessed :exec
-- last_error is NULL for a clean success. It is set — with processed_at still
-- stamped — for an event that can never succeed no matter how often it is
-- retried, such as a payment that arrived for an order the sweeper already
-- expired. Retrying those forever would spin the worker; leaving the message
-- behind is what makes them findable.
UPDATE stripe_events
SET processed_at = now(), attempts = attempts + 1, last_error = $2
WHERE id = $1;

-- name: MarkStripeEventFailed :exec
-- Leaves processed_at NULL so the event is retried on the next poll.
UPDATE stripe_events
SET attempts = attempts + 1, last_error = $2
WHERE id = $1;
