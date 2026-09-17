package run

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jason-yusen-wu/doorbust/bench/internal/report"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
)

// The analytic model this project predicted before measuring anything:
//
//	T_lock  = (client round trips inside the critical section) x RTT
//	        + server execution time
//	        + commit cost
//	ceiling_per_SKU ~= 1 / T_lock
//
// Measuring the model's own inputs in the same run as the thing it predicts is
// what makes it falsifiable. A prediction confirmed by its own run is a far
// stronger claim than a throughput number with no theory attached — and a
// mismatch is the more interesting outcome, because it means the bottleneck is
// somewhere the model does not know about. Either way it is one experiment, not
// two.

// rttsInLock is each arm's defining property: how many client round trips the
// stock row's lock is held across.
func rttsInLock(arm string) int {
	switch arm {
	case orders.StrategyBaseline:
		return 2 // ReserveStock -> CreateOrder -> COMMIT
	case orders.StrategyReserveLast:
		return 1 // CreateOrder -> ReserveStock -> COMMIT
	case orders.StrategyCTE:
		return 0 // one statement, no client network inside the lock at all
	default:
		return -1
	}
}

// ProbeModel measures RTT and commit cost against the same database the run
// will use, then predicts the ceiling.
//
// Deliberately measured rather than assumed. The project's own notes estimate
// loopback RTT at 0.05ms, but Docker Desktop on macOS has no host networking:
// a published port goes through a userspace proxy into a Linux VM, so the real
// figure is more like 0.2-0.5ms and jittery. Assuming the wrong number would
// produce a prediction that "fails" for reasons that have nothing to do with
// the code under test.
func ProbeModel(ctx context.Context, pool *pgxpool.Pool, arm string, execMicros float64) report.Model {
	m := report.Model{
		RTTMicros:        medianMicros(ctx, pool, 500, probeRTT),
		CommitCostMicros: 0,
		ExecMicros:       execMicros,
		ClientRTTsInLock: rttsInLock(arm),
	}

	// Commit cost is the round trip plus the durability work, so the round trip
	// has to come back out. A negative result means the two probes disagreed by
	// less than their own noise, which is itself the finding that commit is
	// effectively free — as it should be with synchronous_commit=off.
	commitTotal := medianMicros(ctx, pool, 200, probeCommit)
	if c := commitTotal - m.RTTMicros; c > 0 {
		m.CommitCostMicros = c
	}

	lockMicros := float64(m.ClientRTTsInLock)*m.RTTMicros + m.ExecMicros + m.CommitCostMicros
	if lockMicros > 0 {
		m.PredictedCeiling = 1_000_000 / lockMicros
	}
	return m
}

func probeRTT(ctx context.Context, pool *pgxpool.Pool) time.Duration {
	start := time.Now()
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return 0
	}
	return time.Since(start)
}

// probeCommit times a trivial write transaction: one round trip's worth of
// network plus whatever the server does to make a commit durable.
func probeCommit(ctx context.Context, pool *pgxpool.Pool) time.Duration {
	start := time.Now()
	if _, err := pool.Exec(ctx, `
		INSERT INTO bench_commit_probe (n) VALUES (1)`); err != nil {
		return 0
	}
	return time.Since(start)
}

// EnsureProbeTable creates the scratch table probeCommit writes to. Unlogged,
// so the probe measures commit machinery without also measuring WAL for a
// table nobody will ever read.
func EnsureProbeTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx,
		`CREATE UNLOGGED TABLE IF NOT EXISTS bench_commit_probe (n int)`)
	return err
}

func medianMicros(ctx context.Context, pool *pgxpool.Pool, n int, probe func(context.Context, *pgxpool.Pool) time.Duration) float64 {
	samples := make([]float64, 0, n)
	for range n {
		if d := probe(ctx, pool); d > 0 {
			samples = append(samples, float64(d.Nanoseconds())/1000.0)
		}
	}
	if len(samples) == 0 {
		return 0
	}
	// Median, not mean: one scheduler hiccup should not move the figure the
	// whole prediction is built on.
	slices.Sort(samples)
	return samples[len(samples)/2]
}
