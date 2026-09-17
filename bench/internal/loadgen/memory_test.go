package loadgen

import (
	"runtime"
	"strings"
	"testing"
)

// The setting that wedged the deployed box: 8,000 in-flight on a 1GiB instance
// with no swap. These pin the arithmetic that would have refused it.
func TestMemoryBudgetArithmetic(t *testing.T) {
	t.Parallel()

	const oneGiB = uint64(1) << 30

	// Half of 1GiB, divided by the 64KiB per-request estimate.
	affordable := int(float64(oneGiB) * memoryHeadroom / BytesPerInFlight)
	if affordable != 8192 {
		t.Fatalf("budget arithmetic changed: %d", affordable)
	}

	// The real box had ~530MiB available, not a full gigabyte, so 8,000 was
	// comfortably over the line rather than marginally under it.
	const realAvailable = 530 << 20
	realAffordable := int(float64(realAvailable) * memoryHeadroom / BytesPerInFlight)
	if realAffordable >= 8000 {
		t.Errorf("8000 in-flight should not have fitted in %dMiB; budget says %d",
			realAvailable>>20, realAffordable)
	}
	if realAffordable < 1000 {
		t.Errorf("the budget is so tight it would refuse reasonable runs: %d", realAffordable)
	}
}

func TestCheckMemoryBudget(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		// Off Linux the check reads no /proc and deliberately allows anything:
		// a developer laptop has memory to spare, and a wedged kernel there
		// costs nothing that matters.
		if err := CheckMemoryBudget(1 << 30); err != nil {
			t.Errorf("expected a no-op away from Linux, got: %v", err)
		}
		return
	}

	if err := CheckMemoryBudget(64); err != nil {
		t.Errorf("a trivially small run was refused: %v", err)
	}

	// Something no machine has.
	err := CheckMemoryBudget(1 << 30)
	if err == nil {
		t.Fatal("an absurd in-flight ceiling was accepted")
	}
	// The message has to tell the operator what to do, not just that they are
	// wrong — this fires in the middle of a benchmarking session.
	if !strings.Contains(err.Error(), "Lower -max-inflight") {
		t.Errorf("error gives no remedy: %v", err)
	}
}
