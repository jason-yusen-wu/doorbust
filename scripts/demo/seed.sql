-- Demo catalogue for filming.
--
-- Self-limiting: it only ever touches the eight products named in demo_catalogue
-- below. It will not delete anything you created by hand, and it does not
-- truncate. Re-run it between takes to reset the catalogue.
--
-- The names are the identifier on purpose. An earlier version tagged each row
-- with a "[demo]" marker so it could find them again, which worked but put the
-- word "demo" on screen in every shot.
--
-- The catalogue puts every state the storefront can render on screen at once:
--
--   healthy stock          the ordinary case
--   nearly gone            the stock bar reads as urgent
--   exactly one unit       the oversell race, filmed live
--   sold out              sorts last, renders unavailable
--   upcoming              a future start_at, listed separately from live sales

BEGIN;

CREATE TEMP TABLE demo_catalogue (name, cents, starts_in, qty) ON COMMIT DROP AS
SELECT * FROM (VALUES
    -- Live now. A start_at in the past is the only thing separating a live
    -- sale from an upcoming one.
    ('Aether Running Shoe — Midnight', 12900, -interval '2 hours',     250),
    ('Lumen 27" 4K Monitor',           34900, -interval '90 minutes',   40),
    ('Nomad Weekender Duffel',          8900, -interval '1 hour',       12),
    ('Vertex Mechanical Keyboard',     15900, -interval '45 minutes',    3),

    -- The race: one unit, two buyers, filmed live.
    ('Solstice Limited Print — 1 of 1', 4500, -interval '10 minutes',    1),

    -- Already gone. Sorts last and renders as sold out.
    ('Halo Wireless Earbuds',           7900, -interval '3 hours',       0),

    -- Upcoming. Listed under its own heading, not yet reservable.
    ('Cirrus Down Jacket',             19900,  interval '2 hours',     100),
    ('Terra Cast Iron Set',            11900,  interval '1 day',        60)
) AS v;

-- Clear the previous run, innermost first.
--
-- orders.product_id references products WITHOUT a cascade, and that is correct
-- for the application: deleting a product must never silently erase the record
-- of what people bought. It does mean a reseed has to remove its own orders
-- first — which is the normal case here, since you reseed BETWEEN TAKES, when
-- the last take's orders exist. Skipping it is a foreign-key error at exactly
-- the wrong moment.
--
-- order_idempotency cascades from orders, and stock cascades from products, so
-- neither needs a statement of its own.
DELETE FROM orders
WHERE product_id IN (SELECT id FROM products WHERE name IN (SELECT name FROM demo_catalogue));

DELETE FROM products WHERE name IN (SELECT name FROM demo_catalogue);

WITH seeded AS (
    INSERT INTO products (name, price_in_cents, start_at)
    SELECT name, cents, now() + starts_in FROM demo_catalogue
    RETURNING id, name
)
INSERT INTO stock (product_id, quantity, num_reserved)
SELECT seeded.id, demo_catalogue.qty, 0
FROM seeded JOIN demo_catalogue USING (name);

COMMIT;

-- What the storefront will show, in the order it will show it.
SELECT
    CASE WHEN p.start_at <= now() THEN 'live' ELSE 'upcoming' END AS state,
    p.name,
    '$' || to_char(p.price_in_cents / 100.0, 'FM999990.00') AS price,
    s.quantity - s.num_reserved AS available
FROM products p
JOIN stock s ON s.product_id = p.id
ORDER BY (s.quantity - s.num_reserved <= 0), p.start_at;
