package identity

import (
	"testing"
	"time"
)

func TestRateFail_AccountBackoffLadder(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	st := rateState{}
	// Failures 1–4: no lock.
	for i := 1; i <= 4; i++ {
		st = rateFail(rateAccount, st, now)
		if st.LockedUntil != nil {
			t.Fatalf("failure %d: locked too early", i)
		}
	}
	// Failure 5 → 30s, 6 → 1m, 7 → 2m, 8 → 4m, 9 → 8m, 10+ → capped 15m.
	wants := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, want := range wants {
		st = rateFail(rateAccount, st, now)
		if st.LockedUntil == nil {
			t.Fatalf("failure %d: expected lock", i+5)
		}
		if got := st.LockedUntil.Sub(now); got != want {
			t.Fatalf("failure %d: lock = %v, want %v", i+5, got, want)
		}
	}
}

func TestRateFail_IPWindowResetsAndLocks(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	st := rateState{}
	for i := 0; i < 29; i++ {
		st = rateFail(rateIP, st, now)
	}
	if st.LockedUntil != nil {
		t.Fatal("29 failures must not lock")
	}
	// Window expiry resets the count.
	later := now.Add(16 * time.Minute)
	st = rateFail(rateIP, st, later)
	if st.Failures != 1 || st.LockedUntil != nil {
		t.Fatalf("after window reset: %+v", st)
	}
	// 30 in-window failures lock for 15m.
	for i := 0; i < 29; i++ {
		st = rateFail(rateIP, st, later)
	}
	if st.LockedUntil == nil || !st.LockedUntil.Equal(later.Add(15*time.Minute)) {
		t.Fatalf("ip lock = %+v", st.LockedUntil)
	}
}

func TestRateBlocked(t *testing.T) {
	now := time.Unix(1000, 0)
	until := now.Add(time.Minute)
	if !rateBlocked(rateState{LockedUntil: &until}, now) {
		t.Fatal("inside lock window must block")
	}
	if rateBlocked(rateState{LockedUntil: &until}, now.Add(2*time.Minute)) {
		t.Fatal("past lock window must not block")
	}
	if rateBlocked(rateState{}, now) {
		t.Fatal("no lock must not block")
	}
}
