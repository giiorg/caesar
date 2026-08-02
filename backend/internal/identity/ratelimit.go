package identity

import "time"

// Pinned login rate-limit parameters (plan header). DB-backed via the
// login_rate table so they survive restarts and hold across processes.
const (
	accountLockThreshold = 5
	accountLockBase      = 30 * time.Second
	accountLockMax       = 15 * time.Minute
	ipWindow             = 15 * time.Minute
	ipLockThreshold      = 30
	ipLockDuration       = 15 * time.Minute
)

type rateKind int

const (
	rateAccount rateKind = iota
	rateIP
)

// rateState mirrors one login_rate row.
type rateState struct {
	Failures    int
	WindowStart time.Time
	LockedUntil *time.Time
}

func rateBlocked(st rateState, now time.Time) bool {
	return st.LockedUntil != nil && now.Before(*st.LockedUntil)
}

// rateFail applies one failed attempt and returns the next state.
// Account scope: consecutive failures, exponential lock from the 5th,
// capped at 15m; the counter resets only on success (Login deletes the
// row). IP scope: fixed 15-minute window; the 30th in-window failure
// locks for 15 minutes.
func rateFail(kind rateKind, st rateState, now time.Time) rateState {
	switch kind {
	case rateAccount:
		if st.WindowStart.IsZero() {
			st.WindowStart = now
		}
		st.Failures++
		if st.Failures >= accountLockThreshold {
			d := accountLockMax
			if over := st.Failures - accountLockThreshold; over < 6 {
				if v := accountLockBase << over; v < d {
					d = v
				}
			}
			t := now.Add(d)
			st.LockedUntil = &t
		}
	case rateIP:
		if st.WindowStart.IsZero() || now.Sub(st.WindowStart) >= ipWindow {
			st = rateState{WindowStart: now}
		}
		st.Failures++
		if st.Failures >= ipLockThreshold {
			t := now.Add(ipLockDuration)
			st.LockedUntil = &t
		}
	}
	return st
}
