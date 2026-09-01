package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jason-yusen-wu/doorbust/internal/adapters/postgresql"
	"github.com/jason-yusen-wu/doorbust/internal/auth"
	"github.com/jason-yusen-wu/doorbust/internal/env"
)

func main() {
	// create Top-level Context
	ctx := context.Background()
	cfg := config{
		addr:            ":8080",
		shutdownTimeout: env.GetDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		db: postgresql.Config{
			DSN:             env.MustGetString("GOOSE_DBSTRING"),
			MaxConns:        int32(env.GetInt("DB_MAX_CONNS", 25)),
			MinConns:        int32(env.GetInt("DB_MIN_CONNS", 5)),
			MinIdleConns:    int32(env.GetInt("DB_MIN_IDLE_CONNS", 5)),
			MaxConnLifetime: env.GetDuration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: env.GetDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
		},
		cognito: cognitoConfig{
			issuerURL: env.MustGetString("COGNITO_ISSUER_URL"),
			clientID:  env.MustGetString("COGNITO_CLIENT_ID"),
			// Has a safe default because the failure mode is safe: if this
			// does not match a real Cognito group, nobody is a vendor and
			// POST /products is closed to everyone.
			vendorGroup: env.GetString("COGNITO_VENDOR_GROUP", "vendors"),
		},
		stripe: stripeConfig{
			// Required, like the DSN: a missing key would otherwise surface
			// as every checkout failing at runtime instead of at boot.
			secretKey:     env.MustGetString("STRIPE_SECRET_KEY"),
			webhookSecret: env.MustGetString("STRIPE_WEBHOOK_SECRET"),
			currency:      env.GetString("STRIPE_CURRENCY", "usd"),
		},
		orders: ordersConfig{
			reservationTTL: env.GetDuration("RESERVATION_TTL", 15*time.Minute),
			sweepInterval:  env.GetDuration("RESERVATION_SWEEP_INTERVAL", time.Minute),
			sweepBatchSize: int32(env.GetInt("RESERVATION_SWEEP_BATCH", 100)),
		},
		payments: paymentsConfig{
			pollInterval: env.GetDuration("STRIPE_POLL_INTERVAL", 5*time.Second),
			maxAttempts:  int32(env.GetInt("STRIPE_MAX_ATTEMPTS", 5)),

			// Set STRIPE_EVENT_POLL_INTERVAL=0 to disable pulling and rely on
			// the webhook endpoint instead (what `stripe listen` does).
			eventPollInterval: env.GetDuration("STRIPE_EVENT_POLL_INTERVAL", 10*time.Second),
			// Generous on purpose: the cost of re-scanning is zero (the
			// insert dedups on Stripe's event id) and the cost of a window
			// that is too small is a permanently skipped event.
			eventPollOverlap:     env.GetDuration("STRIPE_EVENT_POLL_OVERLAP", 2*time.Minute),
			eventInitialLookback: env.GetDuration("STRIPE_EVENT_INITIAL_LOOKBACK", time.Hour),
		},

		// Unset in production: the frontend is served by this process, so
		// nothing is cross-origin. Set it to the Vite dev server's origin
		// (http://localhost:5173) when running the two separately.
		corsAllowedOrigins: env.GetStringSlice("CORS_ALLOWED_ORIGINS"),
		// Where `npm run build` leaves the bundle. The container image sets
		// this to /srv/web; a missing directory just means no frontend.
		webDistDir: env.GetString("WEB_DIST_DIR", "./web/dist"),
	}

	// structured (text based) logger as global logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// connect to DB
	pool, err := postgresql.NewPool(ctx, cfg.db)
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	logger.Info("connected to database", "host", pool.Config().ConnConfig.Host, "database", pool.Config().ConnConfig.Database, "maxConns", cfg.db.MaxConns)

	// verify the Cognito issuer/JWKS are reachable now, not on the first request
	authVerifier, err := auth.NewVerifier(ctx, cfg.cognito.issuerURL, cfg.cognito.clientID)
	if err != nil {
		panic(err)
	}

	logger.Info("cognito verifier ready", "issuer", cfg.cognito.issuerURL)

	api := application{
		config: cfg,
		db:     pool,
		auth:   authVerifier,
	}

	if err := api.run(api.mount()); err != nil {
		slog.Error("Server has failed to start", "error", err)
		os.Exit(1)
	}
}
