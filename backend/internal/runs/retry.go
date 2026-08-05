package runs

import "time"

// Package-internal delayed-retry scheduling (spec §4.4). Two regimes, per
// baked decision 6: a FALLBACK delay (no usable provider reset) is our own
// guessing and sits on [floor, cap] — a hard bound, jitter included; a
// PROVIDER-STATED reset is truth and is only floor-clamped — capping it
// would wake us before the provider unlocks, and its real ceiling is the
// wall deadline, which the caller compares against the resulting time
// (past-deadline eligibility terminalizes instead of parking).
type retryParams struct{ floor, base, cap time.Duration }

var retrySchedules = map[string]retryParams{
	CodeRateLimited:    {floor: 2 * time.Minute, base: 15 * time.Minute, cap: time.Hour},
	CodeCredentialBusy: {floor: 30 * time.Second, base: 30 * time.Second, cap: 10 * time.Minute},
}

// retryDelay returns a duration, not an absolute time: the caller adds it to
// the DATABASE clock (the same clock the claim CTE's `not_before <= now()`
// predicate reads — the global DB-clock constraint), never to an app-side
// timestamp. `now` is that database now, used only to convert a
// provider-surfaced absolute RetryAt into a delay.
func retryDelay(out Outcome, now time.Time, waits int32, jitter func() float64) time.Duration {
	p, ok := retrySchedules[out.Code]
	if !ok {
		p = retrySchedules[CodeRateLimited] // conservative default for future wait-class codes
	}
	providerReset := out.RetryAt != nil && out.RetryAt.After(now)
	var delay time.Duration
	if providerReset {
		delay = out.RetryAt.Sub(now)
	} else {
		delay = p.base << min(uint(waits), 6) //nolint:gosec // waits is a non-negative failure count
	}
	frac := 1 + 0.1*(2*jitter()-1) // ±10%, then clamp: the bounds are hard
	delay = time.Duration(float64(delay) * frac)
	delay = max(p.floor, delay)
	if !providerReset {
		delay = min(delay, p.cap) // the cap bounds only our own backoff, never the provider's word
	}
	return delay
}
