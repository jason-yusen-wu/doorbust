-- +goose Up
-- Idempotency keys for POST /orders.
--
-- Until now a retried reserve created a SECOND reservation: the endpoint was
-- the one genuinely non-idempotent write in the system, and the frontend worked
-- around it with a ref guard rather than the server being safe to retry. This
-- makes the retry safe, which is what lets a client, a proxy or a load
-- generator retry at all.
--
-- Keyed on the Cognito subject rather than customers.id on purpose. The subject
-- arrives in the verified token, so the key can be claimed BEFORE the customer
-- row is resolved — otherwise every request would pay an extra round trip just
-- to find the id to key on. The subject is also the identity that never
-- changes, which is the same reason owns() prefers it over email.
--
-- order_id is nullable because the row is claimed before the order exists. NULL
-- therefore means "a request holding this key is still in flight", which is a
-- distinct answer from "here is the order it produced" and must not be
-- collapsed into it.
CREATE TABLE IF NOT EXISTS order_idempotency (
    cognito_sub  TEXT NOT NULL,
    idem_key     TEXT NOT NULL,
    -- Hash of the request this key was first used with. A client that reuses a
    -- key for a different product has a bug, and returning the first order
    -- would quietly serve them the wrong product.
    request_hash TEXT NOT NULL,
    order_id     BIGINT REFERENCES orders(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cognito_sub, idem_key)
);

-- Supports pruning old keys by age.
CREATE INDEX IF NOT EXISTS order_idempotency_created_at_idx
    ON order_idempotency (created_at);

-- +goose Down
DROP TABLE IF EXISTS order_idempotency;
