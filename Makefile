.PHONY: server clean test test-db test-db-stop test-all cover

# Local test database. Port 55432 rather than 5432 so it cannot collide with a
# Postgres already running on the machine.
TEST_DB_NAME    := doorbust-test-pg
TEST_DB_PORT    := 55432
TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:$(TEST_DB_PORT)/doorbust_test?sslmode=disable

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

# Coverage with the per-package floors enforced. -coverpkg=./... is required
# or the HTTP tests in cmd would credit none of their coverage to the packages
# they actually exercise.
cover:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -timeout=5m \
		-coverpkg=./... -coverprofile=coverage.out ./...
	@./scripts/check-coverage.sh coverage.out
