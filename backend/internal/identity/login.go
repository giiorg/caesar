package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

var errBadCredentials = xerrors.E(xerrors.ClassUnauthenticated, "invalid email or password")

var errLoginLocked = xerrors.E(xerrors.ClassRateLimited, "too many failed attempts, try again later")

// dummyVerify equalizes timing for unknown emails: one argon2 run happens
// on every credential failure path.
var (
	dummyOnce sync.Once
	dummyPHC  string
)

func dummyVerify(pw string) {
	dummyOnce.Do(func() {
		dummyPHC, _ = HashPassword("caesar.dummy.timing.equalizer")
	})
	_, _ = VerifyPassword(dummyPHC, pw)
}

func rateStateFromRow(row store.LoginRate) rateState {
	return rateState{Failures: int(row.Failures), WindowStart: row.WindowStartedAt, LockedUntil: row.LockedUntil}
}

// loadRate reads a scope's row under FOR UPDATE (absent row = zero state).
func loadRate(ctx context.Context, q *store.Queries, scope string) (rateState, error) {
	row, err := q.GetLoginRateForUpdate(ctx, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return rateState{}, nil
	}
	if err != nil {
		return rateState{}, fmt.Errorf("identity: rate lookup: %w", err)
	}
	return rateStateFromRow(row), nil
}

func saveRate(ctx context.Context, q *store.Queries, scope string, st rateState) error {
	err := q.UpsertLoginRate(ctx, store.UpsertLoginRateParams{
		Scope: scope, Failures: int32(st.Failures), WindowStartedAt: st.WindowStart, LockedUntil: st.LockedUntil,
	})
	if err != nil {
		return fmt.Errorf("identity: rate upsert: %w", err)
	}
	return nil
}

// Login authenticates email+password from ip. One transaction: rate rows
// locked FOR UPDATE (serializing concurrent attempts per scope), failure
// counters and the session mint commit atomically with their audit rows.
func (s *Service) Login(ctx context.Context, email, password, ip string) (User, string, error) {
	if len(password) > MaxPasswordLen {
		return User{}, "", errBadCredentials
	}
	accountScope := "account:" + strings.ToLower(email)
	ipScope := "ip:" + ip
	now := s.clock.Now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, "", fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	// Deterministic lock order: account scope, then ip scope.
	accountRate, err := loadRate(ctx, q, accountScope)
	if err != nil {
		return User{}, "", err
	}
	ipRate, err := loadRate(ctx, q, ipScope)
	if err != nil {
		return User{}, "", err
	}
	if rateBlocked(accountRate, now) || rateBlocked(ipRate, now) {
		return User{}, "", errLoginLocked
	}

	row, lookupErr := q.GetUserByEmail(ctx, email)
	authenticated := false
	if lookupErr == nil {
		ok, verr := VerifyPassword(row.PasswordHash, password)
		if verr != nil {
			return User{}, "", fmt.Errorf("identity: verify: %w", verr)
		}
		authenticated = ok && row.Active
	} else if errors.Is(lookupErr, pgx.ErrNoRows) {
		dummyVerify(password)
	} else {
		return User{}, "", fmt.Errorf("identity: user lookup: %w", lookupErr)
	}

	if !authenticated {
		nextAccount := rateFail(rateAccount, accountRate, now)
		nextIP := rateFail(rateIP, ipRate, now)
		if err := saveRate(ctx, q, accountScope, nextAccount); err != nil {
			return User{}, "", err
		}
		if err := saveRate(ctx, q, ipScope, nextIP); err != nil {
			return User{}, "", err
		}
		entries := []audit.Entry{{
			Action: "auth.login_failed", SubjectKind: "user", SubjectID: strings.ToLower(email),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"ip": ip},
		}}
		// Audit the lock only on the transition into it.
		newlyLocked := (rateBlocked(nextAccount, now) && !rateBlocked(accountRate, now)) ||
			(rateBlocked(nextIP, now) && !rateBlocked(ipRate, now))
		if newlyLocked {
			entries = append(entries, audit.Entry{
				Action: "auth.rate_limited", SubjectKind: "user", SubjectID: strings.ToLower(email),
				CorrelationID: httpx.CorrelationID(ctx),
				Detail:        map[string]any{"ip": ip},
			})
		}
		for _, e := range entries {
			if err := s.audit.Record(ctx, tx, e); err != nil {
				return User{}, "", err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return User{}, "", fmt.Errorf("identity: commit: %w", err)
		}
		return User{}, "", errBadCredentials
	}

	// Success: reset the account counter (the IP window stands — a spray
	// attack doesn't earn credit from one valid login).
	if err := q.DeleteLoginRate(ctx, accountScope); err != nil {
		return User{}, "", fmt.Errorf("identity: rate reset: %w", err)
	}
	token, err := s.mintSession(ctx, q, row.ID)
	if err != nil {
		return User{}, "", err
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: row.ID, Valid: true},
		Action: "auth.login", SubjectKind: "user", SubjectID: row.ID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
		Detail:        map[string]any{"ip": ip},
	})
	if err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, "", fmt.Errorf("identity: commit: %w", err)
	}
	return userFromRow(row), token, nil
}
