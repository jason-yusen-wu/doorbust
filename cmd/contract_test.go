package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/jason-yusen-wu/doorbust/internal/testsupport"
)

// The JSON contract a frontend is built against.
//
// These assert the exact top-level key set of every response, so a rename or a
// dropped field fails here rather than in someone else's UI. AssertJSONKeys
// fails on unexpected keys too: a field added without thought is how a
// response starts leaking internals.

var (
	productKeys = []string{
		"id", "name", "price_in_cents", "created_at", "start_at",
		"quantity", "num_reserved", "available",
	}
	orderKeys = []string{
		"id", "product_id", "status", "total_in_cents", "created_at", "expires_at",
	}
	meKeys = []string{"id", "email", "subject", "groups", "is_vendor"}
)

func TestProductContract(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	vendorToken, _ := h.issuer.Vendor(t, 1)

	t.Run("create returns 201 and the product shape", func(t *testing.T) {
		rec := h.do(t, http.MethodPost, "/products", vendorToken,
			`{"name":"Doorbuster TV","price_in_cents":49999,"quantity":5}`)
		testsupport.AssertStatus(t, rec, http.StatusCreated)
		testsupport.AssertJSONKeys(t, rec.Body.Bytes(), productKeys)

		var got map[string]any
		testsupport.DecodeJSON(t, rec, &got)

		// available is computed, not stored — the reason the handler has its
		// own response type rather than returning the generated row.
		if got["available"] != float64(5) {
			t.Errorf("available = %v, want 5", got["available"])
		}
		if got["price_in_cents"] != float64(49999) {
			t.Errorf("price_in_cents = %v, want 49999", got["price_in_cents"])
		}
	})

	t.Run("list returns an array with the same shape", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/products", "", "")
		testsupport.AssertStatus(t, rec, http.StatusOK)

		var list []json.RawMessage
		testsupport.DecodeJSON(t, rec, &list)
		if len(list) == 0 {
			t.Fatal("expected at least the seeded product")
		}
		testsupport.AssertJSONKeys(t, list[0], productKeys)
	})

	t.Run("empty list is [] not null", func(t *testing.T) {
		// A fresh database: a nil Go slice would marshal to null and break a
		// client that iterates without a nil check.
		empty := newHarness(t)
		rec := empty.do(t, http.MethodGet, "/products", "", "")
		testsupport.AssertStatus(t, rec, http.StatusOK)
		testsupport.AssertJSONArrayNotNull(t, rec)
	})

	t.Run("missing product is a 404 envelope", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/products/999999", "", "")
		testsupport.AssertErrorEnvelope(t, rec, http.StatusNotFound, "not_found")
	})

	t.Run("non-numeric id is rejected", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/products/abc", "", "")
		testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
	})
}

func TestProductValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	vendorToken, _ := h.issuer.Vendor(t, 1)

	tests := []struct {
		name string
		body string
	}{
		{"empty name", `{"name":"","price_in_cents":100,"quantity":1}`},
		{"negative price", `{"name":"x","price_in_cents":-1,"quantity":1}`},
		{"negative quantity", `{"name":"x","price_in_cents":100,"quantity":-1}`},
		{"malformed json", `{"name":`},
		// Decode uses DisallowUnknownFields so a typo'd field fails loudly
		// rather than being silently dropped.
		{"unknown field", `{"name":"x","price_in_cents":100,"quantity":1,"prize":5}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(t, http.MethodPost, "/products", vendorToken, tt.body)
			testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestPaginationContract(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	vendorToken, _ := h.issuer.Vendor(t, 1)

	for i := range 3 {
		rec := h.do(t, http.MethodPost, "/products", vendorToken,
			fmt.Sprintf(`{"name":"p%d","price_in_cents":100,"quantity":1}`, i))
		testsupport.AssertStatus(t, rec, http.StatusCreated)
	}

	t.Run("limit is honoured", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/products?limit=2", "", "")
		var list []json.RawMessage
		testsupport.DecodeJSON(t, rec, &list)
		if len(list) != 2 {
			t.Errorf("got %d products, want 2", len(list))
		}
	})

	t.Run("offset skips", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/products?limit=10&offset=2", "", "")
		var list []json.RawMessage
		testsupport.DecodeJSON(t, rec, &list)
		if len(list) != 1 {
			t.Errorf("got %d products, want 1", len(list))
		}
	})

	t.Run("limit above the maximum is clamped, not rejected", func(t *testing.T) {
		if rec := h.do(t, http.MethodGet, "/products?limit=5000", "", ""); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (over-large limit clamps to 100)", rec.Code)
		}
	})

	for _, bad := range []string{"?limit=0", "?limit=-1", "?limit=abc", "?offset=-1", "?offset=xyz"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, "/products"+bad, "", "")
			testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestMeContract(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	t.Run("provisions a customer on first call", func(t *testing.T) {
		buyerToken, claims := h.issuer.Buyer(t, 1)

		if n := testsupport.CountRows(t, h.db, "customers"); n != 0 {
			t.Fatalf("expected no customers before /me, found %d", n)
		}

		rec := h.do(t, http.MethodGet, "/me", buyerToken, "")
		testsupport.AssertStatus(t, rec, http.StatusOK)
		testsupport.AssertJSONKeys(t, rec.Body.Bytes(), meKeys)

		var body struct {
			ID       int64    `json:"id"`
			Email    string   `json:"email"`
			Subject  string   `json:"subject"`
			Groups   []string `json:"groups"`
			IsVendor bool     `json:"is_vendor"`
		}
		testsupport.DecodeJSON(t, rec, &body)

		if body.Email != claims.Email {
			t.Errorf("email = %q, want %q", body.Email, claims.Email)
		}
		// The Cognito subject must be persisted; ownership checks prefer it
		// over the mutable email.
		if body.Subject != claims.Subject {
			t.Errorf("subject = %q, want %q", body.Subject, claims.Subject)
		}
		if body.IsVendor {
			t.Error("a buyer must not be reported as a vendor")
		}
		// [] not null, so a client can iterate without a nil check.
		if body.Groups == nil {
			t.Error("groups serialized as null, want []")
		}

		if n := testsupport.CountRows(t, h.db, "customers"); n != 1 {
			t.Errorf("customers = %d after /me, want 1", n)
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		token, _ := h.issuer.Buyer(t, 2)
		before := testsupport.CountRows(t, h.db, "customers")

		for range 3 {
			testsupport.AssertStatus(t, h.do(t, http.MethodGet, "/me", token, ""), http.StatusOK)
		}

		if got := testsupport.CountRows(t, h.db, "customers"); got != before+1 {
			t.Errorf("customers = %d, want %d; /me is not idempotent", got, before+1)
		}
	})

	t.Run("reports vendor membership", func(t *testing.T) {
		token, _ := h.issuer.Vendor(t, 3)

		rec := h.do(t, http.MethodGet, "/me", token, "")
		testsupport.AssertStatus(t, rec, http.StatusOK)

		var body struct {
			Groups   []string `json:"groups"`
			IsVendor bool     `json:"is_vendor"`
		}
		testsupport.DecodeJSON(t, rec, &body)

		if !body.IsVendor {
			t.Error("is_vendor = false for a member of the vendors group")
		}
		if len(body.Groups) != 1 || body.Groups[0] != testsupport.VendorGroup {
			t.Errorf("groups = %v, want [%s]", body.Groups, testsupport.VendorGroup)
		}
	})
}

func TestOrderContract(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	buyerToken, _ := h.issuer.Buyer(t, 1)
	productID := testsupport.SeedProduct(t, h.db, "contract-sku", 2500, 3)

	var orderID int64

	t.Run("reserve returns 201 and the order shape", func(t *testing.T) {
		rec := h.do(t, http.MethodPost, "/orders", buyerToken,
			fmt.Sprintf(`{"product_id":%d}`, productID))
		testsupport.AssertStatus(t, rec, http.StatusCreated)
		testsupport.AssertJSONKeys(t, rec.Body.Bytes(), orderKeys)

		var body struct {
			ID           int64  `json:"id"`
			Status       string `json:"status"`
			TotalInCents int32  `json:"total_in_cents"`
			ExpiresAt    string `json:"expires_at"`
		}
		testsupport.DecodeJSON(t, rec, &body)
		orderID = body.ID

		if body.Status != orders.StatusPending {
			t.Errorf("status = %q, want %q", body.Status, orders.StatusPending)
		}
		// The price is snapshotted at reserve time so a later change cannot
		// move what the buyer is charged.
		if body.TotalInCents != 2500 {
			t.Errorf("total_in_cents = %d, want 2500", body.TotalInCents)
		}
		if body.ExpiresAt == "" {
			t.Error("expires_at is empty; the reservation would never be swept")
		}

		// Reserving must hold stock without consuming it.
		testsupport.AssertStock(t, h.db, productID, 3, 1)
	})

	t.Run("list returns the caller's own orders", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/orders", buyerToken, "")
		testsupport.AssertStatus(t, rec, http.StatusOK)

		var list []json.RawMessage
		testsupport.DecodeJSON(t, rec, &list)
		if len(list) != 1 {
			t.Fatalf("got %d orders, want 1", len(list))
		}
		// The list view joins the product name so a client needs no N+1
		// follow-up requests.
		testsupport.AssertJSONKeys(t, list[0], append(append([]string{}, orderKeys...), "product_name"))
	})

	t.Run("another buyer sees an empty list, not this one's orders", func(t *testing.T) {
		otherToken, _ := h.issuer.Buyer(t, 2)

		rec := h.do(t, http.MethodGet, "/orders", otherToken, "")
		testsupport.AssertStatus(t, rec, http.StatusOK)
		testsupport.AssertJSONArrayNotNull(t, rec)

		var list []json.RawMessage
		testsupport.DecodeJSON(t, rec, &list)
		if len(list) != 0 {
			t.Errorf("got %d orders for a different customer, want 0", len(list))
		}
	})

	t.Run("another buyer cannot read this order", func(t *testing.T) {
		otherToken, _ := h.issuer.Buyer(t, 3)

		rec := h.do(t, http.MethodGet, fmt.Sprintf("/orders/%d", orderID), otherToken, "")
		testsupport.AssertErrorEnvelope(t, rec, http.StatusForbidden, "forbidden")
	})

	t.Run("out of stock is a 409", func(t *testing.T) {
		sold := testsupport.SeedProduct(t, h.db, "sold-out", 100, 1)
		token, _ := h.issuer.Buyer(t, 4)

		first := h.do(t, http.MethodPost, "/orders", token, fmt.Sprintf(`{"product_id":%d}`, sold))
		testsupport.AssertStatus(t, first, http.StatusCreated)

		second := h.do(t, http.MethodPost, "/orders", token, fmt.Sprintf(`{"product_id":%d}`, sold))
		testsupport.AssertErrorEnvelope(t, second, http.StatusConflict, "out_of_stock")
	})

	t.Run("missing product_id is rejected", func(t *testing.T) {
		rec := h.do(t, http.MethodPost, "/orders", buyerToken, `{}`)
		testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("reserving a product that does not exist is a 404", func(t *testing.T) {
		rec := h.do(t, http.MethodPost, "/orders", buyerToken, `{"product_id":999999}`)
		testsupport.AssertErrorEnvelope(t, rec, http.StatusNotFound, "not_found")
	})

	t.Run("unknown request field is rejected", func(t *testing.T) {
		rec := h.do(t, http.MethodPost, "/orders", buyerToken,
			fmt.Sprintf(`{"product_id":%d,"quantity":5}`, productID))
		testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
	})
}

// The /orders collection shares the ?limit=&offset= convention with
// /products, and the {id} routes share id parsing. Both are contract surface a
// client will hit with bad input.
func TestOrderRequestValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	buyerToken, _ := h.issuer.Buyer(t, 1)

	t.Run("pagination", func(t *testing.T) {
		for _, bad := range []string{"?limit=0", "?limit=-3", "?limit=lots", "?offset=-1", "?offset=soon"} {
			t.Run(bad, func(t *testing.T) {
				rec := h.do(t, http.MethodGet, "/orders"+bad, buyerToken, "")
				testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}

		t.Run("an over-large limit clamps rather than failing", func(t *testing.T) {
			rec := h.do(t, http.MethodGet, "/orders?limit=9999", buyerToken, "")
			testsupport.AssertStatus(t, rec, http.StatusOK)
		})
	})

	t.Run("malformed order id", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			t.Run(method, func(t *testing.T) {
				rec := h.do(t, method, "/orders/not-a-number", buyerToken, "")
				testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}

		t.Run("checkout", func(t *testing.T) {
			rec := h.do(t, http.MethodPost, "/orders/not-a-number/checkout", buyerToken, "")
			testsupport.AssertErrorEnvelope(t, rec, http.StatusBadRequest, "invalid_request")
		})
	})

	t.Run("missing order is a 404", func(t *testing.T) {
		rec := h.do(t, http.MethodGet, "/orders/999999", buyerToken, "")
		testsupport.AssertErrorEnvelope(t, rec, http.StatusNotFound, "not_found")
	})
}
