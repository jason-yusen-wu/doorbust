# Doorbust 🛒🏃‍♂️

A Go eCommerce backend built around one question: **what does it actually take
to sell 360 units to 36,000 people at once without selling 361?**

Not to answer it with an architecture diagram — to answer it with a number, and
to keep the number in the repository next to the code that produced it.

```
36,000 requests · 360 units in stock · one SKU · zero warm-up

  360     orders created
  35,640  correct "out of stock" answers
  0       5xx
  0       oversold units
  num_reserved = 360, exactly matching the order count
```

That run is `bench/results/*-doorbuster-cte.json`. Every figure below comes from
a committed result file you can re-run.

## The guarantee, and the one idiom that implements it

Inventory is never read-then-written. Every state change is a **single guarded
`UPDATE`** whose `WHERE` clause is the precondition, and "zero rows affected"
means *someone else got there first*:

```sql
UPDATE stock SET num_reserved = num_reserved + 1
WHERE product_id = $1 AND quantity - num_reserved > 0
RETURNING *;
```

One idiom does triple duty:

- **It prevents oversell.** The check and the increment are the same statement,
  so there is no window between them.
- **It makes Stripe's at-least-once webhook delivery idempotent** with no
  distributed lock — a redelivered payment matches zero rows the second time.
- **It stops a double release** driving `num_reserved` negative.

A `CHECK (num_reserved >= 0 AND num_reserved <= quantity)` on `stock` is the
backstop, so an oversell could never be silent: it would fail the write rather
than corrupt the ledger.

## What the measurement found

`CreateOrder` was originally six client round trips in one transaction, with the
contended `stock` row's lock held across the **last two**. The predicted
per-SKU ceiling is therefore roughly `1 / (client round trips in the lock × RTT
+ server execution + commit)`, so the interesting variable is how much client
network sits *inside* the critical section.

Three implementations, identical guarantees, differing only in that:

| arm | statements holding the lock | client RTT in lock |
| --- | --- | --- |
| `baseline` | `ReserveStock` → `CreateOrder` → `COMMIT` | 2 |
| `reserve-last` | `CreateOrder` → `ReserveStock` → `COMMIT` | 1 |
| `cte` | one statement, no explicit transaction | **0** |

**At 2,000 reserves/s offered against a single hot SKU:**

| arm | goodput | p50 latency | p99 latency |
| --- | --- | --- | --- |
| `baseline` | 1,613/s — saturated | **2,173,260 µs** (2.17 s) | 2,944,044 µs |
| `reserve-last` | 2,000/s | 1,249 µs | 2,057 µs |
| `cte` | 2,000/s | **376 µs** | 563 µs |

**Measured per-SKU ceiling:**

| arm | ceiling | vs. baseline | predicted |
| --- | --- | --- | --- |
| `baseline` | 1,613/s | 1.00× | 4,290/s |
| `reserve-last` | 2,487/s | 1.54× | 7,561/s |
| `cte` | **≥ 5,302/s** | **≥ 3.29×** | 10,743/s |

Three things worth saying plainly about that table:

1. **The CTE figure is a lower bound, not a plateau.** At 6,000 rps the
   *generator* degraded before the server did, so the real ceiling was never
   found. A number the measurement cannot support is worth less than an honest
   bound.
2. **The model predicted the ordering, not the magnitude.** It over-predicts
   every absolute ceiling by 2–2.6×, which is the more useful outcome: the lock
   window is not the only serialisation point once the generator, the app and
   Postgres share CPU and every request pays an RSA-2048 token verification.
   Prediction and measurement sit side by side in every result file so the gap
   stays visible.
3. **The headline is the latency column, not the throughput one.** Removing
   client round trips from inside the lock — and changing nothing else about
   the guarantee — moved p50 at a fixed offered rate from 2.17 seconds to 376
   microseconds.

### So: does this need Redis?

**No, and that is a finding rather than an opinion.** The original plan listed a
Redis admission gate as the remedy for contention. Collapsing reserve into one
statement removed five network round trips and bought at least 3.29× for the
price of a CTE — no new infrastructure, no second source of truth, no cache to
invalidate. The box this deploys to would never see the load at which the
question becomes interesting again. Redis is listed under *Future work*, gated
on a number, not under *Tech stack*.

### Reproducing it

