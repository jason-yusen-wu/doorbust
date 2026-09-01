package orders

import (
	"context"
	"log/slog"
	"time"
)

// Sweeper releases reservations whose orders were never paid for.
//
// It is deliberately independent of the payment processor. Expiry is our
// rule, driven by our clock against our own table: if Stripe is down, never
// calls back, or is swapped out entirely, stock must still come back on sale.
// The only thing it shares with the webhook worker is the shutdown context.
//
// It also touches none of the contended reservation path — ReserveStock's
// guard is unchanged — so the contention strategy stays free to be replaced
// after profiling without revisiting expiry.
type Sweeper struct {
	service   Service
	interval  time.Duration
	batchSize int32
}

func NewSweeper(service Service, interval time.Duration, batchSize int32) *Sweeper {
	return &Sweeper{
		service:   service,
		interval:  interval,
		batchSize: batchSize,
	}
}

// Run blocks until ctx is cancelled. Errors are logged and retried on the
// next tick rather than returned: a transient database blip should not take
// the process down, and the work is idempotent so nothing is lost by waiting.
func (s *Sweeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	slog.Info("reservation sweeper started", "interval", s.interval, "batchSize", s.batchSize)

	for {
		select {
		case <-ctx.Done():
			slog.Info("reservation sweeper stopped")
			return nil
		case <-ticker.C:
			s.sweepAll(ctx)
		}
	}
}

// sweepAll drains the backlog in batches instead of releasing at most
// batchSize per interval — after an outage there may be far more expired
// reservations than one batch holds, and stock should not trickle back.
func (s *Sweeper) sweepAll(ctx context.Context) {
	for {
		released, err := s.service.SweepExpired(ctx, s.batchSize)
		if err != nil {
			// Don't log a cancelled context as a failure; that's shutdown.
			if ctx.Err() == nil {
				slog.Error("reservation sweep failed", "error", err)
			}
			return
		}
		if released > 0 {
			slog.Info("released expired reservations", "count", released)
		}
		// A short batch means the backlog is drained.
		if int32(released) < s.batchSize {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}
