// Package postgresql owns the Postgres connection adapter. It hands the rest
// of the app a *pgxpool.Pool, which is safe for concurrent use and satisfies
// both repo.DBTX (for direct queries) and repo.Beginner (for transactions).
package postgresql

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config describes the connection pool. A zero value for any field other than
// DSN falls back to the pgx default for that setting.
type Config struct {
	DSN string

	// MaxConns caps concurrent connections. Under flash-sale load this is the
	// real backpressure valve: requests past it queue for a connection rather
	// than piling more work onto Postgres.
	MaxConns int32

	// MinConns is the floor the health check restores the pool to.
	MinConns int32

	// MinIdleConns keeps spare connections ready so a traffic spike doesn't
	// pay connection-establishment latency on the hot path.
	MinIdleConns int32

	// MaxConnLifetime recycles connections so they don't outlive a failover or
	// accumulate server-side state.
	MaxConnLifetime time.Duration

	// MaxConnIdleTime reaps connections left over after a spike subsides.
	MaxConnIdleTime time.Duration
}

// NewPool parses cfg and returns a live pool. pgxpool.New connects lazily, so
// this pings once to fail fast on a bad DSN or an unreachable database.
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, err
	}

	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MinIdleConns > 0 {
		poolCfg.MinIdleConns = cfg.MinIdleConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}
