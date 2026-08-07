// In-package: retryDelay is the scheduler's internal arithmetic and has
// no exported surface — the contract it must hold is the bound, not a
// name callers can spell.
package runs

import (
	"testing"
	"time"
)

func TestRetryDelay_ProviderResetClampedToFloorAndDeadlineless(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	noJitter := func() float64 { return 0.5 } // multiplier 1.0
	past := now.Add(-time.Hour)
	// malformed/past reset falls back to base backoff, never before floor
	got := retryDelay(Outcome{Code: CodeRateLimited, RetryAt: &past}, now, 0, noJitter)
	if got < 2*time.Minute {
		t.Fatalf("past reset must hold the floor, got %v", got)
	}
	// a sane provider reset is used, floored
	soon := now.Add(30 * time.Second)
	got = retryDelay(Outcome{Code: CodeRateLimited, RetryAt: &soon}, now, 0, noJitter)
	if got < 2*time.Minute || got > 3*time.Minute {
		t.Fatalf("reset below floor must clamp to floor, got %v", got)
	}
	// a provider reset BEYOND the fallback cap is honored, not capped —
	// waking at cap would retry while the window is still shut; the wall
	// deadline (compared by the caller) is its only ceiling
	late := now.Add(5 * time.Hour)
	got = retryDelay(Outcome{Code: CodeRateLimited, RetryAt: &late}, now, 0, noJitter)
	if got < 4*time.Hour {
		t.Fatalf("provider reset must not be capped at %v, got %v", time.Hour, got)
	}
	// Jitter must not reach the provider branch at all. Asserting only at
	// the neutral multiplier hides the failure that matters: at jit=0 a
	// ±10% band shaves a stated hour down to 54 minutes and we wake inside
	// a window the provider told us is shut. Above the floor, the returned
	// delay must never be shorter than what the provider stated.
	for _, jit := range []float64{0, 0.5, 1} {
		jitter := func() float64 { return jit }
		for _, stated := range []time.Duration{10 * time.Minute, time.Hour, 5 * time.Hour} {
			at := now.Add(stated)
			got := retryDelay(Outcome{Code: CodeRateLimited, RetryAt: &at}, now, 0, jitter)
			if got < stated {
				t.Errorf("jit=%v: provider stated %v, got %v — never wake before the provider does", jit, stated, got)
			}
		}
	}
}

// The two publish rows exist so neither code falls through to
// retrySchedules[CodeRateLimited], whose 2-minute floor would park a
// publication that never began — or one already pushed — behind a cap wait.
// Every assertion here fails if a row is deleted, because the fall-through
// answer is >= 2 minutes in both cases.
func TestRetryDelay_PublishWaitsRequeueWithoutACapWindow(t *testing.T) {
	now := time.Now()
	jitters := []float64{0, 0.5, 1}

	// publish_not_started waits for NOTHING: the worker died before it
	// authorized anything, so there is nothing to back off from. The zero row
	// is the value most likely to be "tidied" into a real backoff later.
	for _, jit := range jitters {
		jitter := func() float64 { return jit }
		for waits := int32(0); waits < 8; waits++ {
			if d := retryDelay(Outcome{Code: CodePublishNotStarted}, now, waits, jitter); d != 0 {
				t.Errorf("jit=%v waits=%d: publish_not_started = %v, want 0 (immediate requeue)", jit, waits, d)
			}
		}
	}

	// publish_resumable backs off instead: its retry re-drives the state
	// machine, which re-probes the forge, so an unreachable forge is retried
	// on a curve rather than hot-looping until the wall deadline.
	for _, jit := range jitters {
		jitter := func() float64 { return jit }
		prev := time.Duration(0)
		for waits := int32(0); waits < 8; waits++ {
			d := retryDelay(Outcome{Code: CodePublishResumable}, now, waits, jitter)
			if d < 5*time.Second || d > 5*time.Minute {
				t.Fatalf("jit=%v waits=%d: delay %v outside [floor,cap]", jit, waits, d)
			}
			if jit == 0.5 && d < prev { // monotonicity is only meaningful un-jittered
				t.Fatalf("jit=%v: backoff shrank: %v -> %v", jit, prev, d)
			}
			prev = d
		}
		// A receipt that failed to commit must resume within SECONDS. This is
		// the assertion that catches a fall-through: CodeRateLimited's floor
		// would answer 2 minutes here.
		if d := retryDelay(Outcome{Code: CodePublishResumable}, now, 0, jitter); d > 30*time.Second {
			t.Errorf("jit=%v: first resume = %v, want seconds not a cap window", jit, d)
		}
	}

	// The 5s floor never binds on the backoff branch (base 15s already
	// exceeds it); it exists for a forge that states its own near-instant
	// reset, which is not worth a claim cycle.
	soon := now.Add(time.Second)
	if d := retryDelay(Outcome{Code: CodePublishResumable, RetryAt: &soon}, now, 0, func() float64 { return 0.5 }); d != 5*time.Second {
		t.Errorf("a 1s provider reset must clamp to the 5s floor, got %v", d)
	}
}

func TestRetryDelay_BackoffGrowsAndCapsUnderAnyJitter(t *testing.T) {
	now := time.Now()
	// [floor, cap] is a hard bound: assert it at both jitter extremes, not
	// just the neutral multiplier — the clamp runs after the jitter.
	for _, jit := range []float64{0, 0.5, 1} {
		jitter := func() float64 { return jit }
		prev := time.Duration(0)
		for waits := int32(0); waits < 8; waits++ {
			d := retryDelay(Outcome{Code: CodeCredentialBusy}, now, waits, jitter)
			if d < 30*time.Second || d > 10*time.Minute {
				t.Fatalf("jit=%v waits=%d delay %v outside [floor,cap]", jit, waits, d)
			}
			if jit == 0.5 && d < prev { // monotonicity is only meaningful un-jittered
				t.Fatalf("backoff shrank: %v -> %v", prev, d)
			}
			prev = d
		}
	}
}
