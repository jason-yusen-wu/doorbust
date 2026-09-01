-- Bridges Cognito identity to app identity. Until now a customers row was
-- keyed only by email, which Cognito lets a user change; the sub never
-- changes. Nullable so existing rows stay valid and backfill lazily on the
-- owner's next GET /me (expand phase — migrations run before the container
-- restarts, so the old binary must keep working against this schema).

-- +goose Up
ALTER TABLE customers ADD COLUMN IF NOT EXISTS cognito_sub TEXT UNIQUE;

-- +goose Down
ALTER TABLE customers DROP COLUMN IF EXISTS cognito_sub;
