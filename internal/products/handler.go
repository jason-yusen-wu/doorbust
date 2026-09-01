package products

import (
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

// productResponse is the wire shape of a product. The generated repo rows
// can't carry a computed field, so the handler layer owns the mapping and
// adds Available — otherwise every client has to re-derive stock math, and
// they'd all have to agree on it.
type productResponse struct {
	ID           int64              `json:"id"`
	Name         string             `json:"name"`
	PriceInCents int32              `json:"price_in_cents"`
	CreatedAt    pgtype.Timestamptz `json:"created_at"`
	StartAt      pgtype.Timestamptz `json:"start_at"`
	Quantity     int32              `json:"quantity"`
	NumReserved  int32              `json:"num_reserved"`
	Available    int32              `json:"available"`
}

func newProductResponse(id int64, name string, priceInCents int32, createdAt, startAt pgtype.Timestamptz, quantity, numReserved int32) productResponse {
	return productResponse{
		ID:           id,
		Name:         name,
		PriceInCents: priceInCents,
		CreatedAt:    createdAt,
		StartAt:      startAt,
		Quantity:     quantity,
		NumReserved:  numReserved,
		Available:    quantity - numReserved,
	}
}

// handler to list current and upcoming sales, paginated via ?limit=&offset=
func (h *handler) ListProducts(w http.ResponseWriter, r *http.Request) {
	limit := int32(defaultListLimit)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "invalid limit")
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
			json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "invalid offset")
			return
		}
		offset = int32(n)
	}

	rows, err := h.service.ListProducts(r.Context(), limit, offset)
	if err != nil {
		log.Println(err)
		json.WriteInternalError(w)
		return
	}

	// Always emit [] rather than null on an empty page — a client iterating
	// the result shouldn't have to special-case it.
	out := make([]productResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, newProductResponse(p.ID, p.Name, p.PriceInCents, p.CreatedAt, p.StartAt, p.Quantity, p.NumReserved))
	}

	json.Write(w, http.StatusOK, out)
}

// handler to get a single product's details
func (h *handler) GetProduct(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "invalid product id")
		return
	}

	p, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			json.WriteError(w, http.StatusNotFound, json.CodeNotFound, "product not found")
			return
		}
		log.Println(err)
		json.WriteInternalError(w)
		return
	}

	json.Write(w, http.StatusOK, newProductResponse(p.ID, p.Name, p.PriceInCents, p.CreatedAt, p.StartAt, p.Quantity, p.NumReserved))
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
	if err := json.Decode(r, &req); err != nil {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.PriceInCents < 0 || req.Quantity < 0 {
		json.WriteError(w, http.StatusBadRequest, json.CodeInvalidRequest, "name, non-negative price_in_cents and quantity are required")
		return
	}
	if req.StartAt.IsZero() {
		req.StartAt = time.Now()
	}

	p, err := h.service.CreateProduct(r.Context(), CreateProductParams{
		Name:         req.Name,
		PriceInCents: req.PriceInCents,
		StartAt:      pgtype.Timestamptz{Time: req.StartAt, Valid: true},
		Quantity:     req.Quantity,
	})
	if err != nil {
		log.Println(err)
		json.WriteInternalError(w)
		return
	}

	json.Write(w, http.StatusCreated, newProductResponse(p.ID, p.Name, p.PriceInCents, p.CreatedAt, p.StartAt, p.Quantity, p.NumReserved))
}
