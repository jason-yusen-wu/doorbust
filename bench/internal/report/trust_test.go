package report

import "testing"

// The rule these pin: a late send inflates exactly one sample, so the share of
// late sends bounds which tail statistics the generator could have moved. A run
// that cannot support p99.9 can still support p99, and saying so beats throwing
// the whole measurement away.
func TestTrustedPercentileFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fraction float64
		want     string
	}{
		{"nothing late", 0, TrustP999},
		{"one in ten thousand", 0.0001, TrustP999},
		{"exactly at the p99.9 boundary", 0.001, TrustP999},
		{"just past it — eight of 3750", 8.0 / 3750.0, TrustP99},
		{"half a percent", 0.005, TrustP99},
		{"exactly at the p99 boundary", 0.01, TrustP99},
		{"past p99 — the run is void", 0.02, TrustNone},
		{"generator collapsed", 0.19, TrustNone},
	}

	for _, c := range cases {
		if got := TrustedPercentileFor(c.fraction); got != c.want {
			t.Errorf("%s (%.4f): got %q, want %q", c.name, c.fraction, got, c.want)
		}
	}
}