```bash
make bench-db        # throwaway Postgres on :55433, tuned so the arms separate
make bench-sweep     # every arm, plus the self-saturation check
```

The generator (`bench/`) is **open-loop**: arrival times are computed before
anything is sent, and latency is measured from a request's *scheduled* arrival
rather than from when it was actually sent. A closed-loop generator cannot offer
load faster than the server answers, so it slows down exactly when the server
does and the queueing never appears — that is coordinated omission, and it is
the difference between a load test and a `for` loop.

Runs judge themselves. Generator saturation, connection errors, schedule lag
over threshold, any 5xx, or failed inventory invariants each print `RUN VOID`
and exit non-zero. `valid`, `tier` and `absolute_throughput_valid` are required
fields of the result schema, so a number cannot be copied out without the
caveat that says what may be claimed from it.

**These are Tier-1 numbers: the generator shares CPU with the app and the
database.** They compare arms against each other on one machine. They are not
absolute throughput, and the result files say so in their own text.

## Architecture

Ports and adapters, one package per feature, wired in `cmd/api.go`'s `mount()`.

```
cmd/                     composition root, router, readiness
internal/orders/         reserve → pay → fulfil, and the three reserve arms
internal/payments/       Stripe gateway, webhook, event worker, event poller
internal/customers/      Cognito identity → customers row
internal/products/       catalogue
internal/auth/           Cognito ID token verification (resource server)
internal/ratelimit/      per-IP and per-subject token buckets
internal/web/            serves the compiled storefront
internal/adapters/       pgxpool + sqlc-generated queries
web/                     Vite + React + TypeScript storefront
bench/                   open-loop load generator
```

### Payment is two halves, deliberately

`POST /orders` reserves stock (`pending`). `POST /orders/{id}/checkout` creates a
Stripe PaymentIntent and returns its `client_secret` (`awaiting_payment`) — it
commits no stock. Payment completes **asynchronously**: `stripe_events` →
in-process worker → `CompleteOrder` + `CommitStock`.

The Stripe call sits *between* two short transactions rather than inside one.
An outbound HTTP call holding a row lock on contended inventory would turn
Stripe's latency into every other buyer's lock latency — the exact pathology
this project exists to avoid.

Three background goroutines run in-process: the **event worker** (drains
`stripe_events` with `FOR UPDATE SKIP LOCKED`), the **reservation sweeper**
(expires reservations and releases their stock), and the **event poller**. The
sweeper is deliberately *not* coupled to the worker: expiry is our rule on our
clock, so stock returns to sale even if the processor never calls back.

**The paid-but-expired race is handled loudly, not silently.** If a payment
succeeds for an order the sweeper already expired, `CompleteOrder` matches zero
rows; the event is stamped processed with `last_error` and logged at ERROR for
manual refund. Retrying cannot fix it, so it must not spin.

## API

| Method | Path | Auth |
| --- | --- | --- |
| `GET` | `/health` · `/health/ready` | public |
| `GET` | `/products` · `/products/{id}` | public |
| `GET` | `/me` | Cognito |
| `GET` | `/orders` · `/orders/{id}` | Cognito, owner only |
| `POST` | `/orders` | Cognito |
| `POST` | `/orders/{id}/checkout` | Cognito, owner only |
| `DELETE` | `/orders/{id}` | Cognito, owner only |
| `POST` | `/products` | Cognito, `vendors` group |
| `POST` | `/webhooks/stripe` | Stripe signature |
| `GET` | `/*` | public (storefront) |

Every non-2xx body is `{"error":{"code":"...","message":"..."}}`. The `code`
values are the stable, machine-readable half of the contract.

`POST /orders` accepts an **`Idempotency-Key`** header. Without one a retry
reserves a second unit; with one it returns the original order. Three outcomes
stay distinct rather than collapsed: the order, `reserve_in_progress` (the first
request is still running — retriable), and `idempotency_key_reused` (the key was
first used with a different body, which is a client bug).

`TestEveryRouteIsClassified` walks the real router and fails if any route is
missing from the access table in `cmd/api_test.go`. A new route with no entry
fails by default — which is how an ungated `POST /products` was caught.

## Quick start

```bash
cp .env.example .env     # Postgres DSN + Cognito issuer/client id + Stripe test keys
make server              # goose up, then the API on :8080
```

