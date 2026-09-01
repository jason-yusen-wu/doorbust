package testsupport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// HTTP assertion helpers. These exist mostly to make contract failures
// readable: a renamed JSON field should say which field, not just that two
// blobs differ.

// AssertStatus fails unless the response has the expected status, printing the
// body — which carries the error code — when it does not.
func AssertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}

// AssertErrorEnvelope fails unless the response is the shared error envelope
// with the expected stable code.
//
// The code is the machine-readable half of the contract — clients branch on
// it — so it is asserted exactly while the human-readable message is not.
func AssertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()

	AssertStatus(t, rec, wantStatus)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (errors must not be text/plain)", ct)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the error envelope: %v; body: %s", err, rec.Body)
	}

	if body.Error.Code != wantCode {
		t.Errorf("error code = %q, want %q", body.Error.Code, wantCode)
	}
	if body.Error.Message == "" {
		t.Error("error message is empty")
	}
}

// DecodeJSON unmarshals a response body, failing the test on malformed JSON.
func DecodeJSON(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()

	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body)
	}
}

// AssertJSONKeys fails unless the object's top-level keys are exactly want.
//
// This is the frontend contract check. It deliberately fails on *extra* keys
// as well as missing ones: a field added without thought is how a response
// starts leaking internals, and a field removed is how a frontend breaks. If
// a key is genuinely new, the expected list is the one place to record it.
func AssertJSONKeys(t *testing.T, body []byte, want []string) {
	t.Helper()

	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("response is not a JSON object: %v; body: %s", err, body)
	}

	got := make([]string, 0, len(object))
	for k := range object {
		got = append(got, k)
	}
	slices.Sort(got)

	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)

	if !slices.Equal(got, wantSorted) {
		var missing, extra []string
		for _, k := range wantSorted {
			if !slices.Contains(got, k) {
				missing = append(missing, k)
			}
		}
		for _, k := range got {
			if !slices.Contains(wantSorted, k) {
				extra = append(extra, k)
			}
		}
		t.Errorf("JSON keys mismatch.\n  missing: %v\n  unexpected: %v\n  got: %v", missing, extra, got)
	}
}

// AssertJSONArrayNotNull fails if the body is JSON null rather than an array.
//
// An empty page must serialize as [] so clients can iterate blindly; a nil Go
// slice marshals to null and quietly breaks that.
func AssertJSONArrayNotNull(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Body.String() == "null\n" || rec.Body.String() == "null" {
		t.Error("empty collection serialized as null, want []")
	}
}

// Request builds a request with an optional bearer token.
func Request(t *testing.T, method, target, token string, body string) *http.Request {
	t.Helper()

	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}
