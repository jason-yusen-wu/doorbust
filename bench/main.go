// Command bench is Doorbust's load generator.
//
// It exists because the README claimed the project's guarantees were "proven
// with real load-testing benchmarks" while the repository contained no
// benchmark, no profile and no measured number. This is the thing that makes
// the claim true or retracts it.
//
// Two subcommands:
//
//	bench run    one measured run, JSON to stdout, summary to stderr
//	bench sweep  every arm, plus the self-saturation check
//
// It is package main under bench/, with everything else under bench/internal/,
// so Go itself forbids cmd/ and internal/ from importing any of it. That is a
// stronger guarantee than a build tag, and unlike a build tag it keeps the code
// inside `go build ./...` and `go vet ./...` so it cannot rot unnoticed.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jason-yusen-wu/doorbust/bench/internal/loadgen"
	"github.com/jason-yusen-wu/doorbust/bench/internal/report"
	"github.com/jason-yusen-wu/doorbust/bench/internal/run"
	"github.com/jason-yusen-wu/doorbust/bench/internal/workload"
	"github.com/jason-yusen-wu/doorbust/internal/orders"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "sweep":
		os.Exit(cmdSweep(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `bench - Doorbust load generator

  bench run   [flags]   one measured run; JSON to stdout, summary to stderr
  bench sweep [flags]   every reserve arm, with a self-saturation check

Run 'bench run -h' for flags.
`)
}

type flags struct {
	opts    run.Options
	benchGO int
	out     string
	tier    *int
}

func bind(fs *flag.FlagSet) *flags {
	f := &flags{}
	o := &f.opts

	fs.StringVar(&o.DSN, "dsn", envOr("BENCH_DATABASE_URL",
		"postgres://postgres:postgres@localhost:55433/doorbust_bench?sslmode=disable"),
		"benchmark database DSN (NOT the test database, and never production)")
	fs.BoolVar(&o.AllowUnsafeTarget, "i-know-this-truncates", false,
		"run even though the DSN does not name a throwaway benchmark database")
	fs.StringVar(&o.MigrationsDir, "migrations", "./internal/adapters/postgresql/migrations", "goose migrations directory")
	fs.StringVar(&o.AppBinary, "app", "./bin/doorbust", "app binary to start and measure")
	fs.StringVar(&o.Addr, "addr", ":8099", "address the app under test should listen on")

	fs.StringVar(&o.Arm, "arm", orders.StrategyBaseline,
		"reserve strategy: "+strings.Join(orders.StrategyNames(), ", "))
	fs.BoolVar(&o.CustomerCache, "customer-cache", false, "memoise cognito sub -> customer id (R1)")
	fs.BoolVar(&o.LogRequests, "log-requests", false, "mount chi's request logger (off for measurement)")
	fs.IntVar(&o.DBMaxConns, "db-max-conns", 25, "app connection pool size")

	fs.StringVar(&o.Shape, "shape", workload.ShapeHotDeep,
		"workload shape: "+strings.Join(workload.ShapeNames(), ", "))
	fs.StringVar(&o.Arrivals, "arrivals", "fixed", "arrival process: fixed or poisson")
	fs.Float64Var(&o.Rate, "rate", 500, "offered requests per second")
	fs.DurationVar(&o.Duration, "duration", 30*time.Second, "total run length, warm-up included")
	fs.DurationVar(&o.Warmup, "warmup", 5*time.Second, "leading period discarded from the measurement")
	fs.IntVar(&o.Buyers, "buyers", 1000, "distinct simulated customers")

	fs.IntVar(&o.MaxInFlight, "max-inflight", 0, "concurrent request ceiling (default: one second of arrivals)")
	fs.DurationVar(&o.Timeout, "timeout", 10*time.Second, "per-request timeout")
	fs.DurationVar(&o.SpinSlack, "spin-slack", loadgen.DefaultSpinSlack,
		"busy-wait this long before each deadline; set 0 when cores are scarce")

	tier := fs.Int("tier", 1, "1 = loopback (relative only), 2 = on the deployed box (absolute)")
	fs.StringVar(&o.GitSHA, "git-sha", "",
		"commit this binary was built from; required when running away from a checkout")
	fs.StringVar(&o.SessionID, "session", "", "groups comparable runs (default: a timestamp)")
	fs.IntVar(&o.AppGOMAXPROCS, "app-procs", 4, "GOMAXPROCS for the app under test")
	fs.IntVar(&f.benchGO, "bench-procs", 2, "GOMAXPROCS for the generator itself")
	fs.BoolVar(&o.AllowDirty, "allow-dirty", false, "permit a run from an uncommitted tree (its result is marked void)")
	fs.Uint64Var(&o.Seed, "seed", 1, "PRNG seed, so a shape is reproducible")
	fs.StringVar(&f.out, "o", "", "write JSON here instead of stdout")

	f.tier = tier
	return f
}

func (f *flags) finish() {
	o := &f.opts
	o.Tier = report.Tier(*f.tier)
	if o.MaxInFlight <= 0 {
		// One full second of arrivals may be outstanding before the generator
		// declares itself the bottleneck.
		o.MaxInFlight = int(o.Rate)
		if o.MaxInFlight < 64 {
			o.MaxInFlight = 64
		}
	}
	if o.SessionID == "" {
		o.SessionID = time.Now().UTC().Format("20060102T150405Z")
	}
	runtime.GOMAXPROCS(f.benchGO)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	f := bind(fs)
	_ = fs.Parse(args)
	f.finish()

	// The app's own logs would otherwise interleave with the summary on stderr.
	f.opts.AppLog = io.Discard

	res, err := run.Execute(context.Background(), f.opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		return 1
	}

	summarize(os.Stderr, res)
	if err := emit(f.out, res); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		return 1
	}
	if !res.Valid {
		return 1
	}
	return 0
}

