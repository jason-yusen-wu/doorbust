package customers

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
)

// Link's retry exists because the customers table carries two unique
// constraints and ON CONFLICT arbitrates only one. The race that motivates it
// is real but rare, so these drive it deterministically with a scripted
// Querier rather than hoping for a collision.

// scriptedQuerier returns a canned sequence of results. Only the three methods
// Link uses are implemented; anything else panics on the embedded nil.
type scriptedQuerier struct {
	repo.Querier

	// findBySub is consulted in order, one entry per call.
	findBySub    []findResult
	findBySubIdx int

	// linkResults is consulted in order, one entry per LinkCustomer call.
	linkResults []linkResult
	linkIdx     int

	updated      *repo.Customer
	updateCalls  int
	linkCalls    int
	findSubCalls int
}

type findResult struct {
	customer repo.Customer
	err      error
}

type linkResult struct {
	customer repo.Customer
	err      error
}

func (s *scriptedQuerier) FindCustomerBySub(context.Context, pgtype.Text) (repo.Customer, error) {
	s.findSubCalls++
	if s.findBySubIdx >= len(s.findBySub) {
		return repo.Customer{}, pgx.ErrNoRows
	}
	r := s.findBySub[s.findBySubIdx]
	s.findBySubIdx++
	return r.customer, r.err
}

func (s *scriptedQuerier) LinkCustomer(context.Context, repo.LinkCustomerParams) (repo.Customer, error) {
	s.linkCalls++
	if s.linkIdx >= len(s.linkResults) {
		return repo.Customer{}, errors.New("unexpected LinkCustomer call")
	}
	r := s.linkResults[s.linkIdx]
	s.linkIdx++
	return r.customer, r.err
}

func (s *scriptedQuerier) UpdateCustomerEmail(_ context.Context, arg repo.UpdateCustomerEmailParams) (repo.Customer, error) {
	s.updateCalls++
	c := repo.Customer{ID: arg.ID, Email: arg.Email}
	s.updated = &c
	return c, nil
}

func duplicateKey(constraint string) error {
	return &pgconn.PgError{
		Code:           uniqueViolation,
		Message:        "duplicate key value violates unique constraint",
		ConstraintName: constraint,
	}
}

var claims = auth.Claims{Subject: "sub-1", Email: "user@example.test"}

// The race this fix exists for: our insert loses to a concurrent one and trips
// the cognito_sub constraint. Retrying finds the winner's row instead of
// surfacing a 500 to a user who did nothing wrong.
func TestLinkRetriesAfterLosingARace(t *testing.T) {
	t.Parallel()

	winner := repo.Customer{ID: 7, Email: claims.Email, CognitoSub: pgtype.Text{String: claims.Subject, Valid: true}}

	q := &scriptedQuerier{
		findBySub: []findResult{
			{err: pgx.ErrNoRows}, // first pass: nothing there yet
			{customer: winner},   // second pass: the winner's row exists
		},
		linkResults: []linkResult{
			{err: duplicateKey("customers_cognito_sub_key")},
		},
	}

	got, err := Link(context.Background(), q, claims)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.ID != winner.ID {
		t.Errorf("id = %d, want the winning row %d", got.ID, winner.ID)
	}
	if q.findSubCalls != 2 {
		t.Errorf("FindCustomerBySub called %d times, want 2 (the retry did not re-look-up)", q.findSubCalls)
	}
	if q.linkCalls != 1 {
		t.Errorf("LinkCustomer called %d times, want 1", q.linkCalls)
	}
}

// A duplicate that never resolves must eventually surface rather than spin.
func TestLinkGivesUpAfterRepeatedViolations(t *testing.T) {
	t.Parallel()

	q := &scriptedQuerier{
		linkResults: []linkResult{
			{err: duplicateKey("customers_email_key")},
			{err: duplicateKey("customers_email_key")},
			{err: duplicateKey("customers_email_key")},
		},
	}

	_, err := Link(context.Background(), q, claims)
	if err == nil {
		t.Fatal("expected the error to surface once the retry budget is spent")
	}
	if !isUniqueViolation(err) {
		t.Errorf("got %v, want the unique violation preserved", err)
	}
	if q.linkCalls != linkAttempts {
		t.Errorf("LinkCustomer called %d times, want %d", q.linkCalls, linkAttempts)
	}
}

