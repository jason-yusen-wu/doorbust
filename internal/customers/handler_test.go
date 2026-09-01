package customers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// GetMe's happy path is covered end to end through the real router in
// cmd/contract_test.go. These cover its refusals, which are easier to drive
// directly and easy to get wrong.

type stubService struct {
	customer repo.Customer
	err      error
	calls    int
}

func (s *stubService) GetOrCreate(context.Context, auth.Claims) (repo.Customer, error) {
	s.calls++
	return s.customer, s.err
}

func assertEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()

	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, wantStatus, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not the error envelope: %v; body %s", err, rec.Body)
	}
	if body.Error.Code != wantCode {
		t.Errorf("code = %q, want %q", body.Error.Code, wantCode)
	}
}

// Called without the auth middleware in front of it — a wiring mistake rather
// than a caller error, but it must still refuse rather than panic on the
// missing claims.
func TestGetMeWithoutClaims(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	h := NewHandler(svc, testsupport.VendorGroup)

	rec := httptest.NewRecorder()
	h.GetMe(rec, httptest.NewRequest(http.MethodGet, "/me", nil))

	assertEnvelope(t, rec, http.StatusUnauthorized, "unauthorized")
	if svc.calls != 0 {
		t.Error("reached the service with no verified caller")
	}
}

// email is still the upsert key, so a token without one cannot be mapped to a
// customer. Letting it through would collide every such user onto one row.
func TestGetMeRejectsATokenWithNoEmail(t *testing.T) {
	t.Parallel()

	issuer := testsupport.NewIssuer(t)
	svc := &stubService{}
	h := NewHandler(svc, testsupport.VendorGroup)

	token := issuer.Token(t, testsupport.TokenClaims{Subject: "sub-no-email"})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	issuer.Verifier(t).Middleware(http.HandlerFunc(h.GetMe)).ServeHTTP(rec, req)

	assertEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
	if svc.calls != 0 {
		t.Error("reached the service with no email to key on")
	}
}

// A database failure must become a bare 500 — pgx errors embed the connection
// user and database name.
func TestGetMeHidesServiceFailures(t *testing.T) {
	t.Parallel()

	issuer := testsupport.NewIssuer(t)
	svc := &stubService{err: errors.New(`failed to connect to "user=doorbust database=doorbust_prod"`)}
	h := NewHandler(svc, testsupport.VendorGroup)

	token, _ := issuer.Buyer(t, 1)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	issuer.Verifier(t).Middleware(http.HandlerFunc(h.GetMe)).ServeHTTP(rec, req)

	assertEnvelope(t, rec, http.StatusInternalServerError, "internal_error")

	for _, leak := range []string{"user=", "database=", "doorbust_prod"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("500 body leaked %q: %s", leak, rec.Body)
		}
	}
}