// cmdSweep runs every arm, then reruns the fastest configuration at twice the
// rate to answer the only question that makes a throughput number meaningful:
// was the SERVER saturated, or merely the generator?
func cmdSweep(args []string) int {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	f := bind(fs)
	dir := fs.String("dir", "bench/results", "directory for result JSON")
	_ = fs.Parse(args)
	f.finish()
	f.opts.AppLog = io.Discard

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		return 1
	}

	failed := false
	for _, arm := range orders.StrategyNames() {
		opts := f.opts
		opts.Arm = arm

		res, err := run.Execute(context.Background(), opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bench: arm %s: %v\n", arm, err)
			return 1
		}
		summarize(os.Stderr, res)

		path := fmt.Sprintf("%s/%s-%s-%s.json", *dir, opts.SessionID, opts.Shape, arm)
		if err := emit(path, res); err != nil {
			fmt.Fprintf(os.Stderr, "bench: %v\n", err)
			return 1
		}
		if !res.Valid {
			failed = true
		}
	}

	// The saturation check, on the arm with the most headroom.
	//
	// The question is NOT "did throughput move" — against a genuinely saturated
	// server it correctly will not. The question is whether the GENERATOR
	// stayed clean when asked for twice as much. If it did, the plateau is the
	// server's and the number is real; if it did not, both runs are void and
	// the earlier number was never a measurement of the server at all.
	double := f.opts
	double.Arm = orders.StrategyCTE
	double.Rate = f.opts.Rate * 2

	res, err := run.Execute(context.Background(), double)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: saturation check: %v\n", err)
		return 1
	}

	verdict := report.VerdictServerSaturated
	switch {
	case res.Schedule.GeneratorSaturated > 0 || res.Schedule.LagP99Micros > 1000:
		verdict = report.VerdictGeneratorLimited
	case res.Through.AcceptedPerSec > f.opts.Rate*1.8:
		verdict = report.VerdictNotYetSaturated
	}
	res.Saturation = &report.Saturation{Verdict: verdict, RefRun: f.opts.SessionID}

	summarize(os.Stderr, res)
	fmt.Fprintf(os.Stderr, "\nsaturation verdict at %.0f rps: %s\n", double.Rate, verdict)
	if verdict != report.VerdictServerSaturated {
		fmt.Fprintf(os.Stderr, "  -> the arm comparison above is NOT a server measurement\n")
		failed = true
	}

	if err := emit(fmt.Sprintf("%s/%s-saturation.json", *dir, f.opts.SessionID), res); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		return 1
	}

	if failed {
		return 1
	}
	return 0
}

func emit(path string, res *report.Result) error {
	if path == "" {
		return res.WriteJSON(os.Stdout)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := res.WriteJSON(f); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	return nil
}

// summarize prints the human-readable table, always leading with validity and
// the tier caveat. A number is never shown without the context that says what
// may be claimed from it.
func summarize(w io.Writer, r *report.Result) {
	fmt.Fprintf(w, "\n=== %s / %s ", r.App.Arm, r.Workload.Shape)
	if r.App.CustomerCache {
		fmt.Fprint(w, "(cache) ")
	}
	fmt.Fprintf(w, "===\n")

	if !r.Valid {
		fmt.Fprintf(w, "RUN VOID\n")
		for _, reason := range r.InvalidReasons {
			fmt.Fprintf(w, "  - %s\n", reason)
		}
	}
	if !r.AbsoluteThroughputValid {
		fmt.Fprintf(w, "  [relative comparison only — %s]\n", strings.TrimSpace(r.Note))
	}

	fmt.Fprintf(w, "  offered   %10.0f rps\n", r.Through.OfferedPerSec)
	fmt.Fprintf(w, "  goodput   %10.1f reserves/s\n", r.Through.GoodputPerSec)
	fmt.Fprintf(w, "  accepted  %10.1f answers/s\n", r.Through.AcceptedPerSec)
	fmt.Fprintf(w, "  predicted %10.1f reserves/s  (%d client RTT in lock, rtt %.0fus, commit %.0fus)\n",
		r.Model.PredictedCeiling, r.Model.ClientRTTsInLock, r.Model.RTTMicros, r.Model.CommitCostMicros)

	l := r.Latency.Overall
	fmt.Fprintf(w, "  latency   p50 %.0fus  p95 %.0fus  p99 %.0fus  p99.9 %.0fus  max %.0fus\n",
		l.P50, l.P95, l.P99, l.P999, l.Max)
	fmt.Fprintf(w, "  lag       p50 %.0fus  p99 %.0fus  max %.0fus  late %d (%.3f%%)  saturated %d\n",
		r.Schedule.LagP50Micros, r.Schedule.LagP99Micros, r.Schedule.LagMaxMicros,
		r.Schedule.LateRequests, r.Schedule.LateFraction*100, r.Schedule.GeneratorSaturated)

	fmt.Fprintf(w, "  outcomes  ")
	for _, name := range []string{"reserved", "out_of_stock", "server_error", "timeout", "connect_error", "generator_saturated"} {
		fmt.Fprintf(w, "%s=%d ", name, r.Outcomes[name])
	}
	fmt.Fprintln(w)

	status := "PASS"
	if !r.Invariants.Passed {
		status = "FAIL"
	}
	fmt.Fprintf(w, "  invariants %s\n", status)
	for _, c := range r.Invariants.Checks {
		if !c.OK {
			fmt.Fprintf(w, "    - %s: %s\n", c.Name, c.Detail)
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
