# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Code Style
Use clean layered architecture where possible. 

## Project

Doorbust is a Go eCommerce backend targeting oversell-free, idempotent inventory guarantees under high contention (flash-sale / "doorbuster" style load). `/products` (browse + create) and `/orders` (reserve → checkout, two-phase) are implemented and Cognito-authenticated. The oversell-free guarantee currently rests on two SQL-level guards — `ReserveStock`'s `quantity - num_reserved > 0` check and `CompleteOrder`'s `status = 'pending'` check — and has **no automated test coverage**; see Notes.

Tech stack: Go 1.26, chi v5 router, PostgreSQL via pgx/v5 + sqlc (hosted on [Neon](https://neon.tech)), goose migrations, Redis (planned, not yet wired in), Amazon Cognito for auth, Docker + Terraform for deployment.

## Commands

- `make server` — runs goose migrations (`goose up`) against Neon, then runs the API (`go run ./cmd`), both via `scripts/with-env.sh` (see below). This is the standard way to run the app locally. There is no local Postgres container — `.env` must point at a real (Neon) database.
- `make clean` — tears down: `scripts/with-env.sh goose down`.
- Build/vet/test directly with the Go toolchain, e.g. `go build ./...`, `go vet ./...`, `go test ./...` (no tests exist yet).
- Requires `goose` CLI installed locally and a `.env` (copy `.env.example`) with a live Postgres connection string plus `COGNITO_ISSUER_URL`/`COGNITO_CLIENT_ID`.
- `scripts/with-env.sh <cmd>` — reads `.env` line-by-line (`KEY=VALUE`, **no quoting** — see below) and execs `<cmd>` with those vars exported. Used instead of shell-`source`ing `.env` directly, because the Neon DSN contains a literal `&` (from `?sslmode=require&channel_binding=require`), which a shell interprets as "run in background" — `with-env.sh` reads each line via `read` instead of evaluating it, so DSN special characters are inert. This is also why `.env` values are unquoted: that format is what `docker run --env-file .env` expects too (Docker doesn't strip quotes the way a shell would), so one `.env` format now works for both consumers.
- `docker build -t doorbust:latest .` / `docker run --env-file .env -p 8080:8080 doorbust:latest` — build/run the container locally. `Dockerfile` is a multi-stage Alpine build; the final stage needs `ca-certificates` since the app makes outbound TLS calls to both Neon and Cognito's JWKS endpoint, and runs as unprivileged uid 10001 (`USER doorbust`), not root.
- **Deploying**: `terraform -chdir=infra apply` provisions a single EC2 instance (no ALB, no ECS — see `infra/` below), then `./scripts/deploy.sh` builds the image and ships it via `docker save | ssh | docker load` (no ECR for this box). This is a start-for-a-benchmark-session, stop-when-done box, not an always-on deployment — `terraform -chdir=infra destroy` (or stop the instance) between sessions so it isn't billing while idle. `deploy.sh` builds with `--platform linux/amd64` explicitly — the EC2 AMI is x86_64, and `docker build` otherwise defaults to the host machine's architecture (an arm64 image silently `exec format error`s on the amd64 instance instead of failing at build time).
- **Testing a protected endpoint by hand**: `COGNITO_CLIENT_SECRET='...' ./scripts/with-env.sh ./scripts/get-test-token.sh <username> <password>` signs in as a Cognito user via `USER_PASSWORD_AUTH` and prints an ID token to `curl -H "Authorization: Bearer $TOKEN" ...` with. It takes the pool/client identifiers from `.env` (hence running it through `with-env.sh`) rather than hardcoding them, and reads the app client's secret only from a `COGNITO_CLIENT_SECRET` env var you set yourself — never pass it as an argument or hardcode it. `ALLOW_USER_PASSWORD_AUTH` must be enabled on the app client (`aws cognito-idp update-user-pool-client --explicit-auth-flows ...`, additive alongside whatever flows already exist) for this to work.

### Database / sqlc workflow

- Schema migrations live in `internal/adapters/postgresql/migrations/` (goose format: `-- +goose Up` / `-- +goose Down`).
- Hand-written queries live in `internal/adapters/postgresql/sqlc/queries.sql`.
- After editing a migration or `queries.sql`, regenerate the sqlc code with `sqlc generate` (config in `sqlc.yaml`). Never hand-edit the generated files (`db.go`, `models.go`, `querier.go`, `queries.sql.go`) — they're marked `DO NOT EDIT`.
- `GOOSE_DBSTRING`, `GOOSE_DRIVER`, `GOOSE_MIGRATION_DIR` (and the app's `DB_MAX_CONNS`/etc. pool knobs) live in `.env` (gitignored, not committed; see `.env.example` for the required keys). The Makefile loads `.env` via `scripts/with-env.sh` for both `goose` and `go run` — neither goose nor the app loads it on their own, and `cmd/main.go` panics via `env.MustGetString` if `GOOSE_DBSTRING` isn't set, rather than falling back to a hardcoded DSN.
- Prefer Neon's **direct** (non-`-pooler`) connection string over the PgBouncer-pooled one — the app already pools client-side via `pgxpool`, and pgx's prepared-statement caching doesn't play well with PgBouncer's transaction-mode pooling.

## Architecture

The app follows a small ports/adapters style layering, one package per domain feature:

- `cmd/main.go` — process entrypoint. Builds `config`, opens a `*pgxpool.Pool` via `postgresql.NewPool`, constructs the `application` struct, and starts the server.
- `cmd/api.go` — defines `application` (holds `config` + the pool), `mount()` which builds the chi router, wires middleware, and connects each feature's handler/service, and `run()` which starts `http.Server`. This is the composition root — new features get wired into `mount()` here.
- `internal/adapters/postgresql/` — `pool.go` owns the connection adapter: `postgresql.Config` (DSN + pool sizing) and `NewPool`, which parses the DSN, applies the pool knobs, and pings once so a bad DSN fails at startup. Pool sizing is read from env in `main.go` (`DB_MAX_CONNS`, `DB_MIN_CONNS`, `DB_MIN_IDLE_CONNS`, `DB_MAX_CONN_LIFETIME`, `DB_MAX_CONN_IDLE_TIME`).
- `internal/adapters/postgresql/sqlc/` — sqlc-generated DB layer (`Queries` struct implementing a `Querier` interface) plus the source `queries.sql`. Feature services depend on the `Querier` interface, not the concrete `Queries` type, so they can be tested/mocked independently of Postgres. `tx.go` (hand-written) defines the `Beginner` interface used for transactions; `*pgxpool.Pool` satisfies both it and the generated `DBTX`.
- `internal/<feature>/` (e.g. `internal/products/`) — each feature package has:
  - `service.go` — business logic behind a `Service` interface, implemented by an unexported struct wrapping a `repo.Querier`.
  - `handler.go` — HTTP layer behind an unexported `handler` struct wrapping a `Service`; methods are chi-compatible `http.HandlerFunc`s.
  New features should follow this same handler → service → repo.Querier shape and be wired into `mount()` in `cmd/api.go`.
- `internal/env/` — thin env var helpers: `GetString`/`GetInt`/`GetDuration` (each with a fallback), and `MustGetString` (panics if unset — used for `GOOSE_DBSTRING`, `COGNITO_ISSUER_URL`, `COGNITO_CLIENT_ID`, none of which have a safe default).
- `internal/json/` — shared JSON response writer (`json.Write(w, status, data)`) used by handlers instead of ad hoc `encoding/json` calls.
- `internal/auth/` — Cognito ID token verification. doorbust is a **resource server**, not an OAuth2 client: a separate frontend owns the Cognito Hosted UI login/callback and hands doorbust a bearer ID token on each request. `NewVerifier` wraps `coreos/go-oidc`'s `oidc.Provider.Verifier` (discovers the issuer's JWKS at startup, so a bad issuer URL fails fast like `postgresql.NewPool` does). `Middleware` checks `Authorization: Bearer <token>` and puts the caller's `Claims{Subject, Email}` on the request context; `FromContext` reads it back in handlers. Wired via a chi route group in `cmd/api.go`'s `mount()` around `POST /products`, `GET /orders/{id}`, `POST /orders`, `POST /orders/{id}/checkout` — `GET /products*` stays public. `internal/orders` uses the caller's email from context (not a client-supplied field) both as the order's owner on `CreateOrder`, and as an ownership check on `GetOrder`/`Checkout` (403 `ErrForbidden` on mismatch, via the customer-joined `FindOrderByID` query). No role/group-based authorization yet — any authenticated Cognito user can hit any protected endpoint.
- `infra/` — Terraform: one EC2 instance (Amazon Linux 2023) in the default VPC, a security group scoped to `var.allowed_cidr` (never `0.0.0.0/0`), a Terraform-generated SSH key (`infra/deploy_key`, gitignored), and an IAM instance role that can only read SSM params under `/doorbust/*`. `COGNITO_ISSUER_URL`/`COGNITO_CLIENT_ID` are managed here as plain SSM `String` params (not secret); the Neon DSN is **not** — put it in SSM yourself as a `SecureString` so it never enters `.tf` files or state (`aws ssm put-parameter --name /doorbust/GOOSE_DBSTRING --type SecureString --value '...'`). `user_data` (`infra/templates/user_data.sh.tpl`) installs Docker and writes a script + the `deploy/doorbust.service` systemd unit that renders `/etc/doorbust.env` from those SSM params before each container start. State is local and gitignored — acceptable for a solo box, revisit with a remote backend if this becomes shared/long-lived.

## Notes

- **Error responses**: handlers log the real error server-side (`log.Println(err)`) and return a static `"internal server error"` body on 500s. Never pass `err.Error()` into a 5xx response — pgx errors embed connection detail (e.g. ``failed to connect to `user=<db-user> database=<db-name>` ``) and would leak it to unauthenticated callers. Returning `err.Error()` for our *own* sentinel errors (`ErrOutOfStock`, `ErrOrderNotPending`, `ErrForbidden`) is fine and intended.
- **No tests exist.** The highest-value gap is the project's central claim: a concurrency test asserting N simultaneous `CreateOrder` calls against M stock never drive `num_reserved` past `quantity`. Nothing currently pins that invariant.
- Graceful shutdown is not yet implemented (`run()` has a `// TODO: graceful shutdown script`). `docker stop` therefore kills in-flight requests, which can distort the tail of a benchmark run.
