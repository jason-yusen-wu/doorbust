-- Durable inbox for Stripe webhooks. The HTTP handler verifies the signature,
-- writes the raw event here, and returns 200 immediately; a background worker
-- applies the effect. Two reasons for the split:
--   1. Stripe retries on a slow or failed response. Doing the DB work inline
--      means a slow checkout path can turn one payment into several delivery
--      attempts.
--   2. The effect (complete order + commit stock) then survives a crash: an
--      unprocessed row is simply picked up again on restart.
-- id is Stripe's own evt_... identifier, so it doubles as the dedup key —
-- at-least-once delivery becomes exactly-once processing via ON CONFLICT.

-- +goose Up
CREATE TABLE IF NOT EXISTS stripe_events (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    payload      JSONB NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT
);

-- Partial: the worker only ever polls for unprocessed rows, so processed
-- history (which grows without bound) never enters the index.
CREATE INDEX IF NOT EXISTS idx_stripe_events_unprocessed ON stripe_events (received_at)
    WHERE processed_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_stripe_events_unprocessed;
DROP TABLE IF EXISTS stripe_events;