Frontend (Node 22): `cd web && npm ci && npm run dev`, with
`CORS_ALLOWED_ORIGINS=http://localhost:5173` on the server. Or `npm run build`
once and let `make server` serve `web/dist` from `:8080` — which is what
production does, at one origin, with no CORS at all.

### Tests

```bash
make test           # unit only, ~4s, no database
make test-db        # throwaway Postgres on :55432
make test-all       # everything, -race -shuffle=on
make cover          # adds the per-package coverage floors
```

**Every database test gets its own database**, cloned from a migrated template
whose name embeds a hash of the migration files. That is what allows
`t.Parallel()` and lets globally-scoped operations — the expiry sweep, the
worker's queue claim — be asserted to an exact count rather than "at least one".

**Auth is tested against the real verifier**, not stubbed past it: a local OIDC
issuer serves discovery and a JWKS and mints RS256 tokens, so expired,
wrong-audience, wrong-issuer and forged tokens are all genuinely rejected. The
load generator mints its tokens from that same issuer, so a benchmark's tokens
take exactly the path the tests cover.

**Stripe is stubbed at the HTTP layer**, not behind an interface, so tests
assert what is actually sent — amount, currency, `Idempotency-Key`,
`metadata[order_id]`. A fake interface would happily accept a checkout that
charged the wrong amount.

The oversell test, the cancel-race test and the 404-vs-409 contract test all run
against **every** reserve arm. An arm that is faster and oversells is a bug, not
a result.

## Deployment

Push to `main`. `.github/workflows/ci-cd.yml` runs `build` (vet, build, `sqlc`
drift), `test` (`-race` against a Postgres service container, plus coverage
floors) and `web` (lint, typecheck, vitest, build) in parallel, then `deploy`
builds a SHA-tagged image, pushes it to ECR, runs `goose up` against Neon, and
drives the rollout over **SSM Run Command** — no SSH key in CI, no inbound port
opened, no long-lived AWS keys (the runner authenticates via GitHub OIDC).

Infrastructure is one EC2 instance in `infra/` (Terraform): no load balancer, no
ECS. Terraform is always applied by hand, since state is local.
`scripts/deploy.sh` ships an image over SSH as an escape hatch when CI is broken.

## Known limitations

Recorded rather than hidden, because each one is a consequence of a deliberate
trade and most of them share a single root cause.

- **Nobody can sign in on the deployed box.** Cognito refuses to register a
  non-HTTPS callback outside the loopback hosts, and the box has no domain, no
  Elastic IP and no TLS — so `http://<ip>:8080/auth/callback` is rejected at
  `create-user-pool-client` time. The deployed storefront browses the catalogue;
  sign-in works on `localhost` only. `/signin` detects this and explains it
  rather than rendering a button whose only outcome is a Cognito error page.
- **Stripe events are pulled, not pushed**, for the same missing TLS: Stripe
  cannot reach the box, so a poller lists `/v1/events` on a timer into the same
  inbox the webhook writes to. This needs no infrastructure and no dashboard
  configuration, and it catches up after an outage by construction.
- **Throughput numbers are Tier-1**, measured with the generator sharing a
  laptop with the app and the database. They compare arms; they are not absolute
  capacity. There is no Tier-2 figure yet.
- **One environment.** Every post-deploy check runs against production. The
  industry answer is a staging environment; for one box and one developer,
  making failures self-correcting is the cheaper substitute — and is not built
  yet either.
- **Authorization is one Cognito group.** `POST /products` requires `vendors`;
  everything else authenticated is open to any signed-in user. Group membership
  rides in the ID token, so granting it takes effect on the next token, not
  immediately.
- **Rate limiting is off by default** (`RATE_LIMIT_PER_IP_RPS`,
  `RATE_LIMIT_PER_SUBJECT_RPS`), so an upgrade never starts rejecting traffic
  unasked.

## Tech stack

Go 1.26 · chi v5 · PostgreSQL 17 (pgx/v5 + sqlc) on [Neon](https://neon.tech) ·
goose · Stripe (test mode) · Amazon Cognito · Docker · Terraform · GitHub
Actions · Vite + React + TypeScript + Tailwind

## Design notes

`CLAUDE.md` carries the full engineering log: why each decision was made, what
was tried and rejected, and the remaining roadmap with its declined items struck
through in place. [System design whiteboard](https://www.tldraw.com/f/UTNkBEuEvJF9yf0wjVOPS?d=v0.0.1660.989.page).
