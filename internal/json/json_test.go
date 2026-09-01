package json

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The error envelope is the machine-readable half of the API contract: clients
// branch on the code. These tests pin the shape and the code values, so a
// change to either has to be deliberate.

func TestWriteSetsContentTypeAndStatus(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	Write(rec, http.StatusCreated, map[string]string{"hello": "world"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"hello":"world"}` {
		t.Errorf("body = %q", got)
	}
}

func TestWriteErrorEnvelope(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusConflict, CodeOutOfStock, "product is out of stock")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body ErrorBody
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error.Code != CodeOutOfStock {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeOutOfStock)
	}
	if body.Error.Message != "product is out of stock" {
		t.Errorf("message = %q", body.Error.Message)
	}
}

// The envelope is nested under "error" rather than flat. A client destructuring
// the response depends on that, so it is asserted literally.
func TestErrorEnvelopeIsNested(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusNotFound, CodeNotFound, "nope")

	want := `{"error":{"code":"not_found","message":"nope"}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// These strings are the contract. Renaming one silently breaks every client
// that branches on it, so the values are pinned rather than referenced.
func TestErrorCodesAreStable(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"CodeInvalidRequest":  "invalid_request",
		"CodeUnauthorized":    "unauthorized",
		"CodeForbidden":       "forbidden",
		"CodeNotFound":        "not_found",
		"CodeConflict":        "conflict",
		"CodeOutOfStock":      "out_of_stock",
		"CodeOrderNotPending": "order_not_pending",
		"CodeInternal":        "internal_error",
	}

	got := map[string]string{
		"CodeInvalidRequest":  CodeInvalidRequest,
		"CodeUnauthorized":    CodeUnauthorized,
		"CodeForbidden":       CodeForbidden,
		"CodeNotFound":        CodeNotFound,
		"CodeConflict":        CodeConflict,
		"CodeOutOfStock":      CodeOutOfStock,
		"CodeOrderNotPending": CodeOrderNotPending,
		"CodeInternal":        CodeInternal,
	}

	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("%s = %q, want %q — this is a breaking API change", name, got[name], wantValue)
		}
	}
}

// 5xx bodies must never carry the cause: pgx errors embed the database user
// and name, and this endpoint set is reachable unauthenticated.
func TestWriteInternalErrorRevealsNothing(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteInternalError(rec)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	body := rec.Body.String()
	for _, leak := range []string{"postgres", "password", "user=", "database", "sql", "pgx"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("500 body contains %q: %s", leak, body)
		}
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	t.Run("decodes a well-formed body", func(t *testing.T) {
		t.Parallel()

		var got payload
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","count":3}`))
		if err := Decode(req, &got); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if got.Name != "x" || got.Count != 3 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("rejects unknown fields", func(t *testing.T) {
		t.Parallel()

		// A typo'd client field must fail loudly rather than be silently
		// ignored — otherwise a caller thinks they set something they didn't.
		var got payload
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","nmae":"y"}`))
		if err := Decode(req, &got); err == nil {
			t.Error("expected an error for an unknown field")
		}
	})

	t.Run("rejects malformed json", func(t *testing.T) {
		t.Parallel()

		var got payload
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":`))
		if err := Decode(req, &got); err == nil {
			t.Error("expected an error for malformed json")
		}
	})

	t.Run("rejects an oversized body", func(t *testing.T) {
		t.Parallel()

		// Without the cap, an unauthenticated POST could stream an unbounded
		// body straight into the decoder.
		huge := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`

		var got payload
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(huge))
		if err := Decode(req, &got); err == nil {
			t.Error("expected an error for a body over the 1 MiB cap")
		}
	})

	t.Run("rejects an empty body", func(t *testing.T) {
		t.Parallel()

		var got payload
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
		if err := Decode(req, &got); err == nil {
			t.Error("expected an error for an empty body")
		}
	})
}
