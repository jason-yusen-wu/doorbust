package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// RequireGroup is the only authorization in the system, so its failure modes
// are worth pinning: a caller in no groups, a caller in the wrong group, and
// the wiring bug where it is mounted without Middleware in front of it.
func TestRequireGroup(t *testing.T) {
	const vendors = "vendors"

	tests := []struct {
		name       string
		claims     *Claims // nil means no claims on the context at all
		wantStatus int
		wantCode   string
	}{
		{
			name:       "caller in the group passes",
			claims:     &Claims{Subject: "s", Email: "v@example.test", Groups: []string{vendors}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "caller in several groups including this one passes",
			claims:     &Claims{Subject: "s", Groups: []string{"admins", vendors, "beta"}},
			wantStatus: http.StatusOK,
		},
		{
			// The common case: an ordinary buyer. Cognito omits the claim
			// entirely, so Groups is nil rather than empty.
			name:       "caller with no groups is forbidden",
			claims:     &Claims{Subject: "s", Email: "buyer@example.test"},
			wantStatus: http.StatusForbidden,
			wantCode:   "forbidden",
		},
		{
			name:       "caller in a different group is forbidden",
			claims:     &Claims{Subject: "s", Groups: []string{"admins"}},
			wantStatus: http.StatusForbidden,
			wantCode:   "forbidden",
		},
		{
			// Mounted without Middleware. 401, not 403 — we know nothing
			// about the caller, so we cannot claim they lack permission.
			name:       "no claims on the context is unauthorized",
			claims:     nil,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached bool
			handler := RequireGroup(vendors)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/products", nil)
			if tt.claims != nil {
				req = req.WithContext(context.WithValue(req.Context(), claimsContextKey, *tt.claims))
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			wantReached := tt.wantStatus == http.StatusOK
			if reached != wantReached {
				t.Errorf("next handler reached = %v, want %v", reached, wantReached)
			}

			if tt.wantCode == "" {
				return
			}

			// Rejections must use the same envelope as every other error, or
			// a client has to special-case authorization failures.
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not the JSON error envelope: %v (%s)", err, rec.Body)
			}
			if body.Error.Code != tt.wantCode {
				t.Errorf("error code = %q, want %q", body.Error.Code, tt.wantCode)
			}
			// The rejection must not disclose which group would have worked.
			if body.Error.Message == "" {
				t.Error("error message is empty")
			}
			if containsGroupName(body.Error.Message, vendors) {
				t.Errorf("message %q names the required group", body.Error.Message)
			}
		})
	}
}

func containsGroupName(message, group string) bool {
	for i := 0; i+len(group) <= len(message); i++ {
		if message[i:i+len(group)] == group {
			return true
		}
	}
	return false
}

func TestClaimsHasGroup(t *testing.T) {
	c := Claims{Groups: []string{"vendors", "admins"}}

	if !c.HasGroup("vendors") {
		t.Error("HasGroup(vendors) = false, want true")
	}
	if c.HasGroup("Vendors") {
		t.Error("HasGroup is case-insensitive; Cognito group names are case-sensitive")
	}
	if (Claims{}).HasGroup("vendors") {
		t.Error("zero-value Claims reported group membership")
	}
}
