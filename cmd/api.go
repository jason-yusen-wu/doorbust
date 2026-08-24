package main

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/jason-yusen-wu/doorbust/internal/products"
)

func (app *application) mount() http.Handler {
	r := chi.NewRouter()

	// middleware
	r.Use(middleware.RequestID) // rate limiting & pass request in context
	r.Use(middleware.RealIP)    // rate limiting, analytics & tracing
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// endpoints
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("all good"))
	})

	productService := products.NewService(repo.New(app.db), app.db)
	productHandler := products.NewHandler(productService)
	r.Get("/products", productHandler.ListProducts)
	r.Get("/products/{id}", productHandler.GetProduct)

	orderService := orders.NewService(repo.New(app.db), app.db)
	orderHandler := orders.NewHandler(orderService)

	// writes require a verified Cognito caller
	r.Group(func(r chi.Router) {
		r.Use(app.auth.Middleware)
		r.Post("/products", productHandler.CreateProduct)
		r.Get("/orders/{id}", orderHandler.GetOrder)
		r.Post("/orders", orderHandler.CreateOrder)
		r.Post("/orders/{id}/checkout", orderHandler.Checkout)
	})

	return r
}

func (app *application) run(h http.Handler) error {
	srv := &http.Server{
		Addr:         app.config.addr,
		Handler:      h,
		WriteTimeout: time.Second * 30,
		ReadTimeout:  time.Second * 10,
		IdleTimeout:  time.Minute,
	}

	log.Printf("Server has started at addr %s", app.config.addr)
	// TODO: graceful shutdown script

	return srv.ListenAndServe()
}

// global struct
type application struct {
	config config
	db     *pgxpool.Pool // shared, concurrency-safe connection pool
	auth   *auth.Verifier
}

type config struct {
	addr    string // network address where server is hosted
	db      postgresql.Config
	cognito cognitoConfig
}

type cognitoConfig struct {
	issuerURL string
	clientID  string
}
