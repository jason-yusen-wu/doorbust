// Package json is the shared HTTP JSON layer. Handlers use it instead of
// calling encoding/json (or http.Error) directly, so every response on the
// API — success or failure — has one predictable shape.
package json

import (
	"encoding/json"
	"net/http"
)

// maxRequestBody caps how much a client can make the server decode. Without
// it, an unauthenticated POST could stream an unbounded body straight into
// the decoder.
const maxRequestBody = 1 << 20 // 1 MiB

func Write(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// ErrorBody is the envelope every non-2xx response uses. Code is the stable,
// machine-readable half of the contract — clients branch on it, so the
// constants below must not be renamed once a frontend depends on them.
// Message is human-readable and may change freely.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Stable error codes. Never put a raw err.Error() in a 5xx message: pgx
// errors embed connection detail (user, database name) and would leak it to
// unauthenticated callers. Our own sentinel errors are safe to surface.
const (
	CodeInvalidRequest  = "invalid_request"
	CodeUnauthorized    = "unauthorized"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeOutOfStock      = "out_of_stock"
	CodeOrderNotPending = "order_not_pending"
	CodeInternal        = "internal_error"
)

// WriteError replaces http.Error, which writes text/plain and would leave the
// API returning two different content types depending on the outcome.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	Write(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// WriteInternalError is the 500 path: it never reveals err to the caller.
// Logging the real error is the caller's job.
func WriteInternalError(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
}

// Decode reads a JSON request body into dst. It rejects unknown fields so a
// typo'd client field fails loudly instead of being silently ignored.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
