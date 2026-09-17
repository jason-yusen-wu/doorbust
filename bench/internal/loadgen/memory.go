package loadgen

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// BytesPerInFlight is a conservative estimate of what one outstanding request
// costs the generator: a goroutine stack that has already grown past its
// minimum, the transport's read and write buffers, the request and response
// structures, and the socket itself.
//
// Deliberately generous. The cost of overestimating is refusing a run that
// would have fitted; the cost of underestimating is what it was measured to be
// — a box driven into memory thrashing with no swap, taking the service it was
// hosting down with it.
const BytesPerInFlight = 64 << 10

// memoryHeadroom is the share of available memory the generator may plan to
// use. The rest belongs to the app under test, which is on the same machine in
// both tiers and is the thing actually being measured.
const memoryHeadroom = 0.5

// CheckMemoryBudget refuses an in-flight ceiling the machine cannot hold.
//
// This exists because a setting that was harmless on a laptop was carried over
// unchanged to a 1GB instance: 8,000 in-flight requests is a few hundred
// megabytes, which on a box with no swap is not a slow run but a wedged kernel.
// The generator lost SSH, SSM and the production service with it.
//
// Reported rather than silently clamped. A run at a quarter of the requested
// concurrency is a different experiment, and one that renamed itself without
// saying so would be worse than one that refused.
func CheckMemoryBudget(maxInFlight int) error {
	available, ok := availableBytes()
	if !ok {
		// Not Linux, or /proc is unreadable. Both tiers that matter run on
		// Linux; a developer's laptop has memory to spare and is not where a
		// wedged kernel costs anything.
		return nil
	}

	need := uint64(maxInFlight) * BytesPerInFlight
	budget := uint64(float64(available) * memoryHeadroom)

	if need > budget {
		return fmt.Errorf(
			"max-inflight %d needs about %d MiB for the generator alone, but only %d MiB is available "+
				"and half of that belongs to the app under test.\n"+
				"Lower -max-inflight to around %d, or run on a larger machine",
			maxInFlight, need>>20, available>>20, budget/BytesPerInFlight)
	}
	return nil
}

// availableBytes reads MemAvailable, which accounts for reclaimable cache and
// is therefore the figure that predicts thrashing — unlike MemFree.
func availableBytes() (uint64, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb << 10, true
	}
	return 0, false
}
