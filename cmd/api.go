package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql"
	repo "github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql/sqlc"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/customers"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
	"github.com/jason-yusen-wu/doorbust/internal/payments"
	"github.com/jason-yusen-wu/doorbust/internal/products"
	"github.com/jason-yusen-wu/doorbust/internal/web"
	"github.com/stripe/stripe-go/v83"
	"golang.org/x/sync/errgroup"
)

func (app *application) mount() http.Handler {
	r := chi.NewRouter()

	// middleware
	r.Use(middleware.RequestID) // rate limiting & pass request in context
	r.Use(middleware.RealIP)    // rate limiting, analytics & tracing
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Cross-origin access is off unless an origin is named, which in practice
	// means it is off in production: the frontend is served from this same
	// process, so the browser never makes a cross-origin request. It exists for
	// `npm run dev`, where Vite serves the app from :5173 and every call to
	// :8080 is cross-origin.
	//
	// Closed by default on purpose, the same way COGNITO_VENDOR_GROUP fails
	// closed: a misconfiguration should deny access, never widen it.
	if len(app.config.corsAllowedOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: app.config.corsAllowedOrigins,
			AllowedMethods: []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions},
			AllowedHeaders: []string{"Authorization", "Content-Type"},
			// We authenticate with a bearer token, not a cookie. Allowing
			// credentials would also forbid a wildcard origin, and buys nothing.
			AllowCredentials: false,
			MaxAge:           300,
		}))
	}

	queries := repo.New(app.db)

	// endpoints
	//
	// /health is liveness: it proves the process started and bound the port,
	// nothing more. Deliberately left touching no dependency, because the
	// deploy's restart check relies on it answering even while the app is
	// still settling.
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("all good"))
	})

	productService := products.NewService(queries, app.db)
	productHandler := products.NewHandler(productService)
	r.Get("/products", productHandler.ListProducts)
	r.Get("/products/{id}", productHandler.GetProduct)

	// stripeClientOptions is empty in production; tests set it to point the
	// client at a stub server.
	gateway := payments.NewStripeGateway(
		app.config.stripe.secretKey,
		app.config.stripe.currency,
		app.stripeClientOptions...,
	)
	orderService := orders.NewService(queries, app.db, gateway, app.config.orders.reservationTTL)
	orderHandler := orders.NewHandler(orderService)

	customerService := customers.NewService(queries)
	customerHandler := customers.NewHandler(customerService, app.config.cognito.vendorGroup)

	// Stripe carries no Cognito token, so this route is public; the webhook
	// signature is its authentication.
	paymentHandler := payments.NewHandler(queries, app.config.stripe.webhookSecret)
	r.Post("/webhooks/stripe", paymentHandler.HandleWebhook)

	// writes require a verified Cognito caller
	r.Group(func(r chi.Router) {
		r.Use(app.auth.Middleware)
		r.Get("/me", customerHandler.GetMe)

		// Creating a sale is a vendor action, not something any signed-up
		// buyer may do. RequireGroup layers on top of the group's auth
		// middleware, so the caller is already verified by the time it runs.
		r.With(auth.RequireGroup(app.config.cognito.vendorGroup)).
			Post("/products", productHandler.CreateProduct)
		r.Get("/orders", orderHandler.ListOrders)
		r.Get("/orders/{id}", orderHandler.GetOrder)
		r.Post("/orders", orderHandler.CreateOrder)
		r.Post("/orders/{id}/checkout", orderHandler.Checkout)
		r.Delete("/orders/{id}", orderHandler.CancelOrder)
	})

	// Background halves, both cancelled by the same shutdown context as the
	// server. They are separate jobs on purpose: expiry is our own rule and
	// must keep releasing stock even if the payment processor never calls
	// back, so it does not ride on the Stripe worker.
	app.background = []backgroundJob{
		{
			name: "stripe-worker",
			run: payments.NewWorker(
				queries, app.db, orderService,
				app.config.payments.pollInterval,
				app.config.payments.maxAttempts,
			).Run,
		},
		{
			name: "reservation-sweeper",
			run: orders.NewSweeper(
				orderService,
				app.config.orders.sweepInterval,
				app.config.orders.sweepBatchSize,
			).Run,
		},
	}

	// The poller is how Stripe events actually reach the deployed box: it has
	// no Elastic IP, no domain, no TLS, and a security group admitting one
	// /32, so nothing can be pushed to it. Pulling needs none of that.
	//
	// Disabled by setting the interval to zero, which is what a local run
	// using `stripe listen` against /webhooks/stripe wants.
	var poller *payments.Poller
	if app.config.payments.eventPollInterval > 0 {
		poller = payments.NewPoller(
			queries, gateway.Events(),
			app.config.payments.eventPollInterval,
			app.config.payments.eventPollOverlap,
			app.config.payments.eventInitialLookback,
		)
		app.background = append(app.background, backgroundJob{
			name: "stripe-poller",
			run:  poller.Run,
		})
	} else {
		slog.Warn("stripe event poller disabled; events must arrive via the webhook endpoint")
	}

	// Readiness, unlike /health, actually touches the dependencies. Registered
	// after the poller exists so it can report on it.
	r.Get("/health/ready", app.readyHandler(poller))

	// The storefront, last: chi matches specific patterns before "/*", so every
	// route above still wins. Registered as GET only, so a POST to an unknown
	// path is still a 405 rather than a page.
	r.Get("/*", web.Handler(app.config.webDistDir).ServeHTTP)

	return r
}

