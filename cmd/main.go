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
		addr: ":8080",
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
		},
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
