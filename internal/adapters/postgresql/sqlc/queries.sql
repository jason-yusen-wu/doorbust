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

-- name: FindOrCreateCustomer :one
-- Upsert keyed on the email unique constraint, so concurrent orders from a new
-- customer's first request can't race into duplicate customer rows.
INSERT INTO customers (email)
VALUES ($1)
ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
RETURNING *;

-- name: FindOrderByID :one
SELECT * FROM orders WHERE id = $1;

-- name: CreateOrder :one
INSERT INTO orders (customer_id, product_id)
VALUES ($1, $2)
RETURNING *;

-- name: CompleteOrder :one
-- Guarded by status = 'pending' so a duplicate/concurrent checkout call can
-- only ever commit stock once for a given order.
UPDATE orders
SET status = 'completed'
WHERE id = $1 AND status = 'pending'
RETURNING *;
