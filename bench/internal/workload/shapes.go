package workload

import (
	"fmt"
	"math/rand/v2"
)

// Shape names. These appear in committed results, so they are a contract.
const (
	// ShapeHotDeep is one SKU with far more stock than the run can consume.
	//
	// This is the ONLY shape the contention arms can be scored on, and the
	// reason is subtle enough to be worth stating: once quantity - num_reserved
	// > 0 is false, the guarded UPDATE matches no row, takes NO LOCK, and
	// commits trivially. So in a run where stock is smaller than demand, only
	// the first `quantity` requests ever contend and every request after that
	// is a cheap lock-free no-op. Such a run measures tail latency and
	// correctness; it cannot produce a per-SKU ceiling.
	ShapeHotDeep = "hot-deep"

	// ShapeDoorbuster is the real flash sale: one SKU, stock far below demand.
	// Measures tail latency and proves the oversell guarantee under load. Not
	// a throughput measurement — see above.
	ShapeDoorbuster = "doorbuster"

	// ShapeUncontended spreads demand across enough SKUs that no two requests
	// collide. The denominator: what the app can do when row contention is not
	// the limit.
	ShapeUncontended = "uncontended"

	// ShapeZipf is realistic skew — a few popular SKUs and a long tail — which
	// shows how much of the uncontended ceiling the hot head eats.
	ShapeZipf = "zipf"
)

func ShapeNames() []string {
	return []string{ShapeHotDeep, ShapeDoorbuster, ShapeUncontended, ShapeZipf}
}

// Plan is a shape resolved against a concrete run size: how many SKUs to seed,
// how much stock each gets, and which SKU a given request should target.
type Plan struct {
	Name     string
	SKUs     int
	Quantity int32

	pick func(i int, rng *rand.Rand) int
}

// Pick returns the index of the SKU request i should buy.
func (p Plan) Pick(i int, rng *rand.Rand) int { return p.pick(i, rng) }

// PlanFor resolves a shape for a run of `requests` total requests.
func PlanFor(shape string, requests int) (Plan, error) {
	switch shape {
	case ShapeHotDeep:
		return Plan{
			Name: ShapeHotDeep,
			SKUs: 1,
			// Ten times the maximum conceivable successes, so the SKU cannot
			// run dry mid-run and turn the measurement into the no-lock path.
			Quantity: int32(requests)*10 + 1000,
			pick:     func(int, *rand.Rand) int { return 0 },
		}, nil

	case ShapeDoorbuster:
		// Deliberately tiny relative to demand: the doorbuster this project is
		// named for, where almost everyone correctly loses.
		q := int32(requests / 100)
		if q < 1 {
			q = 1
		}
		return Plan{
			Name:     ShapeDoorbuster,
			SKUs:     1,
			Quantity: q,
			pick:     func(int, *rand.Rand) int { return 0 },
		}, nil

	case ShapeUncontended:
		// One SKU per request would be wasteful to seed; enough SKUs that
		// simultaneous collisions on one row are rare is what matters.
		skus := requests / 10
		if skus < 64 {
			skus = 64
		}
		return Plan{
			Name:     ShapeUncontended,
			SKUs:     skus,
			Quantity: int32(requests)*10 + 1000,
			// Round-robin rather than random: it guarantees an even spread
			// instead of merely expecting one, so a low-contention run cannot
			// accidentally contend.
			pick: func(i int, _ *rand.Rand) int { return i % skus },
		}, nil

	case ShapeZipf:
		const skus = 1000
		return Plan{
			Name:     ShapeZipf,
			SKUs:     skus,
			Quantity: int32(requests)*10 + 1000,
			pick: func(_ int, rng *rand.Rand) int {
				// s=1.1 is a mild but real skew. Built per call site rather
				// than shared because rand.Zipf is not safe for concurrent use;
				// the dispatcher picks SKUs on one goroutine, so one is enough.
				return int(zipfFor(rng, skus).Uint64())
			},
		}, nil

	default:
		return Plan{}, fmt.Errorf("unknown workload shape %q, want one of %v", shape, ShapeNames())
	}
}

// zipfFor memoises a Zipf generator per rng. Constructing one per call would
// dominate the dispatcher loop.
var zipfCache = map[*rand.Rand]*rand.Zipf{}

func zipfFor(rng *rand.Rand, n int) *rand.Zipf {
	if z, ok := zipfCache[rng]; ok {
		return z
	}
	z := rand.NewZipf(rng, 1.1, 1, uint64(n-1))
	zipfCache[rng] = z
	return z
}
