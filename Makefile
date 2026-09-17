.PHONY: server clean test test-db test-db-stop test-all cover \
	bench-build bench-db bench-db-stop bench bench-sweep

# Local test database. Port 55432 rather than 5432 so it cannot collide with a
# Postgres already running on the machine.
TEST_DB_NAME    := doorbust-test-pg
TEST_DB_PORT    := 55432
TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:$(TEST_DB_PORT)/doorbust_test?sslmode=disable

# Benchmark database. A separate container on a separate port, deliberately:
# a bench run TRUNCATEs every table, and pointing that at the test database
# mid-suite would be an unpleasant way to learn the difference.
BENCH_DB_NAME   := doorbust-bench-pg
BENCH_DB_PORT   := 55433
BENCH_DATABASE_URL ?= postgres://postgres:postgres@localhost:$(BENCH_DB_PORT)/doorbust_bench?sslmode=disable

# Arm, shape and rate for a one-off `make bench`.
ARM   ?= baseline
SHAPE ?= hot-deep
RATE  ?= 500

server:
	./scripts/with-env.sh goose up
	./scripts/with-env.sh go run ./cmd

clean:
	./scripts/with-env.sh goose down

# Unit tests only. Database-backed tests skip themselves when
# TEST_DATABASE_URL is unset, so this needs no Docker and no setup — it is the
# fast inner loop.
#
# The variable is cleared explicitly rather than merely left unset: a developer
# who exports it in their shell would otherwise get the slow suite from the
# target documented as the fast one.
test:
	TEST_DATABASE_URL= go test -race -shuffle=on ./...

# A throwaway Postgres for the database-backed tests. The suite creates and
# drops its own per-test databases from a migrated template, so this container
# needs nothing beyond being empty and reachable.
test-db:
	@docker rm -f $(TEST_DB_NAME) >/dev/null 2>&1 || true
	docker run -d --rm --name $(TEST_DB_NAME) \
		-e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=doorbust_test \
		-p $(TEST_DB_PORT):5432 postgres:17-alpine
	@printf 'waiting for postgres'
	@until docker exec $(TEST_DB_NAME) pg_isready -U postgres >/dev/null 2>&1; do printf '.'; sleep 1; done
	@echo ' ready'

test-db-stop:
	@docker rm -f $(TEST_DB_NAME) >/dev/null 2>&1 || true

# Everything, including the database-backed and HTTP contract tests.
# -shuffle=on catches order dependence between tests; per-test database
# isolation is what makes passing it possible.
test-all:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -shuffle=on -timeout=5m ./...

# Coverage with the per-package floors enforced. -coverpkg is required or the
# HTTP tests in cmd would credit none of their coverage to the packages they
# actually exercise.
#
# Scoped to internal/ and cmd/ rather than ./...: bench/ is a measurement tool
# with no floor, and including it drags the reported total down by tens of
# points while telling you nothing about the code the floors are protecting.
# -count=1 defeats the test cache. A gate that can report a cached result is
# not a gate: an unrelated change would leave it green against stale coverage.
cover:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 -timeout=5m \
		-coverpkg=./internal/...,./cmd/... -coverprofile=coverage.out ./internal/... ./cmd/...
	@./scripts/check-coverage.sh coverage.out

# --- Benchmarks (Track A) -------------------------------------------------
#
# Load testing is out of scope for CI, not for the project: throughput on a few
# shared runner vCPUs with -race serialising everything measures the runner.
# These targets are for a dedicated session on a quiet machine.

bench-build:
	go build -o bin/doorbust ./cmd
	go build -o bin/bench ./bench

# The benchmark database, configured so the arms can actually be told apart.
#
# The stock row's lock is released at COMMIT, and with synchronous_commit=on a
# commit includes a WAL flush. Docker Desktop on macOS puts the data directory
# behind a VM filesystem, where an fsync can cost milliseconds — identical for
# every arm and an order of magnitude larger than the round trips the arms
# differ on. Left on, baseline/reserve-last/cte would land within noise of each
# other and the whole bake-off would produce a null result for a reason that has
# nothing to do with locking.
#
# None of this weakens the oversell guarantee. That lives in row locks and the
# CHECK constraint on stock, not in WAL durability. `bench run -durable` against
# a normally configured database is the control.
bench-db:
	@docker rm -f $(BENCH_DB_NAME) >/dev/null 2>&1 || true
	docker run -d --rm --name $(BENCH_DB_NAME) \
		-e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=doorbust_bench \
		-p $(BENCH_DB_PORT):5432 \
		--tmpfs /var/lib/postgresql/data:rw,size=2g \
		postgres:17-alpine \
		-c synchronous_commit=off -c fsync=off -c max_connections=200
	@printf 'waiting for postgres'
	@until docker exec $(BENCH_DB_NAME) pg_isready -U postgres >/dev/null 2>&1; do printf '.'; sleep 1; done
	@echo ' ready'

bench-db-stop:
	@docker rm -f $(BENCH_DB_NAME) >/dev/null 2>&1 || true

bench: bench-build
	BENCH_DATABASE_URL="$(BENCH_DATABASE_URL)" \
		./bin/bench run -arm=$(ARM) -shape=$(SHAPE) -rate=$(RATE)

# The whole bake-off: every arm at one rate, then the self-saturation check
# that says whether the numbers describe the server or the generator.
bench-sweep: bench-build
	BENCH_DATABASE_URL="$(BENCH_DATABASE_URL)" \
		./bin/bench sweep -shape=$(SHAPE) -rate=$(RATE)
