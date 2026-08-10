package runs

import "time"

// Package-internal delayed-retry scheduling (spec §4.4). Two regimes, per
// baked decision 6: a FALLBACK delay (no usable provider reset) is our own
// guessing and sits on [floor, cap] — a hard bound, jitter included; a
// PROVIDER-STATED reset is truth and is taken verbatim, floor-clamped and
// nothing else. Neither the cap nor the jitter may touch it: both can only
// shorten the wait, and any wake before the provider unlocks retries into
// a window we were told is shut. Its real ceiling is the wall deadline,
// which the caller compares against the resulting time (past-deadline
// eligibility terminalizes instead of parking).
type retryParams struct{ floor, base, cap time.Duration }

// Both publish codes need their OWN row: an unknown code falls through to
// CodeRateLimited's schedule below, whose 2-minute floor would park every
// resumable publication for two minutes. publish_not_started waits for
// nothing — the worker died before it caused anything — so it requeues at
// once. publish_resumable backs off because its retry re-drives the state
// machine, which re-probes the forge: that is the path a forge outage takes,
// and a curve beats hot-looping until the wall deadline, while a receipt that
// failed to commit still resumes within seconds.
var retrySchedules = map[string]retryParams{
	CodeRateLimited:       {floor: 2 * time.Minute, base: 15 * time.Minute, cap: time.Hour},
	CodeCredentialBusy:    {floor: 30 * time.Second, base: 30 * time.Second, cap: 10 * time.Minute},
	CodePublishNotStarted: {floor: 0, base: 0, cap: 0},
	CodePublishResumable:  {floor: 5 * time.Second, base: 15 * time.Second, cap: 5 * time.Minute},
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
	var delay time.Duration
	if out.RetryAt != nil && out.RetryAt.After(now) {
		delay = out.RetryAt.Sub(now) // the provider's word, untouched
	} else {
		// Our own guess, so spread it and bound it: the ±10% keeps a shared
		// cap window from waking every parked attempt at once, and the cap
		// bounds only this branch — never the provider's word.
		delay = p.base << min(uint(waits), 6) //nolint:gosec // waits is a non-negative failure count
		frac := 1 + 0.1*(2*jitter()-1)
		delay = min(time.Duration(float64(delay)*frac), p.cap)
	}
	// The floor applies to both: a reset already in the past, or one only
	// seconds out, is not worth a claim cycle.
	return max(p.floor, delay)
}
