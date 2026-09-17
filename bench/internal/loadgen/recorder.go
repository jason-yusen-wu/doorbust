package loadgen

import (
	"slices"
	"time"

	"github.com/jason-yusen-wu/doorbust/bench/internal/report"
)

// recorder holds one sample per scheduled request.
//
// Every slice is preallocated to the full schedule length and request i writes
// ONLY index i. Distinct indices of a slice with a fixed backing array are
// independently writable, so there is no lock, no atomic and no race — and no
// contention inside the generator, which would otherwise be measuring itself.
//
// An HDR histogram was considered and rejected: it trades exactness for
// constant memory, and constant memory is not a problem here. 300k requests is
// about 7MB of int64s, which sorts in milliseconds and gives exact percentiles
// with no bucket error and no new dependency. Revisit if runs ever last hours,
// and record the memory number when doing so.
type recorder struct {
	arrival []int64 // ns from scheduled arrival to response — the headline
	service []int64 // ns from actual send to response
	lag     []int64 // ns the send was late by
	outcome []Outcome
	sent    []bool
}

func newRecorder(n int) *recorder {
	return &recorder{
		arrival: make([]int64, n),
		service: make([]int64, n),
		lag:     make([]int64, n),
		outcome: make([]Outcome, n),
		sent:    make([]bool, n),
	}
}

// percentilesOf sorts a copy and indexes it. Exact, not interpolated: for a
// latency distribution the nearest-rank definition is the honest one, and
// interpolating between two observed samples invents a value nobody measured.
func percentilesOf(samples []int64) report.Percentiles {
	if len(samples) == 0 {
		return report.Percentiles{}
	}

	s := slices.Clone(samples)
	slices.Sort(s)

	at := func(q float64) float64 {
		i := int(q * float64(len(s)))
		if i >= len(s) {
			i = len(s) - 1
		}
		return float64(s[i]) / 1000.0 // ns -> us
	}

	return report.Percentiles{
		P50:   at(0.50),
		P95:   at(0.95),
		P99:   at(0.99),
		P999:  at(0.999),
		Max:   float64(s[len(s)-1]) / 1000.0,
		Count: len(s),
	}
}

// window selects the requests inside the measured period, discarding warm-up.
//
// Warm-up is discarded rather than merely noted because the first requests of a
// run pay for things production has already paid for long ago: an empty
// connection pool, a cold plan cache, an unwarmed page cache. Including them
// measures startup, not steady state.
func (r *recorder) window(schedule []time.Duration, warmup time.Duration) (from int) {
	for i, at := range schedule {
		if at >= warmup {
			return i
		}
	}
	return len(schedule)
}

// totalReserved counts successful reserves across the WHOLE run, warm-up
// included. The client-vs-server cross-check compares this against the orders
// table, and the database does not know that the first few seconds were warm-up
// — it kept every order. Comparing the windowed count against the full table
// would report a mismatch on every run.
func (r *recorder) totalReserved() int64 {
	var n int64
	for i := range r.outcome {
		if r.sent[i] && r.outcome[i] == OutcomeReserved {
			n++
		}
	}
	return n
}

// totalUnknown counts requests across the whole run whose outcome the client
// never learned. The server may have completed them anyway.
func (r *recorder) totalUnknown() int64 {
	var n int64
	for i := range r.outcome {
		switch r.outcome[i] {
		case OutcomeTimeout, OutcomeConnectError:
			n++
		}
	}
	return n
}

// collect turns raw samples into the reported latency and outcome figures.
func (r *recorder) collect(from int) (report.Latencies, map[string]int64) {
	counts := map[string]int64{}
	for _, name := range OutcomeNames() {
		counts[name] = 0
	}

	var overall, reserved, outOfStock, service []int64

	for i := from; i < len(r.outcome); i++ {
		o := r.outcome[i]
		counts[o.String()]++

		if !r.sent[i] {
			continue
		}

		overall = append(overall, r.arrival[i])
		service = append(service, r.service[i])
		switch o {
		case OutcomeReserved:
			reserved = append(reserved, r.arrival[i])
		case OutcomeOutOfStock:
			outOfStock = append(outOfStock, r.arrival[i])
		}
	}

	return report.Latencies{
		Overall:        percentilesOf(overall),
		Reserved:       percentilesOf(reserved),
		OutOfStock:     percentilesOf(outOfStock),
		ServiceOverall: percentilesOf(service),
	}, counts
}

// scheduleStats reports how well the generator kept to its own plan. This is
// the run's self-check: if lag grew, the numbers describe the generator.
func (r *recorder) scheduleStats(from int) report.Schedule {
	var lags []int64
	var saturated, late int64

	for i := from; i < len(r.outcome); i++ {
		if r.outcome[i] == OutcomeGeneratorSaturated {
			saturated++
		}
		if r.sent[i] {
			lags = append(lags, r.lag[i])
			if r.lag[i] > int64(time.Millisecond) {
				late++
			}
		}
	}

	p := percentilesOf(lags)
	total := len(r.outcome) - from

	var fraction float64
	if total > 0 {
		fraction = float64(late) / float64(total)
	}

	return report.Schedule{
		TrustedPercentile:  report.TrustedPercentileFor(fraction),
		Requests:           total,
		LagP50Micros:       p.P50,
		LagP99Micros:       p.P99,
		LagMaxMicros:       p.Max,
		LateRequests:       late,
		LateFraction:       fraction,
		GeneratorSaturated: saturated,
	}
}
