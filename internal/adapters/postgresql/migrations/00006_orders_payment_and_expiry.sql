-- Widens orders for real (asynchronous) payment:
--   total_in_cents           snapshots the price at reserve time, so a price
--                            change between reserve and pay can't move the
--                            amount actually charged.
--   expires_at               lets an abandoned reservation be swept and its
--                            stock released. Nullable: rows created by the
--                            old binary have none and are simply never swept.
--   stripe_payment_intent_id the webhook's only handle back to an order.
--                            UNIQUE so one intent can't map to two orders.
-- Adding nullable columns and widening a CHECK are both backward compatible,
-- which the deploy requires: goose runs before systemctl restart, so the old
-- binary serves against this schema for a few seconds.

-- +goose Up
ALTER TABLE orders ADD COLUMN IF NOT EXISTS total_in_cents INTEGER NOT NULL DEFAULT 0 CHECK (total_in_cents >= 0);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS stripe_payment_intent_id TEXT UNIQUE;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('pending', 'awaiting_payment', 'completed', 'failed', 'expired', 'cancelled'));

-- Supports GET /orders (a customer's own orders, newest first).
CREATE INDEX IF NOT EXISTS idx_orders_customer_created ON orders (customer_id, created_at DESC);

-- Partial index for the expiry sweeper: it only ever scans non-terminal
-- orders, so terminal ones never enter the index at all.
CREATE INDEX IF NOT EXISTS idx_orders_expiry_sweep ON orders (expires_at)
    WHERE status IN ('pending', 'awaiting_payment');

-- +goose Down
DROP INDEX IF EXISTS idx_orders_expiry_sweep;
DROP INDEX IF EXISTS idx_orders_customer_created;

-- Any row in a status the old CHECK forbids has to be folded back before the
-- narrower constraint can be re-applied, or this migration fails to reverse.
UPDATE orders SET status = 'completed' WHERE status = 'awaiting_payment';
UPDATE orders SET status = 'pending' WHERE status IN ('failed', 'expired', 'cancelled');

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('pending', 'completed'));

ALTER TABLE orders DROP COLUMN IF EXISTS stripe_payment_intent_id;
ALTER TABLE orders DROP COLUMN IF EXISTS expires_at;
ALTER TABLE orders DROP COLUMN IF EXISTS total_in_cents;
