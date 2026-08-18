package products

import (
	stdjson "encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

// handler to list current and upcoming sales, paginated via ?limit=&offset=
func (h *handler) ListProducts(w http.ResponseWriter, r *http.Request) {
	limit := int32(defaultListLimit)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = int32(n)
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	offset := int32(0)
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid offset", http.StatusBadRequest)
			return
		}
		offset = int32(n)
	}

	products, err := h.service.ListProducts(r.Context(), limit, offset)
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, products)
}

// handler to get a single product's details
func (h *handler) GetProduct(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid product id", http.StatusBadRequest)
		return
	}

	product, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "product not found", http.StatusNotFound)
			return
		}
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusOK, product)
}

type createProductRequest struct {
	Name         string    `json:"name"`
	PriceInCents int32     `json:"price_in_cents"`
	StartAt      time.Time `json:"start_at"`
	Quantity     int32     `json:"quantity"`
}

// handler for vendors to add a sale event to the storefront
func (h *handler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	var req createProductRequest
	if err := stdjson.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.PriceInCents < 0 || req.Quantity < 0 {
		http.Error(w, "name, non-negative price_in_cents and quantity are required", http.StatusBadRequest)
		return
	}
	if req.StartAt.IsZero() {
		req.StartAt = time.Now()
	}

	product, err := h.service.CreateProduct(r.Context(), CreateProductParams{
		Name:         req.Name,
		PriceInCents: req.PriceInCents,
		StartAt:      pgtype.Timestamptz{Time: req.StartAt, Valid: true},
		Quantity:     req.Quantity,
	})
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.Write(w, http.StatusCreated, product)
}
