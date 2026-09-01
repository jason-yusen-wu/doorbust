package orders

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/json"
)

const (
	defaultListLimit = 20
	maxListLimit     = 100
)

// HTTP methods live here
type handler struct {
	service Service
}

// constructor that wraps a handler around a service
func NewHandler(service Service) *handler {
	return &handler{
		service: service,
	}
}

// orderResponse is the wire shape of an order. CustomerID is deliberately not
// exposed — it is an internal join key, and a caller can only ever see their
// own orders anyway.
type orderResponse struct {
	ID              int64              `json:"id"`
	ProductID       int64              `json:"product_id"`
	ProductName     string             `json:"product_name,omitempty"`
	Status          string             `json:"status"`
	TotalInCents    int32              `json:"total_in_cents"`
	CreatedAt       pgtype.Timestamptz `json:"created_at"`
	ExpiresAt       pgtype.Timestamptz `json:"expires_at"`
	PaymentIntentID string             `json:"payment_intent_id,omitempty"`
}

func fromOrder(o repo.Order, productName string) orderResponse {
	return orderResponse{
		ID:              o.ID,
		ProductID:       o.ProductID,
		ProductName:     productName,
		Status:          o.Status,
		TotalInCents:    o.TotalInCents,
		CreatedAt:       o.CreatedAt,
		ExpiresAt:       o.ExpiresAt,
		PaymentIntentID: o.StripePaymentIntentID.String,
	}
}

// parsePagination shares the ?limit=&offset= convention with /products.
func parsePagination(r *http.Request) (limit, offset int32, err error) {
	limit = defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n <= 0 {
			return 0, 0, errors.New("invalid limit")
		}
		limit = int32(n)
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return 0, 0, errors.New("invalid offset")
		}
		offset = int32(n)
	}
	return limit, offset, nil
}

// callerAndID pulls the two things almost every handler here needs: the
// verified caller and the {id} path parameter.
func callerAndID(w http.ResponseWriter, r *http.Request) (auth.Claims, int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "invalid order id")
		return auth.Claims{}, 0, false
	}

	claims, ok := auth.FromContext(r.Context())
	if !ok {
		json.WriteError(w, http.StatusUnauthorized, json.CodeUnauthorized, "unauthorized")
		return auth.Claims{}, 0, false
	}

	return claims, id, true
}

// writeOrderError maps the package's sentinel errors onto the API contract.
// Sentinels are safe to echo; anything else is logged and hidden behind a
// generic 500, since pgx errors embed connection detail.
func writeOrderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		json.WriteError(w, http.StatusNotFound, json.CodeNotFound, "order not found")
	case errors.Is(err, ErrForbidden):
		json.WriteError(w, http.StatusForbidden, json.CodeForbidden, err.Error())
	case errors.Is(err, ErrOutOfStock):
		json.WriteError(w, http.StatusConflict, json.CodeOutOfStock, err.Error())
	case errors.Is(err, ErrOrderNotPending):
		json.WriteError(w, http.StatusConflict, json.CodeOrderNotPending, err.Error())
	default:
		log.Println(err)
		json.WriteInternalError(w)
	}
}

// handler to get a single order's details; only the order's own customer may
// view it.
func (h *handler) GetOrder(w http.ResponseWriter, r *http.Request) {
	claims, id, ok := callerAndID(w, r)
	if !ok {
		return
	}

	order, err := h.service.GetOrder(r.Context(), id, claims)
	if err != nil {
		writeOrderError(w, err)
		return
	}

	json.Write(w, http.StatusOK, orderResponse{
		ID:              order.ID,
		ProductID:       order.ProductID,
		Status:          order.Status,
		TotalInCents:    order.TotalInCents,
		CreatedAt:       order.CreatedAt,
		ExpiresAt:       order.ExpiresAt,
		PaymentIntentID: order.StripePaymentIntentID.String,
	})
}

// handler to list the caller's own orders, newest first.
func (h *handler) ListOrders(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		json.WriteError(w, http.StatusUnauthorized, json.CodeUnauthorized, "unauthorized")
		return
	}

	limit, offset, err := parsePagination(r)
	if err != nil {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, err.Error())
		return
	}

	rows, err := h.service.ListOrders(r.Context(), claims, limit, offset)
	if err != nil {
		writeOrderError(w, err)
		return
	}

	// [] rather than null on an empty page, so a client can iterate blindly.
	out := make([]orderResponse, 0, len(rows))
	for _, o := range rows {
		out = append(out, orderResponse{
			ID:              o.ID,
			ProductID:       o.ProductID,
			ProductName:     o.ProductName,
			Status:          o.Status,
			TotalInCents:    o.TotalInCents,
			CreatedAt:       o.CreatedAt,
			ExpiresAt:       o.ExpiresAt,
			PaymentIntentID: o.StripePaymentIntentID.String,
		})
	}

	json.Write(w, http.StatusOK, out)
}

type createOrderRequest struct {
	ProductID int64 `json:"product_id"`
}

// handler for buyers to reserve stock on a product, creating a pending
// order. The buyer's identity comes from their verified bearer token, not
// the request body.
func (h *handler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := json.Decode(r, &req); err != nil {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "invalid request body")
		return
	}
	if req.ProductID <= 0 {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "product_id is required")
		return
	}

	claims, ok := auth.FromContext(r.Context())
	if !ok {
		json.WriteError(w, http.StatusUnauthorized, json.CodeUnauthorized, "unauthorized")
		return
	}

	order, err := h.service.CreateOrder(r.Context(), req.ProductID, claims)
	if err != nil {
		writeOrderError(w, err)
		return
	}

	json.Write(w, http.StatusCreated, fromOrder(order, ""))
}

// checkoutResponse hands the frontend what it needs to confirm the payment
// with Stripe.js. The order is NOT complete yet and its stock is still only
// reserved — a confirmed payment completes it asynchronously via the webhook.
type checkoutResponse struct {
	Order        orderResponse `json:"order"`
	ClientSecret string        `json:"client_secret"`
}

// handler for the synchronous half of payment: creates a PaymentIntent for a
// pending order and returns its client secret.
func (h *handler) Checkout(w http.ResponseWriter, r *http.Request) {
	claims, id, ok := callerAndID(w, r)
	if !ok {
		return
	}

	result, err := h.service.Checkout(r.Context(), id, claims)
	if err != nil {
		writeOrderError(w, err)
		return
	}

	json.Write(w, http.StatusOK, checkoutResponse{
		Order:        fromOrder(result.Order, ""),
		ClientSecret: result.ClientSecret,
	})
}

// handler for buyers to release a reservation they no longer want. Only
// 'pending' orders can be cancelled; see CancelOrder in the service.
func (h *handler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	claims, id, ok := callerAndID(w, r)
	if !ok {
		return
	}

	order, err := h.service.CancelOrder(r.Context(), id, claims)
	if err != nil {
		writeOrderError(w, err)
		return
	}

	json.Write(w, http.StatusOK, fromOrder(order, ""))
}
