package orders

import (
	stdjson "encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/json"
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

// handler to get a single order's details; only the order's own customer may
// view it.
func (h *handler) GetOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid order id", http.StatusBadRequest)
		return
	}

	claims, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	order, err := h.service.GetOrder(r.Context(), id, claims.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "order not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrForbidden) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		log.Println(err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, order)
}

type createOrderRequest struct {
	ProductID int64 `json:"product_id"`
}

// handler for buyers to reserve stock on a product, creating a pending
// order. The buyer's identity comes from their verified bearer token, not
// the request body.
func (h *handler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := stdjson.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ProductID <= 0 {
		http.Error(w, "product_id is required", http.StatusBadRequest)
		return
	}

	claims, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	order, err := h.service.CreateOrder(r.Context(), req.ProductID, claims.Email)
	if err != nil {
		if errors.Is(err, ErrOutOfStock) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Println(err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusCreated, order)
}

// handler standing in for a real payment gateway: finalizes a pending order.
// Only the order's own customer may check it out.
func (h *handler) Checkout(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid order id", http.StatusBadRequest)
		return
	}

	claims, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	order, err := h.service.Checkout(r.Context(), id, claims.Email)
	if err != nil {
		if errors.Is(err, ErrOrderNotPending) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if errors.Is(err, ErrForbidden) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "order not found", http.StatusNotFound)
			return
		}
		log.Println(err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, order)
}
