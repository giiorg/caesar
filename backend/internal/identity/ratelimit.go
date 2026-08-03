package identity

import (
	"context"
	"log/slog"
	"time"

	"github.com/giiorg/caesar/backend/internal/identity/store"
)

// Pinned login rate-limit parameters (plan header). DB-backed via the
// login_rate table so they survive restarts and hold across processes.
const (
	accountLockThreshold = 5
	accountLockBase      = 30 * time.Second
	accountLockMax       = 15 * time.Minute
	ipWindow             = 15 * time.Minute
	ipLockThreshold      = 30
	ipLockDuration       = 15 * time.Minute

	// loginRateRetention: a login_rate row is eligible for purge once its
	// lock (if any) has expired and its window is older than this. Without
	// it the table grows without bound from unauthenticated traffic: every
	// failed attempt against an arbitrary email leaves a permanent
	// account:<email> row, and every successful login leaves a permanent
	// ip:<addr> row (success only clears the account scope).
	loginRateRetention = 24 * time.Hour
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

// purgeStaleLoginRate opportunistically reclaims stale login_rate rows.
// Called after Login's own transaction has already committed — never from
// inside it — so a best-effort cleanup can't add lock-wait latency or
// deadlock risk to the hot login path, and its own SKIP LOCKED means it
// never blocks on a row a concurrent login holds FOR UPDATE. A purge
// failure must not fail the login that triggered it; it's logged instead
// of swallowed so it stays observable.
func (s *Service) purgeStaleLoginRate(ctx context.Context) {
	now := s.clock.Now()
	cutoff := now.Add(-loginRateRetention)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		slog.Default().WarnContext(ctx, "identity: purge login_rate: begin failed", "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := store.New(tx).PurgeStaleLoginRate(ctx, store.PurgeStaleLoginRateParams{
		Now: now, Cutoff: cutoff,
	}); err != nil {
		slog.Default().WarnContext(ctx, "identity: purge login_rate: delete failed", "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Default().WarnContext(ctx, "identity: purge login_rate: commit failed", "error", err)
	}
}
