// Package run orchestrates one benchmark run end to end: it owns the app
// process, the database state, the fake identity provider and the generator.
//
// Bench starting the app as a child, rather than expecting someone to have one
// running, is what makes a result self-describing. Bench SETS the environment,
// so it can RECORD the environment — nobody has to keep a note that stays
// accurate across a dozen arms.
//
// The app runs out of process on purpose. Booting it inside the generator would
// put both in one Go runtime, so a generator GC pause would land directly in
// the server's p99.9 — the exact number under test — and GOMAXPROCS would
// become one shared knob when the whole Tier-1 method depends on splitting CPU
// deliberately between the two. Out of process also exercises the real chi
// middleware chain, HTTP parsing and TCP, which is what a buyer actually hits.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jason-yusen-wu/doorbust/bench/internal/loadgen"
	"github.com/jason-yusen-wu/doorbust/bench/internal/report"
	"github.com/jason-yusen-wu/doorbust/bench/internal/verify"
	"github.com/jason-yusen-wu/doorbust/bench/internal/workload"
)

type Options struct {
	DSN string
	// AllowUnsafeTarget skips the check that the DSN names a throwaway
	// benchmark database. bench TRUNCATEs every table it is pointed at.
	AllowUnsafeTarget bool
	MigrationsDir     string
	AppBinary         string
	Addr              string

	Arm           string
	CustomerCache bool
	LogRequests   bool
	DBMaxConns    int

	Shape    string
	Arrivals string
	Rate     float64
	Duration time.Duration
	Warmup   time.Duration
	Buyers   int

	MaxInFlight int
	Timeout     time.Duration

	// GitSHA identifies the commit this binary was built from, for runs that
	// happen away from a checkout. Empty means "read it from the working tree".
	GitSHA        string
	Tier          report.Tier
	SessionID     string
	AppGOMAXPROCS int
	AllowDirty    bool
	Seed          uint64
	AppLog        io.Writer
}

