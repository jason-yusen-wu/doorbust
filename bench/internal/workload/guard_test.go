package workload

import (
	"errors"
	"strings"
	"testing"
)

// bench destroys whatever it is pointed at, and the honesty run points it at a
// remote database sitting in the same managed project as production. These
// tests are the difference between a benchmark and an outage.
func TestCheckTargetRefusesNonBenchDatabases(t *testing.T) {
	t.Parallel()

	for _, dsn := range []string{
		"postgresql://user:pw@ep-something.us-east-2.aws.neon.tech/neondb",
		"postgres://user:pw@localhost:5432/doorbust",
		"postgres://user:pw@localhost:5432/production",
		// The test database is a particularly easy mistake: it is a DSN the
		// developer already has exported in their shell.
		"postgres://postgres:postgres@localhost:55432/doorbust_test",
	} {
		if err := CheckTarget(dsn, false); err == nil {
			t.Errorf("accepted %q — bench would have truncated it", dsn)
		}
	}
}

func TestCheckTargetAllowsBenchDatabases(t *testing.T) {
	t.Parallel()

	for _, dsn := range []string{
		"postgres://postgres:postgres@localhost:55433/doorbust_bench?sslmode=disable",
		"postgresql://user:pw@ep-something.us-east-2.aws.neon.tech/doorbust_bench",
		"postgres://u:p@host/BENCH_scratch",
	} {
		if err := CheckTarget(dsn, false); err != nil {
			t.Errorf("rejected %q: %v", dsn, err)
		}
	}
}

func TestOverrideIsHonoured(t *testing.T) {
	t.Parallel()

	if err := CheckTarget("postgres://u:p@host/neondb", true); err != nil {
		t.Errorf("explicit override still refused: %v", err)
	}
}

// A password must never reach the error output. An operator pasting a bench
// failure into a chat should not be leaking a credential by doing so.
func TestErrorsNeverEchoTheDSN(t *testing.T) {
	t.Parallel()

	const secret = "sup3rs3cr3t"
	dsn := "postgresql://user:" + secret + "@host.example/neondb"

	err := CheckTarget(dsn, false)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error leaked the password: %s", err)
	}

	var unsafe *ErrUnsafeTarget
	if !errors.As(err, &unsafe) {
		t.Fatalf("got %T, want *ErrUnsafeTarget", err)
	}
	if unsafe.Database != "neondb" {
		t.Errorf("database = %q, want neondb", unsafe.Database)
	}

	// A malformed DSN must not fall back to printing what it could not parse.
	bad := CheckTarget("://:"+secret+"@@@", false)
	if bad != nil && strings.Contains(bad.Error(), secret) {
		t.Errorf("the parse error leaked the password: %s", bad)
	}
}
