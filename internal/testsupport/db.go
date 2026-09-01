// Package testsupport is the shared test harness: isolated databases, a fake
// Cognito issuer, a stubbed Stripe API, and seeding/assertion helpers.
//
// It is a normal package rather than a _test.go file so that tests in any
// package can import it — including cmd, whose router tests are the only way
// to exercise the real middleware chain. Nothing in production imports it.
package testsupport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// The template database is migrated once and then cloned per test.
//
// A dedicated template is used rather than cloning the database
// TEST_DATABASE_URL points at, because CREATE DATABASE ... TEMPLATE fails
// while anything at all is connected to the source. Cloning the shared test
// database would break the moment one test held a pool on it; nothing ever
// connects to this one.
//
// Its name embeds a hash of the migration files, which does three things at
// once: `go test ./...` runs one process per package and they all share a
// single template rather than each rebuilding it; a run after a migration
// change gets a new name and so cannot reuse a stale schema; and the previous
// drop-and-recreate — which raced badly across those concurrent processes —
// is gone entirely.
var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

// templateLockID namespaces the advisory lock that serialises template
// creation across processes. The value is arbitrary but must be stable.
const templateLockID int64 = 0x2b7d_1a55

// DatabaseURL returns the test database DSN, or "" when tests that need a
// database should skip.
func DatabaseURL() string { return os.Getenv("TEST_DATABASE_URL") }