// run starts the HTTP server and every background job, and blocks until the
// process is asked to stop.
//
// SIGTERM is what both `systemctl restart doorbust` and `docker stop` send,
// so without this the deploy killed in-flight requests and could interrupt a
// worker mid-transaction. Nothing is lost when that happens — every write is
// a guarded UPDATE and unprocessed events are re-claimed — but a request the
// buyer already sent should not simply vanish.
func (app *application) run(h http.Handler) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:         app.config.addr,
		Handler:      h,
		WriteTimeout: time.Second * 30,
		ReadTimeout:  time.Second * 10,
		IdleTimeout:  time.Minute,
	}

	g, gctx := errgroup.WithContext(ctx)

	for _, job := range app.background {
		g.Go(func() error {
			if err := job.run(gctx); err != nil {
				return err
			}
			slog.Info("background job finished", "job", job.name)
			return nil
		})
	}

	g.Go(func() error {
		slog.Info("server has started", "addr", app.config.addr)
		// A clean Shutdown returns this; it is not a failure.
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	g.Go(func() error {
		<-gctx.Done()
		slog.Info("shutting down", "timeout", app.config.shutdownTimeout)

		// Deliberately not derived from gctx: that context is already
		// cancelled, and Shutdown needs a live deadline to drain in-flight
		// requests rather than returning immediately.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), app.config.shutdownTimeout)
		defer cancel()

		return srv.Shutdown(shutdownCtx)
	})

	if err := g.Wait(); err != nil {
		return err
	}

	slog.Info("shutdown complete")
	return nil
}

// backgroundJob is a long-running loop that stops when its context is
// cancelled.
type backgroundJob struct {
	name string
	run  func(context.Context) error
}

// global struct
type application struct {
	config     config
	db         *pgxpool.Pool // shared, concurrency-safe connection pool
	auth       *auth.Verifier
	background []backgroundJob

	// stripeClientOptions is empty in production. Tests set it so mount()
	// builds a gateway pointed at a stub server instead of live Stripe,
	// which is what makes the checkout path testable through the real router.
	stripeClientOptions []stripe.ClientOption
}

type config struct {
	addr            string // network address where server is hosted
	shutdownTimeout time.Duration
	db              postgresql.Config
	cognito         cognitoConfig
	stripe          stripeConfig
	orders          ordersConfig
	payments        paymentsConfig

	// corsAllowedOrigins is empty in production, where the frontend is served
	// from this process and nothing is cross-origin. Empty disables CORS.
	corsAllowedOrigins []string
	// webDistDir holds the built frontend. Missing is fine — the API serves
	// without it.
	webDistDir string
}

type cognitoConfig struct {
	issuerURL string
	clientID  string
	// vendorGroup is the Cognito group whose members may create products.
	// Configurable so renaming the group in Cognito needs no code change.
	vendorGroup string
}

type stripeConfig struct {
	secretKey     string
	webhookSecret string
	currency      string
}

type ordersConfig struct {
	// reservationTTL bounds how long an unpaid order may hold stock.
	reservationTTL time.Duration
	sweepInterval  time.Duration
	sweepBatchSize int32
}

type paymentsConfig struct {
	// pollInterval is how often the worker drains the inbox.
	pollInterval time.Duration
	// maxAttempts stops an event that can never succeed from being re-claimed
	// on every poll forever.
	maxAttempts int32

	// eventPollInterval is how often the poller fetches from Stripe. Zero
	// disables the poller entirely, for local runs that use `stripe listen`
	// to push to the webhook endpoint instead.
	eventPollInterval time.Duration
	// eventPollOverlap re-scans a window behind the cursor. Not optional —
	// see Poller.sinceFor.
	eventPollOverlap time.Duration
	// eventInitialLookback bounds the first poll against an empty inbox.
	eventInitialLookback time.Duration
}
