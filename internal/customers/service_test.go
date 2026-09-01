package customers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// LinkCustomer is the bridge between two identity stores — the Cognito pool
// and our customers table — and it is deliberately an upsert. These tests pin
// the three behaviours that bridge depends on: create, return-existing, and
// backfill of cognito_sub on rows that predate the column.

func TestGetOrCreateCreatesOnFirstCall(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool))

	claims := auth.Claims{Subject: "sub-new", Email: "new@example.test"}

	got, err := service.GetOrCreate(context.Background(), claims)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	if got.Email != claims.Email {
		t.Errorf("email = %q, want %q", got.Email, claims.Email)
	}
	if !got.CognitoSub.Valid || got.CognitoSub.String != claims.Subject {
		t.Errorf("cognito_sub = %+v, want %q", got.CognitoSub, claims.Subject)
	}
	if n := testsupport.CountRows(t, pool, "customers"); n != 1 {
		t.Errorf("customers = %d, want 1", n)
	}
}

func TestGetOrCreateIsIdempotent(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool))
	ctx := context.Background()

	claims := auth.Claims{Subject: "sub-repeat", Email: "repeat@example.test"}

	first, err := service.GetOrCreate(ctx, claims)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	for range 4 {
		again, err := service.GetOrCreate(ctx, claims)
		if err != nil {
			t.Fatalf("repeat call: %v", err)
		}
		if again.ID != first.ID {
			t.Errorf("id = %d on repeat, want the same row %d", again.ID, first.ID)
		}
	}

	if n := testsupport.CountRows(t, pool, "customers"); n != 1 {
		t.Errorf("customers = %d after repeated calls, want 1", n)
	}
}

// The frontend calls /me right after login, so two tabs opening at once is an
// ordinary event rather than an edge case. The upsert is what stops that
// racing into duplicate rows.
func TestGetOrCreateIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool))

	claims := auth.Claims{Subject: "sub-race", Email: "race@example.test"}

	const callers = 16
	var (
		wg     sync.WaitGroup
		ids    sync.Map
		errors atomic.Int64
		start  = make(chan struct{})
	)

	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			got, err := service.GetOrCreate(context.Background(), claims)
			if err != nil {
				errors.Add(1)
				t.Errorf("concurrent GetOrCreate failed: %v", err)
				return
			}
			ids.Store(got.ID, true)
		}()
	}

	close(start)
	wg.Wait()

	if n := errors.Load(); n != 0 {
		t.Errorf("%d concurrent callers errored", n)
	}

	distinct := 0
	ids.Range(func(any, any) bool { distinct++; return true })
	if distinct != 1 {
		t.Errorf("%d distinct customer ids, want 1", distinct)
	}
	if n := testsupport.CountRows(t, pool, "customers"); n != 1 {
		t.Errorf("customers = %d, want 1", n)
	}
}

// Migration 00005 added cognito_sub nullable, so rows created before it have
// none. They must be adopted on the owner's next call rather than duplicated.
func TestGetOrCreateBackfillsCognitoSub(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool))

	legacy := testsupport.SeedCustomer(t, pool, "legacy@example.test", "")
	if legacy.CognitoSub.Valid {
		t.Fatal("expected the seeded row to have no cognito_sub")
	}

	got, err := service.GetOrCreate(context.Background(), auth.Claims{
		Subject: "sub-backfilled",
		Email:   "legacy@example.test",
	})
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	if got.ID != legacy.ID {
		t.Errorf("id = %d, want the existing row %d — a duplicate was created", got.ID, legacy.ID)
	}
	if !got.CognitoSub.Valid || got.CognitoSub.String != "sub-backfilled" {
		t.Errorf("cognito_sub = %+v, want it backfilled", got.CognitoSub)
	}
	if n := testsupport.CountRows(t, pool, "customers"); n != 1 {
		t.Errorf("customers = %d, want 1", n)
	}
}

// A user who changes their email in Cognito keeps their subject. Before this
// was handled, the upsert saw no email conflict, inserted, and violated the
// cognito_sub unique constraint — permanently, on every subsequent request.
// The customers table has two unique constraints and ON CONFLICT arbitrates
// only one.
func TestGetOrCreateFollowsAnEmailChange(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool))
	ctx := context.Background()

	original, err := service.GetOrCreate(ctx, auth.Claims{Subject: "sub-mover", Email: "before@example.test"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	moved, err := service.GetOrCreate(ctx, auth.Claims{Subject: "sub-mover", Email: "after@example.test"})
	if err != nil {
		t.Fatalf("after an email change: %v", err)
	}

	if moved.ID != original.ID {
		t.Errorf("id = %d, want the same row %d — the user lost their order history", moved.ID, original.ID)
	}
	if moved.Email != "after@example.test" {
		t.Errorf("email = %q, want the new address", moved.Email)
	}
	if n := testsupport.CountRows(t, pool, "customers"); n != 1 {
		t.Errorf("customers = %d, want 1", n)
	}

	t.Run("and remains stable afterwards", func(t *testing.T) {
		again, err := service.GetOrCreate(ctx, auth.Claims{Subject: "sub-mover", Email: "after@example.test"})
		if err != nil {
			t.Fatalf("repeat after the change: %v", err)
		}
		if again.ID != original.ID {
			t.Errorf("id = %d, want %d", again.ID, original.ID)
		}
	})
}

// COALESCE keeps the first subject recorded. Overwriting it would let a later
// caller with the same email quietly take over an established identity.
func TestGetOrCreateDoesNotOverwriteAnExistingSubject(t *testing.T) {
	t.Parallel()

	pool := testsupport.DB(t)
	service := NewService(repo.New(pool))
	ctx := context.Background()

	original, err := service.GetOrCreate(ctx, auth.Claims{Subject: "sub-original", Email: "shared@example.test"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	got, err := service.GetOrCreate(ctx, auth.Claims{Subject: "sub-different", Email: "shared@example.test"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("id = %d, want %d", got.ID, original.ID)
	}
	if got.CognitoSub.String != "sub-original" {
		t.Errorf("cognito_sub = %q, want it to stay %q", got.CognitoSub.String, "sub-original")
	}
}