// DB returns a pool onto a freshly created, fully migrated, empty database
// that is dropped when the test finishes.
//
// Isolation is per-test rather than per-package so tests can run in parallel
// and so operations with global scope — the expiry sweeper, the event worker's
// queue claim — can be asserted exactly instead of "at least one".
//
// Skips when TEST_DATABASE_URL is unset, keeping `go test ./...` green on a
// machine with no database.
func DB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	adminDSN := DatabaseURL()
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}

	templateOnce.Do(func() { templateName, templateErr = buildTemplate(adminDSN) })
	if templateErr != nil {
		t.Fatalf("prepare template database: %v", templateErr)
	}

	name := fmt.Sprintf("dbt_%d_%d", time.Now().UnixNano(), rand.IntN(1_000_000))

	admin, err := sql.Open("pgx", maintenanceDSN(adminDSN))
	if err != nil {
		t.Fatalf("open maintenance connection: %v", err)
	}
	defer admin.Close()

	// Also retried: cloning fails while any session is connected to the
	// template, and a concurrent process may still be closing one.
	if err := retry(5*time.Second, func() error {
		_, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %q TEMPLATE %q`, name, templateName))
		return err
	}); err != nil {
		t.Fatalf("create test database %s: %v", name, err)
	}

	pool, err := pgxpool.New(context.Background(), replaceDBName(adminDSN, name))
	if err != nil {
		t.Fatalf("connect to test database %s: %v", name, err)
	}

	t.Cleanup(func() {
		// The pool must be closed first: DROP DATABASE fails while any
		// session is still connected.
		pool.Close()

		cleanup, err := sql.Open("pgx", maintenanceDSN(adminDSN))
		if err != nil {
			t.Logf("could not open maintenance connection to drop %s: %v", name, err)
			return
		}
		defer cleanup.Close()

		if _, err := cleanup.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name)); err != nil {
			t.Logf("could not drop test database %s: %v", name, err)
		}
	})

	return pool
}

// buildTemplate creates and migrates the template database if it does not
// already exist, and returns its name.
//
// Concurrency matters here: `go test ./...` starts a separate process per
// package and they race to do this. An advisory lock serialises them, and the
// migration runs into a uniquely-named staging database that is only renamed
// into place once it has fully applied — so a crash mid-migration can never
// leave a half-built template that later runs would happily clone.
func buildTemplate(adminDSN string) (string, error) {
	hash, err := migrationsHash()
	if err != nil {
		return "", err
	}
	name := "doorbust_tmpl_" + hash

	admin, err := sql.Open("pgx", maintenanceDSN(adminDSN))
	if err != nil {
		return "", fmt.Errorf("open maintenance connection: %w", err)
	}
	defer admin.Close()

	// The lock must be held on one connection for its whole lifetime, so take
	// a dedicated one rather than relying on the pool.
	ctx := context.Background()
	conn, err := admin.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("reserve connection for advisory lock: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", templateLockID); err != nil {
		return "", fmt.Errorf("take template lock: %w", err)
	}
	defer conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", templateLockID)

	var exists bool
	if err := conn.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name,
	).Scan(&exists); err != nil {
		return "", fmt.Errorf("check for template: %w", err)
	}
	if exists {
		return name, nil
	}

	staging := fmt.Sprintf("%s_building_%d", name, os.Getpid())
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, staging)); err != nil {
		return "", fmt.Errorf("clear staging template: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, staging)); err != nil {
		return "", fmt.Errorf("create staging template: %w", err)
	}

	if err := migrate(replaceDBName(adminDSN, staging)); err != nil {
		conn.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, staging))
		return "", err
	}

	// Retry the promotion briefly. ALTER DATABASE ... RENAME requires that no
	// session is connected, and database/sql's Close() returns before the
	// server has necessarily finished tearing the connection down — so the
	// migration's own connection can still be lingering for a few
	// milliseconds. Failing here would fail every test in the process.
	if err := retry(2*time.Second, func() error {
		_, err := conn.ExecContext(ctx, fmt.Sprintf(`ALTER DATABASE %q RENAME TO %q`, staging, name))
		return err
	}); err != nil {
		return "", fmt.Errorf("promote staging template: %w", err)
	}

	return name, nil
}

// retry runs fn until it succeeds or the budget runs out.
func retry(budget time.Duration, fn func() error) error {
	deadline := time.Now().Add(budget)
	delay := 10 * time.Millisecond

	var err error
	for {
		if err = fn(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
}

var (
	gooseOnce sync.Once
	gooseErr  error
)

// SetupGoose configures goose's logger and dialect exactly once per process.
//
// Both are package-level globals in goose, so calling them from more than one
// goroutine is a data race — and -race turns that into a failure of whichever
// tests happen to be running, which reads as an unrelated flake. Any test that
// drives goose directly must call this instead of configuring it itself.
func SetupGoose() error {
	gooseOnce.Do(func() {
		goose.SetLogger(goose.NopLogger())
		gooseErr = goose.SetDialect("postgres")
	})
	return gooseErr
}

func migrate(dsn string) error {
	target, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("connect to template: %w", err)
	}
	defer target.Close()

	// The goose library applies the same files the CLI does, so the schema
	// under test is the schema that ships.
	if err := SetupGoose(); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(target, MigrationsDir()); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	return nil
}

// migrationsHash fingerprints the migration files by name and content, so any
// edit produces a different template rather than silently reusing the old one.
func migrationsHash() (string, error) {
	entries, err := os.ReadDir(MigrationsDir())
	if err != nil {
		return "", fmt.Errorf("read migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	sum := sha256.New()
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(MigrationsDir(), name))
		if err != nil {
			return "", fmt.Errorf("read migration %s: %w", name, err)
		}
		fmt.Fprintf(sum, "%s\n", name)
		sum.Write(content)
	}

	return hex.EncodeToString(sum.Sum(nil))[:12], nil
}

// MigrationsDir resolves the migrations directory from this file's location,
// so it works regardless of which package's tests are running and what the
// working directory happens to be.
func MigrationsDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("testsupport: could not resolve caller for migrations dir")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "adapters", "postgresql", "migrations")
}

// maintenanceDSN points at the "postgres" database. CREATE/DROP DATABASE
// cannot run from inside the database being created or dropped.
func maintenanceDSN(dsn string) string { return replaceDBName(dsn, "postgres") }

// replaceDBName swaps the database name in a postgres:// URL, leaving the
// credentials, host and query parameters alone.
func replaceDBName(dsn, name string) string {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return dsn
	}

	authority, tail, hasPath := strings.Cut(rest, "/")
	if !hasPath {
		return scheme + "://" + authority + "/" + name
	}

	params := ""
	if _, query, hasQuery := strings.Cut(tail, "?"); hasQuery {
		params = "?" + query
	}

	return scheme + "://" + authority + "/" + name + params
}

// Ensure the pgx stdlib driver is linked in for database/sql.
var _ = stdlib.GetDefaultDriver
