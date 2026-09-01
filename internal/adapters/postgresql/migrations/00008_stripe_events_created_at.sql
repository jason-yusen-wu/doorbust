-- Records Stripe's own creation timestamp for each event, which is what the
-- event poller resumes from.
--
-- received_at cannot serve as that cursor: it is our insert time, so it moves
-- with our clock and with how long the box was down, not with the order events
-- were actually created in. Resuming from our clock after an outage would skip
-- everything created while we were away.
--
-- Nullable on purpose — rows written before this column existed simply have no
-- cursor value, and max() ignores NULLs. Additive, so the running binary keeps
-- working against this schema during the window before the container restarts.

-- +goose Up
ALTER TABLE stripe_events ADD COLUMN IF NOT EXISTS stripe_created_at TIMESTAMPTZ;

-- The poller reads max(stripe_created_at) on every tick; this keeps that a
-- cheap index scan rather than a growing sequential one.
CREATE INDEX IF NOT EXISTS idx_stripe_events_stripe_created
    ON stripe_events (stripe_created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_stripe_events_stripe_created;
ALTER TABLE stripe_events DROP COLUMN IF EXISTS stripe_created_at;
