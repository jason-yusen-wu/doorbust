package customers

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

// Resolver is on the reserve hot path, so what these tests care about is how
// many times it reaches the database, not just what it returns. scriptedQuerier
// embeds a nil repo.Querier, so a call nobody scripted panics rather than
// quietly succeeding — which is what lets "the cache did not hit the database"
// be asserted rather than assumed.

func resolved(id int64) repo.Customer {
	return repo.Customer{
		ID:         id,
		Email:      claims.Email,
		CognitoSub: pgtype.Text{String: claims.Subject, Valid: true},
	}
}

func TestDirectResolverResolvesEveryCall(t *testing.T) {
	t.Parallel()

	q := &scriptedQuerier{findBySub: []findResult{
		{customer: resolved(11)},
		{customer: resolved(11)},
	}}

	for range 2 {
		id, err := DirectResolver{}.Resolve(context.Background(), q, claims)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if id != 11 {
			t.Fatalf("id = %d, want 11", id)
		}
	}

	// The point of the direct resolver: no memory, so it costs a lookup every
	// time. That cost is exactly what the cached resolver exists to remove.
	if q.findSubCalls != 2 {
		t.Errorf("FindCustomerBySub called %d times, want 2", q.findSubCalls)
	}
}

func TestCachedResolverHitsTheDatabaseOnce(t *testing.T) {
	t.Parallel()

	q := &scriptedQuerier{findBySub: []findResult{{customer: resolved(42)}}}
	c := &CachedResolver{}

	id, err := c.Resolve(context.Background(), q, claims)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}

	// Passing a nil Querier proves the second call never touched the database:
	// if it did, the nil embedded Querier would panic.
	id, err = c.Resolve(context.Background(), nil, claims)
	if err != nil {
		t.Fatalf("cached Resolve: %v", err)
	}
	if id != 42 {
		t.Errorf("cached id = %d, want 42", id)
	}
	if q.findSubCalls != 1 {
		t.Errorf("FindCustomerBySub called %d times, want 1", q.findSubCalls)
	}
	if got := c.Len(); got != 1 {
		t.Errorf("cache holds %d entries, want 1", got)
	}
}

func TestCachedResolverKeysOnSubject(t *testing.T) {
	t.Parallel()

	other := auth.Claims{Subject: "sub-2", Email: "other@example.test"}

	q := &scriptedQuerier{findBySub: []findResult{
		{customer: repo.Customer{ID: 1, CognitoSub: pgtype.Text{String: claims.Subject, Valid: true}}},
		{customer: repo.Customer{ID: 2, CognitoSub: pgtype.Text{String: other.Subject, Valid: true}}},
	}}
	c := &CachedResolver{}

	first, err := c.Resolve(context.Background(), q, claims)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	second, err := c.Resolve(context.Background(), q, other)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if first == second {
		t.Fatalf("two subjects resolved to the same id %d — the cache is not keyed on the subject", first)
	}
	if got := c.Len(); got != 2 {
		t.Errorf("cache holds %d entries, want 2", got)
	}
}

// A caller with no subject has no stable cache key. Caching one under "" would
// hand the first such caller's customer id to every later one — a cross-account
// leak, not a performance bug.
func TestCachedResolverNeverCachesASubjectlessCaller(t *testing.T) {
	t.Parallel()

	anon := auth.Claims{Email: "legacy@example.test"}
	q := &scriptedQuerier{linkResults: []linkResult{
		{customer: repo.Customer{ID: 5, Email: anon.Email}},
		{customer: repo.Customer{ID: 5, Email: anon.Email}},
	}}
	c := &CachedResolver{}

	for range 2 {
		if _, err := c.Resolve(context.Background(), q, anon); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}

	if q.linkCalls != 2 {
		t.Errorf("LinkCustomer called %d times, want 2 — a subjectless caller must not be cached", q.linkCalls)
	}
	if got := c.Len(); got != 0 {
		t.Errorf("cache holds %d entries, want 0", got)
	}
}

func TestCachedResolverDoesNotCacheFailures(t *testing.T) {
	t.Parallel()

	boom := errors.New("connection refused")
	q := &scriptedQuerier{findBySub: []findResult{
		{err: boom},
		{customer: resolved(9)},
	}}
	c := &CachedResolver{}

	if _, err := c.Resolve(context.Background(), q, claims); !errors.Is(err, boom) {
		t.Fatalf("got %v, want the underlying error", err)
	}
	if got := c.Len(); got != 0 {
		t.Fatalf("a failed lookup was cached (%d entries)", got)
	}

	// A transient failure must not poison the identity forever.
	id, err := c.Resolve(context.Background(), q, claims)
	if err != nil {
		t.Fatalf("retry after a transient failure: %v", err)
	}
	if id != 9 {
		t.Errorf("id = %d, want 9", id)
	}
}

// The reserve path is concurrent by definition, so the cache is too.
func TestCachedResolverIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	q := &concurrentQuerier{id: 77, email: claims.Email}
	c := &CachedResolver{}

	var wg sync.WaitGroup
	ids := make([]int64, 64)

	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := c.Resolve(context.Background(), q, claims)
			if err != nil {
				t.Errorf("Resolve: %v", err)
				return
			}
			ids[i] = id
		}()
	}
	wg.Wait()

	for i, id := range ids {
		if id != 77 {
			t.Fatalf("goroutine %d resolved %d, want 77", i, id)
		}
	}
	if got := c.Len(); got != 1 {
		t.Errorf("cache holds %d entries, want 1", got)
	}
}

// concurrentQuerier answers FindCustomerBySub from any number of goroutines.
// scriptedQuerier cannot: its call counters are plain ints, so racing tests
// would be measuring the harness rather than the cache.
type concurrentQuerier struct {
	repo.Querier
	id    int64
	email string
}

// The email must match the caller's, or linkOnce takes its email-adoption
// branch and calls UpdateCustomerEmail — which this fake does not implement.
func (c *concurrentQuerier) FindCustomerBySub(_ context.Context, sub pgtype.Text) (repo.Customer, error) {
	if !sub.Valid {
		return repo.Customer{}, pgx.ErrNoRows
	}
	return repo.Customer{ID: c.id, Email: c.email, CognitoSub: sub}, nil
}