// Execute performs a whole run and returns the result. A result is returned
// even when the run is void, because "we ran it and the numbers are unusable"
// is a finding that belongs on the record rather than in a lost terminal.
func Execute(ctx context.Context, opts Options) (*report.Result, error) {
	res := report.New(opts.Tier)
	res.Meta = report.Collect(opts.SessionID, opts.AppGOMAXPROCS)

	// A run on the deployed box executes a shipped binary, where there is no
	// checkout to interrogate — but the SHA is still known, because whoever
	// built the binary knew it. Supplying it is not a way around the
	// attribution rule; it is how attribution works when the code and the
	// repository are on different machines.
	if opts.GitSHA != "" {
		res.Meta.GitSHA = opts.GitSHA
		res.Meta.GitDirty = false
	}

	// A result that cannot be attributed to a commit is worse than no result:
	// six months later nobody can tell which code produced it.
	if res.Meta.GitSHA == "" && !opts.AllowDirty {
		return nil, errors.New(
			"could not read the git SHA, so this run could not be attributed to a commit.\n" +
				"On macOS this is usually the Xcode licence: run `sudo xcodebuild -license accept`.\n" +
				"Pass -allow-dirty to run anyway.")
	}
	if res.Meta.GitDirty && !opts.AllowDirty {
		return nil, errors.New("working tree is dirty; commit first or pass -allow-dirty")
	}

	// The generator needs a socket per in-flight request, and macOS ships a
	// soft limit of 256. Refusing up front beats a run that dissolves into
	// connection errors halfway through and looks like the server failing.
	soft, hard, err := loadgen.RaiseFileLimit()
	if err == nil {
		res.Meta.RLimitNofile = soft
		if need := uint64(opts.MaxInFlight * 2); hard < need {
			return nil, fmt.Errorf(
				"file descriptor hard limit is %d but this run needs about %d; lower -max-inflight or raise the limit",
				hard, need)
		}
	}

	// Before anything else, and before opening a connection: bench destroys the
	// contents of whatever it is pointed at.
	if err := workload.CheckTarget(opts.DSN, opts.AllowUnsafeTarget); err != nil {
		return nil, err
	}

	pool, err := pgxpool.New(ctx, opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("connect to bench database: %w", err)
	}
	defer pool.Close()

	if err := workload.Migrate(ctx, opts.DSN, opts.MigrationsDir); err != nil {
		return nil, err
	}
	if err := workload.Reset(ctx, pool); err != nil {
		return nil, err
	}
	if err := EnsureProbeTable(ctx, pool); err != nil {
		return nil, fmt.Errorf("create probe table: %w", err)
	}

	res.DB = report.ReadDBInfo(ctx, pool)
	// Not fatal, but it decides whether the arms can be told apart at all: the
	// stock row's lock releases at commit, and with synchronous_commit=on that
	// includes an fsync which is identical for every arm and an order of
	// magnitude larger than the round trips they differ on.
	if res.DB.SynchronousCommit == "on" {
		res.Note += " Durability control run: synchronous_commit=on, so commit cost dominates and arms are expected to converge."
	}

	schedule, err := loadgen.BuildSchedule(opts.Arrivals, opts.Rate, opts.Duration, rand.New(rand.NewPCG(opts.Seed, 0)))
	if err != nil {
		return nil, err
	}

	plan, err := workload.PlanFor(opts.Shape, len(schedule))
	if err != nil {
		return nil, err
	}

	productIDs, err := workload.SeedProducts(ctx, pool, plan)
	if err != nil {
		return nil, err
	}

	issuer, err := workload.StartIssuer()
	if err != nil {
		return nil, err
	}
	defer issuer.Close()

	// Tokens outlive any plausible run, so none expires mid-run and turns into
	// a burst of 401s that would look like an auth bug.
	buyers, err := workload.MintBuyers(issuer, opts.Buyers, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	if err := workload.SeedCustomers(ctx, pool, buyers); err != nil {
		return nil, err
	}

	app, err := startApp(ctx, opts, issuer.URL())
	if err != nil {
		return nil, err
	}
	defer app.stop()

	baseURL := "http://127.0.0.1" + opts.Addr
	if err := waitReady(ctx, baseURL, 30*time.Second); err != nil {
		return nil, err
	}

	// Exec time needs a real product and customer to aim at, and the probe
	// rolls back so it consumes neither.
	var probeCustomer int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM customers ORDER BY id LIMIT 1`).Scan(&probeCustomer); err != nil {
		return nil, fmt.Errorf("find a seeded customer for the exec probe: %w", err)
	}
	execMicros, err := ProbeExec(ctx, pool, opts.Arm, productIDs[0], probeCustomer)
	if err != nil {
		return nil, fmt.Errorf("probe exec time: %w", err)
	}

	res.Model = ProbeModel(ctx, pool, opts.Arm, execMicros)

	out, err := loadgen.Run(ctx, loadgen.Config{
		BaseURL:     baseURL,
		Schedule:    schedule,
		Warmup:      opts.Warmup,
		Buyers:      buyers,
		ProductIDs:  productIDs,
		Plan:        plan,
		MaxInFlight: opts.MaxInFlight,
		Timeout:     opts.Timeout,
		Client:      loadgen.NewClient(opts.MaxInFlight, opts.Timeout),
		Rand:        rand.New(rand.NewPCG(opts.Seed, 1)),
	})
	if err != nil {
		return nil, err
	}

	// Stop the app before reading the database. Graceful shutdown is already
	// implemented, so in-flight requests drain rather than vanishing — reading
	// while requests were still landing would race the invariants against the
	// very writes they are checking.
	app.stop()

	res.App = report.AppInfo{
		Arm:                     opts.Arm,
		CustomerCache:           opts.CustomerCache,
		LogRequests:             opts.LogRequests,
		DBMaxConns:              opts.DBMaxConns,
		ReservationTTL:          "24h",
		SweepInterval:           "24h",
		StripeEventPollInterval: "0s",
	}
	res.Workload = report.Workload{
		Shape:           plan.Name,
		Arrivals:        opts.Arrivals,
		TargetRate:      opts.Rate,
		SKUs:            plan.SKUs,
		InitialQuantity: plan.Quantity,
		Buyers:          opts.Buyers,
		DurationSeconds: opts.Duration.Seconds(),
		WarmupSeconds:   opts.Warmup.Seconds(),
	}
	res.Schedule = out.Schedule
	res.Latency = out.Latency
	res.Outcomes = out.Outcomes
	res.Through = out.Throughput
	res.Through.OfferedPerSec = opts.Rate
	res.Invariants = verify.Check(ctx, pool, productIDs, plan.Quantity, out.ReservedTotal)

	applyValidity(res)
	return res, nil
}

// applyValidity is where a run is judged. Every condition here means the
// numbers describe the generator, the harness or a bug rather than the server,
// so each one voids the run outright instead of being noted as a caveat.
func applyValidity(res *report.Result) {
	if res.Schedule.GeneratorSaturated > 0 {
		res.Void("generator saturated on %d requests: they were never sent, so the denominator is wrong",
			res.Schedule.GeneratorSaturated)
	}
	if n := res.Outcomes["connect_error"]; n > 0 {
		res.Void("%d connection errors: the generator hit a socket or port limit", n)
	}
	if res.Schedule.LagP99Micros > 1000 {
		res.Void("p99 schedule lag %.0fus exceeds 1ms: the generator could not keep to its own schedule",
			res.Schedule.LagP99Micros)
	}
	// The question a lag rule should answer is "could generator delay have
	// moved the numbers being reported", and a raw maximum answers a different
	// one. A single GC pause in the generator makes one request late out of
	// tens of thousands; that cannot move p50, p95 or p99, and the throughput
	// figure is unaffected because the request was still sent and counted.
	//
	// So the rule is on the share of late sends, set below the resolution of
	// the finest percentile reported (p99.9). Above that, generator delay could
	// be showing up as server latency and the run is not trustworthy.
	if res.Schedule.LateFraction > 0.001 {
		res.Void("%d of %d sends (%.2f%%) missed their deadline by over 1ms, enough to move the reported tail",
			res.Schedule.LateRequests, res.Schedule.Requests, res.Schedule.LateFraction*100)
	}
	if n := res.Outcomes["server_error"]; n > 0 {
		res.Void("%d server errors: the run hit a real failure, not a load limit", n)
	}
	if n := res.Outcomes["unauthorized"]; n > 0 {
		res.Void("%d unauthorized responses: a harness problem, not a result", n)
	}
	if n := res.Outcomes["not_found"]; n > 0 {
		res.Void("%d not-found responses: the generator targeted a product that does not exist", n)
	}
	if !res.Invariants.Passed {
		res.Void("inventory invariants failed after the run")
	}
	if res.Meta.GitDirty {
		res.Void("built from a dirty working tree, so the result cannot be attributed to a commit")
	}
}

type appProcess struct {
	cmd     *exec.Cmd
	stopped bool
}

func startApp(ctx context.Context, opts Options, issuerURL string) (*appProcess, error) {
	cmd := exec.Command(opts.AppBinary)
	cmd.Env = append(os.Environ(),
		"ADDR="+opts.Addr,
		"GOOSE_DBSTRING="+opts.DSN,
		"COGNITO_ISSUER_URL="+issuerURL,
		"COGNITO_CLIENT_ID="+workload.Audience,
		"RESERVE_STRATEGY="+opts.Arm,
		"CUSTOMER_CACHE="+strconv.FormatBool(opts.CustomerCache),
		"LOG_REQUESTS="+strconv.FormatBool(opts.LogRequests),
		"DB_MAX_CONNS="+strconv.Itoa(opts.DBMaxConns),
		"GOMAXPROCS="+strconv.Itoa(opts.AppGOMAXPROCS),

		// The reservation sweeper must not touch the run. It releases
		// reservations past their TTL, which would move num_reserved under the
		// invariants and make them fail for a reason that is not a bug.
		"RESERVATION_TTL=24h",
		"RESERVATION_SWEEP_INTERVAL=24h",

		// No Stripe. The poller would otherwise call a live API with a
		// placeholder key every tick, on the machine being measured.
		"STRIPE_EVENT_POLL_INTERVAL=0s",
		"STRIPE_SECRET_KEY=sk_test_bench_placeholder",
		"STRIPE_WEBHOOK_SECRET=whsec_bench_placeholder",

		// No frontend: a missing directory just means the API serves without
		// one, and the bench never asks for a page.
		"WEB_DIST_DIR=/nonexistent-bench",
	)

	if opts.AppLog != nil {
		cmd.Stdout = opts.AppLog
		cmd.Stderr = opts.AppLog
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start app (%s): %w", opts.AppBinary, err)
	}
	return &appProcess{cmd: cmd}, nil
}

// stop asks the app to shut down the way a deploy does, and is safe to call
// twice so the deferred cleanup is a no-op after an explicit stop.
func (a *appProcess) stop() {
	if a == nil || a.stopped {
		return
	}
	a.stopped = true

	_ = a.cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan struct{})
	go func() { _, _ = a.cmd.Process.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = a.cmd.Process.Kill()
	}
}

// waitReady polls readiness, not liveness. /health answers a static string
// while the app is still settling; /health/ready touches the pool, which is
// what "can this actually serve a reserve" means.
func waitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/ready", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("app did not become ready at %s within %v", baseURL, timeout)
}
