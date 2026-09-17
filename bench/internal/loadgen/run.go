package loadgen

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/jason-yusen-wu/doorbust/bench/internal/report"
	"github.com/jason-yusen-wu/doorbust/bench/internal/workload"
	"golang.org/x/sync/semaphore"
)

// Config is one measured phase.
type Config struct {
	BaseURL  string
	Schedule []time.Duration
	Warmup   time.Duration

	Buyers     []workload.Buyer
	ProductIDs []int64
	Plan       workload.Plan

	// MaxInFlight bounds concurrent requests. Reaching it does NOT queue — the
	// request is dropped and recorded as generator_saturated, which voids the
	// run. Queueing here would reintroduce exactly the closed-loop behaviour
	// this package exists to avoid.
	MaxInFlight int
	Timeout     time.Duration
	Client      *http.Client
	Rand        *rand.Rand

	// SpinSlack is how long before a deadline the dispatcher stops sleeping and
	// busy-waits. Zero means pure sleeping. See waitUntil.
	SpinSlack time.Duration
}

// Results is what one measured phase produced.
type Results struct {
	Latency    report.Latencies
	Outcomes   map[string]int64
	Schedule   report.Schedule
	Throughput report.Throughput
	// Reserved is the measured window's successful reserves.
	Reserved int64
	// ReservedTotal includes warm-up, and is what the orders table is compared
	// against — the database kept those orders too.
	ReservedTotal int64
	// UnknownTotal counts requests whose outcome the client never learned:
	// timeouts and connection errors. The server may or may not have created
	// an order for each, which is what makes the cross-check an inequality.
	UnknownTotal int64
	Measured     time.Duration
}

// Run executes the schedule and returns what happened.
func Run(ctx context.Context, cfg Config) (Results, error) {
	rec := newRecorder(len(cfg.Schedule))
	sem := semaphore.NewWeighted(int64(cfg.MaxInFlight))

	bodies := make([][]byte, len(cfg.ProductIDs))
	for i, id := range cfg.ProductIDs {
		bodies[i] = []byte(fmt.Sprintf(`{"product_id":%d}`, id))
	}

	var wg sync.WaitGroup
	start := time.Now()

	for i, due := range cfg.Schedule {
		// Wait until this request is due, then fire everything already due in a
		// tight loop.
		//
		// The coalescing matters more than it looks. At a few thousand rps the
		// mean gap between arrivals is a couple of hundred microseconds, which
		// is below the granularity a macOS timer can reliably deliver. Sleeping
		// per request would make timer error, not the schedule, decide when
		// requests go out — and the generator would fall progressively behind,
		// which is coordinated omission arriving through the back door.
		waitUntil(start.Add(due), cfg.SpinSlack)

		sentAt := time.Now()
		rec.lag[i] = sentAt.Sub(start.Add(due)).Nanoseconds()

		if !sem.TryAcquire(1) {
			// The generator ran out of capacity. Recorded, never queued: a
			// request that was never sent must not silently vanish from the
			// denominator, and its absence must void the run rather than
			// flatter it.
			rec.outcome[i] = OutcomeGeneratorSaturated
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sem.Release(1)

			buyer := cfg.Buyers[i%len(cfg.Buyers)]
			sku := cfg.Plan.Pick(i, cfg.Rand)
			outcome := send(ctx, cfg, buyer, bodies[sku])

			done := time.Now()
			rec.sent[i] = true
			rec.outcome[i] = outcome
			rec.service[i] = done.Sub(sentAt).Nanoseconds()
			// Measured from when the request was DUE, not when it was sent.
			// This is the single most important line in the package: it is what
			// makes generator delay show up as latency instead of disappearing.
			rec.arrival[i] = done.Sub(start.Add(due)).Nanoseconds()
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	from := rec.window(cfg.Schedule, cfg.Warmup)
	latency, counts := rec.collect(from)
	sched := rec.scheduleStats(from)

	measured := elapsed - cfg.Warmup
	if measured <= 0 {
		return Results{}, fmt.Errorf("warm-up (%v) consumed the whole run (%v)", cfg.Warmup, elapsed)
	}

	reserved := counts[OutcomeReserved.String()]
	accepted := reserved + counts[OutcomeOutOfStock.String()]
	secs := measured.Seconds()

	return Results{
		Latency:  latency,
		Outcomes: counts,
		Schedule: sched,
		Throughput: report.Throughput{
			GoodputPerSec:  float64(reserved) / secs,
			AcceptedPerSec: float64(accepted) / secs,
		},
		Reserved:      reserved,
		ReservedTotal: rec.totalReserved(),
		UnknownTotal:  rec.totalUnknown(),
		Measured:      measured,
	}, nil
}

// DefaultSpinSlack suits a machine with cores to spare.
//
// time.Sleep on macOS routinely overshoots by 1-2ms, and that overshoot showed
// up directly as p99 schedule lag above the validity threshold even at trivial
// rates. The overshoot is not cumulative — the schedule is absolute, so each
// request is late independently — but it is still a millisecond or two added to
// every measured latency, which at loopback timings is larger than the thing
// being measured. Sleeping to within a few milliseconds of the deadline and
// busy-waiting the remainder removes it.
const DefaultSpinSlack = 3 * time.Millisecond

// waitUntil blocks until the deadline.
//
// **Spinning is not free, and on a small box it is actively harmful.** It burns
// a core, and where the generator shares only two vCPUs with the app under
// test, that is a core the app needed — the generator then starves the very
// thing it is measuring and reports schedule lag caused by its own busy-wait.
// Pass slack = 0 there: Linux timers are accurate enough that pure sleeping
// keeps to the schedule, which is exactly the case macOS is not.
func waitUntil(deadline time.Time, slack time.Duration) {
	if d := time.Until(deadline) - slack; d > 0 {
		time.Sleep(d)
	}
	if slack <= 0 {
		return
	}
	for time.Now().Before(deadline) {
		runtime.Gosched()
	}
}

func send(ctx context.Context, cfg Config, buyer workload.Buyer, body []byte) Outcome {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, cfg.BaseURL+"/orders", bytes.NewReader(body))
	if err != nil {
		return OutcomeConnectError
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+buyer.Token)

	resp, err := cfg.Client.Do(req)
	if err != nil {
		return ClassifyError(err)
	}
	return ClassifyResponse(resp)
}
