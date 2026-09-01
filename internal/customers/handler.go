package customers

import (
	"log"
	"net/http"

	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/json"
)

// HTTP methods live here
type handler struct {
	service Service
	// vendorGroup is the Cognito group name that gates POST /products. Held
	// here only so /me can report is_vendor with the same name the route is
	// actually guarded by — a hardcoded copy would silently disagree if the
	// group were renamed.
	vendorGroup string
}

// constructor that wraps a handler around a service
func NewHandler(service Service, vendorGroup string) *handler {
	return &handler{service: service, vendorGroup: vendorGroup}
}

// meResponse deliberately omits the internal customer id's role as a
// join key and exposes only what a client needs to render an account view.
type meResponse struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	// Subject is the Cognito sub the token was issued for. Echoed back so a
	// client can tell that its token and its profile agree.
	Subject string `json:"subject"`
	// Groups and IsVendor let a client decide whether to render vendor UI at
	// all. Without them the only way to discover permission is to attempt the
	// action and read a 403, which is a poor thing to build a nav bar on.
	//
	// These come from the token, so they reflect what the caller can do
	// *right now* with this token — a membership change granted since it was
	// issued will not appear until the token refreshes.
	Groups   []string `json:"groups"`
	IsVendor bool     `json:"is_vendor"`
}

// GetMe returns the caller's customer profile, creating it on first call.
// The frontend calls this immediately after the Cognito Hosted UI redirect,
// which is what provisions a customer for a user who has never ordered.
func (h *handler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		json.WriteError(w, http.StatusUnauthorized, json.CodeUnauthorized, "unauthorized")
		return
	}

	// An ID token without an email claim can't be mapped to a customer: email
	// is still the upsert key, and a blank one would collide every such user
	// onto a single row.
	if claims.Email == "" {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "token has no email claim")
		return
	}

	customer, err := h.service.GetOrCreate(r.Context(), claims)
	if err != nil {
		log.Println(err)
		json.WriteInternalError(w)
		return
	}

	// [] rather than null for a caller in no groups, so a client can iterate
	// without a nil check.
	groups := claims.Groups
	if groups == nil {
		groups = []string{}
	}

	json.Write(w, http.StatusOK, meResponse{
		ID:       customer.ID,
		Email:    customer.Email,
		Subject:  customer.CognitoSub.String,
		Groups:   groups,
		IsVendor: claims.HasGroup(h.vendorGroup),
	})
}
