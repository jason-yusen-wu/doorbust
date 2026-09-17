# Doorbust

A flash-sale storefront and API, written in Go. It exists to answer one question
carefully: **when far more people want an item than there are items, how do you
make sure you never sell the same unit twice?**

Selling 360 units to 36,000 simultaneous buyers is easy to get almost right. The
failure mode is quiet — two requests both check stock, both see one left, both
succeed, and you find out when a customer emails about an order you can't ship.

Doorbust solves that with one small idea, applied consistently, and then
measures the result instead of asserting it.

https://github.com/user-attachments/assets/95b81884-d77a-4e98-9e52-40f7f5c0840e


---

## Contents

- [The problem](#the-problem)
- [The solution](#the-solution)
- [How fast is it?](#how-fast-is-it)
- [Running it locally](#running-it-locally)
- [How the code is organised](#how-the-code-is-organised)
- [How payment works](#how-payment-works)
- [API reference](#api-reference)
- [Testing](#testing)
- [Benchmarking](#benchmarking)
- [Deployment](#deployment)
- [Known limitations](#known-limitations)

---

## The problem

Imagine two buyers clicking "Reserve" at the same instant, on the last unit:

```
Buyer A                          Buyer B
───────                          ───────
read stock  → 1 left
                                 read stock  → 1 left
write stock → 0
                                 write stock → 0
✓ order created                  ✓ order created     ← one unit, two orders
```

Both did exactly what the code told them to. The gap between reading the stock
level and acting on it is where the bug lives, and it widens under load — which
is precisely when a flash sale happens.

The usual fixes are to lock the row, wrap everything in a serialisable
transaction, or put a counter in Redis in front of the database. All three work.
All three add something you have to operate, reason about, or keep in sync.

## The solution

**Never read stock and then write it. Make the check part of the write.**

```sql
UPDATE stock
   SET num_reserved = num_reserved + 1
 WHERE product_id = $1
   AND quantity - num_reserved > 0   -- the check, inside the write
RETURNING *;
```

A single SQL statement is atomic. There is no gap for a competitor to slip
into: either the row matched and you reserved a unit, or it didn't and someone
beat you to it. **Zero rows affected is the answer, not an error** — it means
"sold out".

That one idea does three jobs across the codebase:

| Where | What it prevents |
| --- | --- |
| Reserving stock | Overselling under any amount of concurrency |
| Handling Stripe webhooks | Double-crediting an order when Stripe delivers the same event twice (it will — delivery is at-least-once) |
| Releasing a reservation | Driving the reserved count negative when a cancel and an expiry race |

Every order state change uses the same shape: a condition in the `WHERE` clause,
and "no rows matched" meaning *someone else got there first*.

As a backstop, the database itself enforces `num_reserved >= 0 AND
num_reserved <= quantity`. If a bug ever did try to oversell, the write would
fail loudly rather than corrupt the inventory.

## How fast is it?

The interesting part is not the raw number, it's **what determines it**.

Reserving stock originally took six round trips to the database inside one
transaction, and the row stayed locked for the last two of them. While locked,
every other buyer for that item waits. So the ceiling is roughly:

```
                              1
  ───────────────────────────────────────-----------------
  (round trips while locked × network RTT) + database work
```

The lever is not the locking strategy — it's **how much network time you hold
the lock across**. Three versions were built and measured, identical in
behaviour, differing only in that:

| version | order of operations | round trips while locked |
| --- | --- | --- |
| `baseline` | reserve → create order → commit | 2 |
| `reserve-last` | create order → reserve → commit | 1 |
| `cte` | one single SQL statement | **0** |

### On a laptop, everything local

At a fixed 2,000 reservations/second offered:

| version | kept up? | median response |
| --- | --- | --- |
| `baseline` | no — 1,613/s | 2.17 **seconds** |
| `reserve-last` | yes | 1,249 µs |
| `cte` | yes | **376 µs** |

Maximum sustained rate: 1,613/s → 2,487/s → **at least 5,302/s**.

### On the real deployment

A `t3.micro` running the app, with the database on [Neon](https://neon.tech) —
a real network between the two. At 500 reservations/second offered:

| version | kept up? | median response |
| --- | --- | --- |
| `baseline` | **no — 7,989 of 8,000 requests timed out** | — |
| `reserve-last` | 457.7/s | 1.39 seconds |
| `cte` | **yes, 500/s** | **2,845 µs** |

**The gap is wider in production than on a laptop, not narrower.** The lock is
held for a number of *network round trips*, and a real one (measured: 642 µs)
costs about six times a local one. Removing them is worth six times as much.

Two honest notes:

- The laptop numbers compare the three versions **against each other on one
  machine**. They are not a capacity figure for your hardware.
- We never found the ceiling of the fastest version on the deployed box. Above
  ~500/s the load generator — sharing two CPUs with the app — became the
  bottleneck first.

### The correctness result

The point of all this is that being fast must not mean being wrong:

```
36,000 requests · 360 units in stock · one item · no warm-up

  360     orders created
  35,640  "sold out" responses
  0       server errors
  0       oversold units
```

Every figure on this page comes from a JSON file in [`bench/results/`](bench/results/),
committed alongside the code that produced it.

### Did this need Redis?

No — and that's a measured answer, not a preference. The original plan was to
put a Redis counter in front of the database. Rewriting the reservation as a
single SQL statement removed five network round trips and made everything at
least three times faster, with no new infrastructure to run and no second copy
of the inventory to keep in sync.

## Running it locally

**You'll need:** Go 1.26+, Node 22+, Docker, `goose`, and a PostgreSQL database.

```bash
git clone <this repo> && cd doorbust
cp .env.example .env     # then fill in the values below
make server              # applies migrations, starts the API on :8080
```

`.env` needs five values. The app refuses to start without them, rather than
failing later on the first request that needs one:

| Variable | What it is |
| --- | --- |
| `GOOSE_DBSTRING` | PostgreSQL connection string |
| `COGNITO_ISSUER_URL` | Your AWS Cognito user pool's issuer URL |
| `COGNITO_CLIENT_ID` | The app client ID |
| `STRIPE_SECRET_KEY` | A Stripe **test mode** key |
| `STRIPE_WEBHOOK_SECRET` | Only used for local webhook testing — any placeholder works otherwise |

Everything else has a sensible default; `.env.example` documents the rest.

For the storefront:

```bash
cd web
npm ci
npm run build            # then make server serves it at localhost:8080
```

Or run it separately with live reload — `npm run dev` on `:5173`, plus
`CORS_ALLOWED_ORIGINS=http://localhost:5173` in the API's `.env`.

> **Note:** sign-in only works on `localhost`. Cognito refuses to register a
> callback URL that isn't HTTPS, and the deployed box has no TLS certificate.
> See [Known limitations](#known-limitations).

## How the code is organised

A request flows in one direction: **HTTP handler → service → database**. Each
feature is a package containing all three, and they're wired together in one
place ([`cmd/api.go`](cmd/api.go)), so there's a single file to read to see how
the app is assembled.

```
cmd/                    Entry point, routing, startup and shutdown
internal/
  orders/               Reserving, paying for and cancelling orders
  payments/             Stripe: the gateway, webhook, and event processor
  products/             The catalogue
  customers/            Mapping a Cognito login to a customer record
  auth/                 Verifying Cognito tokens
  ratelimit/            Per-IP and per-user request limits
  web/                  Serving the built storefront
  adapters/postgresql/  Connection pool and generated SQL
web/                    Storefront (React + TypeScript + Tailwind)
bench/                  Load generator
```

Handlers depend on an interface, not a concrete database type, so the business
logic can be tested without a database when a database wouldn't add anything.

Queries are written by hand in [`queries.sql`](internal/adapters/postgresql/sqlc/queries.sql)
and turned into typed Go by [sqlc](https://sqlc.dev). **Don't edit the generated
files** — change the SQL and run `sqlc generate`. CI fails if the two drift
apart.

## How payment works

Taking payment is split into two halves, and the split is the interesting part.

```
1. POST /orders                 Reserve a unit.                  → pending
2. POST /orders/{id}/checkout   Create a Stripe payment intent.  → awaiting_payment
3. (Stripe processes payment)
4. Event from Stripe            Confirm and finalise the sale.   → completed
```

**Why split it?** Step 2 calls Stripe over the internet, which can take
hundreds of milliseconds. If that call happened while holding the inventory
lock, *every other buyer of that item would wait for Stripe* — turning one
person's slow payment into everyone's outage.

So the lock is taken and released in step 1, and Stripe is called afterwards.
Payment confirmation arrives later and is handled asynchronously.

Three background tasks run inside the process:

- **Event worker** — processes payment confirmations from a queue table.
- **Reservation sweeper** — releases reservations that were never paid for.
  Deliberately independent of Stripe: it's *our* timeout on *our* clock, so
  stock goes back on sale even if the payment processor never responds.
- **Event poller** — fetches events from Stripe's API (see
  [Known limitations](#known-limitations) for why it pulls rather than
  receiving webhooks).

**One case is handled loudly rather than silently:** if a payment succeeds for
an order the sweeper already expired, money moved but no stock is owed. Doorbust
logs it at ERROR for a human to refund, rather than retrying forever or
pretending it didn't happen.

## API reference

| Method | Path | Who can call it |
| --- | --- | --- |
| `GET` | `/health`, `/health/ready` | Anyone |
| `GET` | `/products`, `/products/{id}` | Anyone |
| `GET` | `/me` | Signed-in users |
| `GET` | `/orders`, `/orders/{id}` | The order's owner |
| `POST` | `/orders` | Signed-in users |
| `POST` | `/orders/{id}/checkout` | The order's owner |
| `DELETE` | `/orders/{id}` | The order's owner |
| `POST` | `/products` | Users in the `vendors` group |
| `POST` | `/webhooks/stripe` | Stripe (signature-verified) |

Errors always come back in the same shape, and the `code` is stable enough to
branch on:

```json
{ "error": { "code": "out_of_stock", "message": "product is out of stock" } }
```

**Retrying a reservation safely.** `POST /orders` accepts an `Idempotency-Key`
header. Send the same key when retrying and you'll get the original order back
instead of reserving a second unit. Without a key, a retry reserves again — so
send one.

`/health` versus `/health/ready`: the first says the process is alive; the
second checks the database and the Stripe poller and returns 503 if either is
broken. Deploys watch the second one.

## Testing

```bash
make test        # fast — no database needed, a few seconds
make test-db     # start a throwaway PostgreSQL in Docker
make test-all    # everything, including database and HTTP tests
make cover       # enforce per-package coverage minimums
```

230 tests. A few deliberate choices worth knowing about:

- **Every database test gets its own database**, cloned from a migrated
  template. That's what lets them run in parallel and assert exact counts.
- **Tests that need no database skip themselves**, so `go test ./...` passes on
  a fresh clone with nothing set up.
- **Authentication is tested against the real verifier**, using a local server
  that issues genuine signed tokens. Expired, forged, and wrong-audience tokens
  are actually rejected, not stubbed.
- **Stripe is faked at the HTTP layer**, so tests check the real request —
  including the amount. Faking the interface instead would happily accept code
  that charged the wrong price.
- **Routes fail closed.** A test walks the router and fails if any route hasn't
  declared who may call it. Forgetting is not an option; it's how an
  accidentally-public endpoint was once caught.
- **Concurrency tests run against all three reservation strategies.** A faster
  version that oversells is a bug, not an improvement.

## Benchmarking

```bash
make bench-db      # a PostgreSQL tuned so the comparison is meaningful
make bench-sweep   # run every version and compare
```

The load generator is **open-loop**: it decides when each request should be sent
before the run starts, and measures from that scheduled time.

This matters more than it sounds. The obvious way to write a load tester — a
pool of workers that each send a request, wait for the reply, and send another —
*cannot send faster than the server replies*. When the server slows down, the
test slows down with it, and the queue that would form in real life never does.
You measure how long the server took, and never see how long users waited.
(It's called coordinated omission.)

**Runs judge themselves.** A run reports `RUN VOID` and exits non-zero if the
generator couldn't keep to its schedule, if it ran out of sockets, if any
request returned a server error, or if the inventory doesn't add up afterwards.
It also refuses to record results from uncommitted code — a number you can't tie
to a commit isn't worth keeping.

It also grades its own precision. A late request inflates one measurement, so
the proportion of late requests bounds which percentiles are trustworthy; rather
than print a figure it can't stand behind, it says
`p99.9 (untrusted: 1.2% late sends)`.

**Every run checks the inventory afterwards** — orders never exceed stock,
reservations match live orders, nothing went negative. That's what makes the
benchmark a correctness test under load rather than a speed test.

### Profiling

Set `PPROF_ADDR=127.0.0.1:6060` to enable Go's profiler. It listens on
localhost only — always, regardless of what you configure — because those
endpoints will dump memory and stack traces to anyone who can reach them. On a
deployed box, tunnel to it:

```bash
aws ssm start-session --target <instance-id> \
  --document-name AWS-StartPortForwardingSession \
  --parameters '{"portNumber":["6060"],"localPortNumber":["6060"]}'
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
```

## Deployment

Push to `main`. GitHub Actions runs the Go build and tests, the frontend build
and tests, and a check that the generated SQL matches its source — then builds a
container image, tags it with the commit, pushes it to ECR, migrates the
database, and restarts the service.

Two details worth copying:

- **No SSH keys or AWS credentials are stored in GitHub.** The runner
  authenticates with OIDC, and deploys are driven through AWS Systems Manager,
  which connects outward from the instance. No inbound port is opened.
- **Database migrations run before the restart.** Keep them
  backwards-compatible, since the old code briefly runs against the new schema.

Infrastructure is one EC2 instance, defined in [`infra/`](infra/) with
Terraform, applied by hand. There's no load balancer and no container
orchestrator — for a single instance they'd add cost and moving parts without
adding anything.

## Known limitations

Written down rather than glossed over. Most of them share one root cause: the
deployed box has no domain name and no TLS certificate.

- **You can't sign in on the deployed site.** Cognito won't register a
  non-HTTPS callback URL (except on `localhost`), so the public site can browse
  the catalogue but not log in. Fixing it means buying a domain and terminating
  TLS — a deliberate, separate decision.
- **Stripe events are fetched, not received.** Stripe can't reach the box for
  the same reason, so the app polls Stripe's API instead. This turns out to be
  more robust anyway: it catches up automatically after a restart or an outage,
  where a missed webhook is simply lost.
- **The benchmark can't fully saturate the deployed box.** Two CPUs aren't
  enough to run both the load generator and a busy server; the generator gives
  out first. The fastest version's true ceiling there is unknown.
- **Authorisation is coarse.** One Cognito group controls who can create
  products. Everything else is open to any signed-in user.
- **Rate limiting is off by default**, so upgrading doesn't suddenly start
  rejecting traffic. Enable it with `RATE_LIMIT_PER_IP_RPS` and
  `RATE_LIMIT_PER_SUBJECT_RPS`.
- **One environment.** Post-deploy checks run against production. A staging
  environment is the textbook answer; for a single-developer project it isn't
  worth the cost.

## Built with

Go 1.26 · [chi](https://github.com/go-chi/chi) · PostgreSQL 17 ·
[pgx](https://github.com/jackc/pgx) · [sqlc](https://sqlc.dev) ·
[goose](https://github.com/pressly/goose) · [Neon](https://neon.tech) ·
Stripe · AWS Cognito · React · TypeScript · Tailwind · Vite · Docker ·
Terraform · GitHub Actions

---

Design decisions, things tried and rejected, and the reasoning behind the
trade-offs above are recorded in [`CLAUDE.md`](CLAUDE.md).
