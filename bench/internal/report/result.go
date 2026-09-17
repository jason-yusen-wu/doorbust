// Package report is the shape of a benchmark run's output.
//
// The schema carries its own caveats. Tier 1 runs the generator on the same
// machine as the app, so its numbers compare arms against each other and are
// not absolute throughput — and the only reliable way to stop that caveat being
// dropped when a number is copied into a README is to make it a required field
// of every result rather than a line in a document somebody may not read.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// SchemaVersion is bumped when a field's meaning changes, so an old committed
// result is never silently compared against a new one.
const SchemaVersion = 1

// Tier is where a run happened, which decides what may be claimed from it.
type Tier int

const (
	// TierLoopback is app + database + generator on one machine. Relative
	// comparisons only.
	TierLoopback Tier = 1
	// TierDeployed is the generator running on the app's own box against a
	// remote database — the honesty run. Absolute, with its own caveats.
	TierDeployed Tier = 2
)

type Result struct {
	Schema int  `json:"schema"`
	Tier   Tier `json:"tier"`

	// Valid is false when something about the run means its numbers must not
	// be used. InvalidReasons says what. A void run is still written out, so
	// that "we ran it and threw it away" is visible rather than silent.
	Valid          bool     `json:"valid"`
	InvalidReasons []string `json:"invalid_reasons"`

	// AbsoluteThroughputValid records whether Throughput may be quoted as a
	// real-world number or only compared against sibling runs.
	AbsoluteThroughputValid bool   `json:"absolute_throughput_valid"`
	Note                    string `json:"note"`

	Meta       Meta       `json:"meta"`
	DB         DBInfo     `json:"db"`
	App        AppInfo    `json:"app"`
	Model      Model      `json:"model"`
	Workload   Workload   `json:"workload"`
	Schedule   Schedule   `json:"schedule"`
	Through    Throughput `json:"throughput"`
	Latency    Latencies  `json:"latency_us"`
	Outcomes   map[string]int64
	Invariants Invariants  `json:"invariants"`
	Saturation *Saturation `json:"saturation_check,omitempty"`
}

// Model is B1: the analytic prediction, measured in the same run as the thing
// it predicts. A prediction confirmed by its own run is a far stronger result
// than a number with no theory attached, and a mismatch is the interesting
// case — either way you learn which without a second experiment.
type Model struct {
	RTTMicros        float64 `json:"rtt_us"`
	CommitCostMicros float64 `json:"commit_cost_us"`
	ExecMicros       float64 `json:"exec_us"`
	// ClientRTTsInLock is the arm's defining property: how many client round
	// trips the stock row's lock is held across.
	ClientRTTsInLock int     `json:"client_rtts_in_lock"`
	PredictedCeiling float64 `json:"predicted_ceiling_per_sku"`
}

type Workload struct {
	Shape           string  `json:"shape"`
	Arrivals        string  `json:"arrivals"`
	TargetRate      float64 `json:"target_rate"`
	SKUs            int     `json:"skus"`
	InitialQuantity int32   `json:"initial_quantity"`
	Buyers          int     `json:"buyers"`
	DurationSeconds float64 `json:"duration_s"`
	WarmupSeconds   float64 `json:"warmup_s"`
}

// Schedule reports whether the generator kept up. Lag is the gap between when a
// request was due to be sent and when it actually was; if that grows, the
// generator is the bottleneck and the run says nothing about the server.
type Schedule struct {
	Requests     int     `json:"requests"`
	LagP50Micros float64 `json:"lag_p50_us"`
	LagP99Micros float64 `json:"lag_p99_us"`
	LagMaxMicros float64 `json:"lag_max_us"`
	// LateRequests is how many sends missed their deadline by more than a
	// millisecond, and LateFraction that as a share of the run. This, not the
	// raw maximum, is what decides whether generator delay could have moved the
	// reported statistics.
	LateRequests int64   `json:"late_requests"`
	LateFraction float64 `json:"late_fraction"`

	GeneratorSaturated int64 `json:"generator_saturated"`
}

// Throughput is deliberately three numbers. Goodput is the per-SKU ceiling the
// arms are scored on; Accepted includes correct out-of-stock answers, which are
// real work the server did; Offered is what was asked for. One number would
// hide which of those was being reported.
type Throughput struct {
	GoodputPerSec  float64 `json:"goodput_per_s"`
	AcceptedPerSec float64 `json:"accepted_per_s"`
	OfferedPerSec  float64 `json:"offered_per_s"`
}

type Percentiles struct {
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	P999  float64 `json:"p999"`
	Max   float64 `json:"max"`
	Count int     `json:"count"`
}

// Latencies splits by outcome as well as overall. Out-of-stock responses are
// structurally cheaper — the guarded UPDATE matches no row, so it takes no lock
// and commits trivially — so mixing them into one distribution makes a
// doorbuster run's p99 meaningless.
type Latencies struct {
	Overall        Percentiles `json:"overall"`
	Reserved       Percentiles `json:"reserved"`
	OutOfStock     Percentiles `json:"out_of_stock"`
	ServiceOverall Percentiles `json:"service_overall"`
}

type Invariants struct {
	Passed bool             `json:"passed"`
	Checks []InvariantCheck `json:"checks"`
}

type InvariantCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Saturation struct {
	Verdict string `json:"verdict"`
	RefRun  string `json:"ref_run"`
}

// Saturation verdicts.
const (
	VerdictServerSaturated  = "server-saturated"
	VerdictGeneratorLimited = "generator-limited"
	VerdictNotYetSaturated  = "not-yet-saturated"
)

// Void marks the run unusable. Every caller that discovers a reason to distrust
// a run calls this rather than returning an error, so the result still gets
// written and the reason is on the record.
func (r *Result) Void(format string, args ...any) {
	r.Valid = false
	r.InvalidReasons = append(r.InvalidReasons, fmt.Sprintf(format, args...))
}

// WriteJSON emits the machine-readable result. It goes to stdout so a run can
// be redirected straight into bench/results/ while the human summary, on
// stderr, stays visible.
func (r *Result) WriteJSON(w io.Writer) error {
	r.Schema = SchemaVersion
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// New builds a result with the honesty fields already set for its tier, so no
// code path can produce one that forgot them.
func New(tier Tier) *Result {
	r := &Result{
		Tier:     tier,
		Valid:    true,
		Outcomes: map[string]int64{},
	}
	switch tier {
	case TierLoopback:
		r.AbsoluteThroughputValid = false
		r.Note = "Tier 1: the generator shares CPU with the app and the database. " +
			"Comparable between arms measured in the same session on this machine; " +
			"never quotable as absolute throughput."
	case TierDeployed:
		r.AbsoluteThroughputValid = true
		r.Note = "Tier 2: generator on the app's own box against a remote database. " +
			"Absolute, but the generator shares the instance's CPU and the instance " +
			"is burstable — keep runs short enough that CPU credits are not what is " +
			"being measured."
	}
	return r
}

// Elapsed is a helper for durations reported in microseconds, which is the unit
// every latency in this schema uses.
func Micros(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1000.0 }
