package loadgen

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	appjson "github.com/jason-yusen-wu/doorbust/internal/json"
)

// Outcome is what happened to one request.
//
// The taxonomy exists because collapsing these into "errors" would hide the
// only failure that matters. An out-of-stock response is a CORRECT ANSWER — the
// server did its job and told a buyer the truth — and counting it as an error
// would make a successful doorbuster run look like a catastrophe. A 5xx is the
// real failure. A connection error is usually the generator's own limits, and
// invalidates the run rather than describing the server.
type Outcome uint8

const (
	OutcomeReserved Outcome = iota
	OutcomeOutOfStock
	OutcomeNotFound
	OutcomeUnauthorized
	OutcomeConflictOther
	OutcomeServerError
	OutcomeTimeout
	OutcomeConnectError
	OutcomeGeneratorSaturated
	outcomeCount
)

var outcomeNames = [outcomeCount]string{
	OutcomeReserved:           "reserved",
	OutcomeOutOfStock:         "out_of_stock",
	OutcomeNotFound:           "not_found",
	OutcomeUnauthorized:       "unauthorized",
	OutcomeConflictOther:      "conflict_other",
	OutcomeServerError:        "server_error",
	OutcomeTimeout:            "timeout",
	OutcomeConnectError:       "connect_error",
	OutcomeGeneratorSaturated: "generator_saturated",
}

func (o Outcome) String() string { return outcomeNames[o] }

// OutcomeNames lists every class, so a result's outcome map always has the same
// keys and a zero is visibly zero rather than missing.
func OutcomeNames() []string { return outcomeNames[:] }

// ClassifyError maps a transport-level failure.
func ClassifyError(err error) Outcome {
	if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
		return OutcomeTimeout
	}
	return OutcomeConnectError
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// ClassifyResponse maps a response to an outcome, reading the body's stable
// `code` field rather than trusting the status alone.
//
// Status is not sufficient: 409 is both out_of_stock and order_not_pending, and
// only one of those is a correct answer to a reserve. The code field is the
// machine-readable half of the API contract precisely so a client can tell them
// apart, and this is a client.
//
// The body is always drained and closed. Leaving it undrained prevents the
// connection being reused, which quietly turns a keep-alive run into a
// connection-per-request run and measures the generator.
func ClassifyResponse(resp *http.Response) Outcome {
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_, _ = io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == http.StatusCreated:
		return OutcomeReserved
	case resp.StatusCode >= 500:
		return OutcomeServerError
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return OutcomeUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return OutcomeNotFound
	}

	var envelope appjson.ErrorBody
	if err := json.Unmarshal(body, &envelope); err == nil {
		switch envelope.Error.Code {
		case appjson.CodeOutOfStock:
			return OutcomeOutOfStock
		case appjson.CodeNotFound:
			return OutcomeNotFound
		case appjson.CodeUnauthorized, appjson.CodeForbidden:
			return OutcomeUnauthorized
		}
	}

	if resp.StatusCode == http.StatusConflict {
		return OutcomeConflictOther
	}
	// Anything else is unexpected enough that calling it a server error is the
	// safe direction: it shows up loudly instead of being averaged away.
	return OutcomeServerError
}
