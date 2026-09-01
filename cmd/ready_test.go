package main

import (
	"testing"
	"time"
)

// Readiness is what the post-deploy smoke check looks at, so its failure modes
// matter more than its happy path. The database and disabled-poller paths are
// covered through the real router in TestHealthAndReadiness; these cover the
// elapsed-time decisions, which are impractical to drive through a live poller
// without either sleeping or adding a hook to production code.

func TestPollerStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	const grace = time.Minute

	tests := []struct {
		name       string
		last       time.Time
		startedAt  time.Time
		wantStatus string
		wantReady  bool
	}{
		{
			name:       "recent success is healthy",
			last:       now.Add(-10 * time.Second),
			startedAt:  now.Add(-time.Hour),
			wantStatus: pollerOK,
			wantReady:  true,
		},
		{
			// A restart must not flap the smoke check while the first poll is
			// still in flight.
			name:       "just started, not yet polled",
			last:       time.Time{},
			startedAt:  now.Add(-5 * time.Second),
			wantStatus: pollerStarting,
			wantReady:  true,
		},
		{
			// The case that matters: an invalid or revoked API key fails every
			// tick, so last-success never advances. Reporting this as
			// "starting" forever would let a deploy with dead payments pass.
			name:       "never succeeded despite having had time",
			last:       time.Time{},
			startedAt:  now.Add(-10 * time.Minute),
			wantStatus: pollerFailing,
			wantReady:  false,
		},
		{
			// Was working, then stopped — a key revoked after startup, or
			// Stripe becoming unreachable.
			name:       "succeeded once but has gone stale",
			last:       now.Add(-10 * time.Minute),
			startedAt:  now.Add(-time.Hour),
			wantStatus: pollerStale,
			wantReady:  false,
		},
		{
			name:       "inside the grace window is still healthy",
			last:       now.Add(-59 * time.Second),
			startedAt:  now.Add(-time.Hour),
			wantStatus: pollerOK,
			wantReady:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, ready := pollerStatus(tt.last, tt.startedAt, now, grace)
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if ready != tt.wantReady {
				t.Errorf("ready = %v, want %v", ready, tt.wantReady)
			}
		})
	}
}

func TestPollerGrace(t *testing.T) {
	t.Parallel()

	// A short interval must not produce a sub-second grace window, or the
	// first request after a restart reports the process unhealthy.
	if got := pollerGrace(time.Millisecond); got != minPollerGrace {
		t.Errorf("grace for a 1ms interval = %v, want the %v floor", got, minPollerGrace)
	}

	// A long interval scales past the floor: polling every 10 minutes should
	// not be called stale after 30 seconds.
	if got := pollerGrace(10 * time.Minute); got != 50*time.Minute {
		t.Errorf("grace for a 10m interval = %v, want 50m", got)
	}

	// The production default: 5 × 10s clears the floor, so the multiple wins.
	if got := pollerGrace(10 * time.Second); got != 50*time.Second {
		t.Errorf("grace for the default 10s interval = %v, want 50s", got)
	}

	// The floor applies exactly up to the crossover.
	if got := pollerGrace(6 * time.Second); got != minPollerGrace {
		t.Errorf("grace for a 6s interval = %v, want the %v floor", got, minPollerGrace)
	}
}
