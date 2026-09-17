package workload

import (
	"fmt"
	"net/url"
	"strings"
)

// ErrUnsafeTarget is returned when a DSN does not look like a throwaway
// benchmark database.
type ErrUnsafeTarget struct {
	Database string
}

func (e *ErrUnsafeTarget) Error() string {
	return fmt.Sprintf(
		"refusing to run against database %q: bench TRUNCATEs every table, and this "+
			"name does not identify a throwaway benchmark database.\n"+
			"Point -dsn at a database whose name contains %q, or pass -i-know-this-truncates "+
			"if you are certain.",
		e.Database, safeMarker)
}

// safeMarker is the substring a database name must contain before bench will
// destroy its contents.
const safeMarker = "bench"

// CheckTarget refuses to proceed against a database that does not announce
// itself as disposable.
//
// This exists because Reset runs TRUNCATE ... RESTART IDENTITY CASCADE across
// every table, and the honesty run points bench at a REMOTE database living in
// the same managed project as production. One mistyped DSN, or one stale
// environment variable inherited from a shell, is the difference between a
// benchmark and an outage — and the error would be silent, because truncating
// a production database is a perfectly valid thing for Postgres to do.
//
// A name check is deliberately crude. Anything cleverer (matching hostnames,
// detecting managed providers) would fail open on the case it cannot classify,
// and the whole point is to fail closed.
func CheckTarget(dsn string, override bool) error {
	if override {
		return nil
	}

	name, err := databaseName(dsn)
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(name), safeMarker) {
		return &ErrUnsafeTarget{Database: name}
	}
	return nil
}

// databaseName extracts the database from a DSN without logging any part of it:
// a benchmark's error output is not a safe place for a password to surface.
func databaseName(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("could not parse the database DSN (its contents are not echoed here)")
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", fmt.Errorf("the database DSN names no database")
	}
	return name, nil
}
