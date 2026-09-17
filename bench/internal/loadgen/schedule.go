// Package loadgen is the open-loop request generator.
//
// Open-loop is not a stylistic choice. A closed-loop generator — N workers each
// looping "send, wait for response, send again" — cannot offer load faster than
// the server answers, so when the server slows down the generator slows down
// with it and the queueing that a real overload produces never happens. That is
// coordinated omission, and it is the difference between a load test and a for
// loop: it reports service time while hiding the latency users would actually
// see.
//
// So arrival times are decided in advance, before any request is sent, and a
// request that cannot be sent on time is recorded as the generator's failure
// rather than absorbed into a queue.
package loadgen

import (
	"fmt"
	"math/rand/v2"
	"time"
)

// Arrival process names.
const (
	// ArrivalsPoisson has exponentially distributed gaps — how independent
	// buyers actually arrive. The right choice for a single realistic run.
	ArrivalsPoisson = "poisson"

	// ArrivalsFixed puts every arrival on a metronome. The right choice when
	// comparing two arms, because it removes arrival-process variance from the
	// difference between the runs: any difference left is the server.
	ArrivalsFixed = "fixed"
)

// BuildSchedule returns the offsets, from run start, at which each request is
// due to be sent.
//
// Computed entirely up front. That is what makes the run open-loop: the
// schedule cannot be influenced by how the server is coping.
func BuildSchedule(arrivals string, rate float64, d time.Duration, rng *rand.Rand) ([]time.Duration, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("rate must be positive, got %v", rate)
	}

	n := int(rate * d.Seconds())
	if n <= 0 {
		return nil, fmt.Errorf("rate %v over %v yields no requests", rate, d)
	}

	out := make([]time.Duration, 0, n)
	mean := float64(time.Second) / rate

	switch arrivals {
	case ArrivalsFixed:
		for i := range n {
			out = append(out, time.Duration(float64(i)*mean))
		}
	case ArrivalsPoisson:
		var offset float64
		for range n {
			// Inter-arrival times of a Poisson process are exponential with
			// mean 1/rate.
			offset += rng.ExpFloat64() * mean
			out = append(out, time.Duration(offset))
		}
	default:
		return nil, fmt.Errorf("unknown arrival process %q, want %q or %q",
			arrivals, ArrivalsPoisson, ArrivalsFixed)
	}

	return out, nil
}
