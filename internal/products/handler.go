package products

import (
	"log"
	"net/http"

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

// handler to list products
func (h *handler) ListProducts(w http.ResponseWriter, r *http.Request) {
	// 1. call service -> ListProduct
	err := h.service.ListProducts(r.Context())
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 2. return JSON in HTTP response
	products := struct {
		Products []string `json:"products"`
	}{}

	json.Write(w, http.StatusOK, products)
}
