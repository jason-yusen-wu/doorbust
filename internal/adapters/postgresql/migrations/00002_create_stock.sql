-- +goose Up
CREATE TABLE IF NOT EXISTS stock (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT NOT NULL UNIQUE REFERENCES products(id) ON DELETE CASCADE,
    quantity INTEGER NOT NULL CHECK (quantity >= 0),
    num_reserved INTEGER NOT NULL DEFAULT 0 CHECK (num_reserved >= 0 AND num_reserved <= quantity)
);

-- +goose Down
DROP TABLE IF EXISTS stock;