// Only duplicate-key errors are worth retrying; anything else is returned at
// once so a genuine failure is not delayed by a pointless retry loop.
func TestLinkDoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("connection refused")
	q := &scriptedQuerier{linkResults: []linkResult{{err: boom}}}

	_, err := Link(context.Background(), q, claims)
	if !errors.Is(err, boom) {
		t.Errorf("got %v, want %v", err, boom)
	}
	if q.linkCalls != 1 {
		t.Errorf("LinkCustomer called %d times, want 1 — a non-duplicate error must not retry", q.linkCalls)
	}
}

// A lookup failure that is not "no rows" must not be mistaken for "not found"
// and turned into an insert.
func TestLinkSurfacesLookupFailures(t *testing.T) {
	t.Parallel()

	boom := errors.New("read timeout")
	q := &scriptedQuerier{findBySub: []findResult{{err: boom}}}

	if _, err := Link(context.Background(), q, claims); !errors.Is(err, boom) {
		t.Errorf("got %v, want %v", err, boom)
	}
	if q.linkCalls != 0 {
		t.Error("attempted an insert after an unexplained lookup failure")
	}
}

// A caller with no subject — a token missing the claim — must still resolve,
// via the email upsert, rather than looking up a NULL subject.
func TestLinkWithoutASubjectUsesTheEmailUpsert(t *testing.T) {
	t.Parallel()

	want := repo.Customer{ID: 3, Email: "legacy@example.test"}
	q := &scriptedQuerier{linkResults: []linkResult{{customer: want}}}

	got, err := Link(context.Background(), q, auth.Claims{Email: want.Email})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("id = %d, want %d", got.ID, want.ID)
	}
	if q.findSubCalls != 0 {
		t.Error("looked up a NULL subject; every row without one would match")
	}
}

// An existing row whose email still matches must be returned untouched — the
// common case, and it should cost no write.
func TestLinkLeavesAnUnchangedEmailAlone(t *testing.T) {
	t.Parallel()

	existing := repo.Customer{ID: 5, Email: claims.Email, CognitoSub: pgtype.Text{String: claims.Subject, Valid: true}}
	q := &scriptedQuerier{findBySub: []findResult{{customer: existing}}}

	got, err := Link(context.Background(), q, claims)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.ID != existing.ID {
		t.Errorf("id = %d, want %d", got.ID, existing.ID)
	}
	if q.updateCalls != 0 {
		t.Error("rewrote an email that had not changed")
	}
	if q.linkCalls != 0 {
		t.Error("inserted despite the subject already having a row")
	}
}

// A token with a subject but no email must not blank out the stored address.
func TestLinkIgnoresAnEmptyEmail(t *testing.T) {
	t.Parallel()

	existing := repo.Customer{ID: 9, Email: "kept@example.test", CognitoSub: pgtype.Text{String: claims.Subject, Valid: true}}
	q := &scriptedQuerier{findBySub: []findResult{{customer: existing}}}

	got, err := Link(context.Background(), q, auth.Claims{Subject: claims.Subject})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.Email != existing.Email {
		t.Errorf("email = %q, want it left as %q", got.Email, existing.Email)
	}
	if q.updateCalls != 0 {
		t.Error("overwrote a stored email with an empty one")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()

	if !isUniqueViolation(duplicateKey("customers_email_key")) {
		t.Error("a 23505 was not recognised")
	}
	// Wrapped errors must still be recognised — Link sees them through the
	// query layer.
	if !isUniqueViolation(errors.Join(errors.New("context"), duplicateKey("x"))) {
		t.Error("a wrapped 23505 was not recognised")
	}
	if isUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Error("a foreign-key violation was treated as a duplicate")
	}
	if isUniqueViolation(errors.New("plain")) {
		t.Error("a non-pg error was treated as a duplicate")
	}
	if isUniqueViolation(nil) {
		t.Error("nil was treated as a duplicate")
	}
}
