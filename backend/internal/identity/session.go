package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const (
	sessionTokenLen      = 32
	sessionTouchInterval = 5 * time.Minute
)

var errBadSession = xerrors.E(xerrors.ClassUnauthenticated, "invalid or expired session")

func newSessionToken() (token string, hash []byte, err error) {
	raw := make([]byte, sessionTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("identity: session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// mintSession inserts a fresh session row inside the caller's transaction
// and returns the bearer token (never stored).
func (s *Service) mintSession(ctx context.Context, q *store.Queries, userID uuid.UUID) (string, error) {
	token, hash, err := newSessionToken()
	if err != nil {
		return "", err
	}
	now := s.clock.Now()
	_, err = q.InsertSession(ctx, store.InsertSessionParams{
		UserID:    userID,
		TokenHash: hash,
		CreatedAt: now,
		ExpiresAt: now.Add(s.params.SessionAbsoluteTTL),
	})
	if err != nil {
		return "", fmt.Errorf("identity: insert session: %w", err)
	}
	return token, nil
}

// AuthenticateSession resolves a bearer token to its principal. Any
// failure mode is the same client-facing error (no oracle).
func (s *Service) AuthenticateSession(ctx context.Context, token string) (AuthSession, error) {
	row, err := store.New(s.pool).GetSessionByTokenHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthSession{}, errBadSession
	}
	if err != nil {
		return AuthSession{}, fmt.Errorf("identity: session lookup: %w", err)
	}
	now := s.clock.Now()
	switch {
	case row.RevokedAt != nil:
		return AuthSession{}, errBadSession
	case !now.Before(row.ExpiresAt):
		return AuthSession{}, errBadSession
	case !now.Before(row.LastSeenAt.Add(s.params.SessionIdleTTL)):
		return AuthSession{}, errBadSession
	case !row.Active:
		return AuthSession{}, errBadSession
	}
	if now.Sub(row.LastSeenAt) >= sessionTouchInterval {
		if err := store.New(s.pool).TouchSession(ctx, store.TouchSessionParams{ID: row.ID, LastSeenAt: now}); err != nil {
			return AuthSession{}, fmt.Errorf("identity: touch session: %w", err)
		}
	}
	return AuthSession{
		Principal: Principal{UserID: row.UserID, Email: row.Email, Role: Role(row.Role)},
		SessionID: row.ID,
	}, nil
}

// Logout revokes the calling session.
func (s *Service) Logout(ctx context.Context, sess AuthSession) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		now := s.clock.Now()
		if err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: sess.SessionID, RevokedAt: &now}); err != nil {
			return fmt.Errorf("identity: revoke session: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
			Action: "auth.logout", SubjectKind: "session", SubjectID: sess.SessionID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		}); err != nil {
			return err
		}
		return nil
	})
}

// ChangePassword verifies the current password, rewrites the hash,
// revokes every session of the user, and mints a replacement for the
// caller (rotation on privilege change, spec §6). Current-password
// guesses are throttled through login_rate scope "pwchange:<user_id>"
// with the account ladder and audited — a stolen session must not be a
// free brute-force oracle. Failure paths COMMIT their rate/audit rows,
// so this keeps an explicit transaction (not pg.WithTx).
func (s *Service) ChangePassword(ctx context.Context, sess AuthSession, current, newPassword string) (string, error) {
	if err := validatePassword(newPassword); err != nil {
		return "", err
	}
	scope := "pwchange:" + sess.UserID.String()
	now := s.clock.Now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	if err := q.EnsureLoginRate(ctx, store.EnsureLoginRateParams{Scope: scope, WindowStartedAt: now}); err != nil {
		return "", fmt.Errorf("identity: ensure pwchange rate: %w", err)
	}
	rate, err := loadRate(ctx, q, scope)
	if err != nil {
		return "", err
	}
	if rateBlocked(rate, now) {
		return "", errLoginLocked
	}

	row, err := q.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return "", fmt.Errorf("identity: get user: %w", err)
	}
	ok := false
	if len(current) <= MaxPasswordLen {
		ok, err = VerifyPassword(row.PasswordHash, current)
		if err != nil {
			return "", fmt.Errorf("identity: verify: %w", err)
		}
	} else {
		dummyVerify(current)
	}

	if !ok {
		next := rateFail(rateAccount, rate, now)
		if err := saveRate(ctx, q, scope, next); err != nil {
			return "", err
		}
		entries := []audit.Entry{{
			Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
			Action: "auth.password_change_failed", SubjectKind: "user", SubjectID: sess.UserID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		}}
		if rateBlocked(next, now) && !rateBlocked(rate, now) {
			entries = append(entries, audit.Entry{
				Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
				Action: "auth.rate_limited", SubjectKind: "user", SubjectID: sess.UserID.String(),
				CorrelationID: httpx.CorrelationID(ctx),
				Detail:        map[string]any{"scope": "pwchange"},
			})
		}
		for _, e := range entries {
			if err := s.audit.Record(ctx, tx, e); err != nil {
				return "", err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("identity: commit: %w", err)
		}
		s.purgeStaleLoginRate(ctx)
		return "", xerrors.E(xerrors.ClassUnauthenticated, "current password is incorrect")
	}

	if err := q.DeleteLoginRate(ctx, scope); err != nil {
		return "", fmt.Errorf("identity: rate reset: %w", err)
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return "", err
	}
	if err := q.UpdateUserPassword(ctx, store.UpdateUserPasswordParams{ID: sess.UserID, PasswordHash: hash, UpdatedAt: now}); err != nil {
		return "", fmt.Errorf("identity: update password: %w", err)
	}
	if err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: sess.UserID, RevokedAt: &now}); err != nil {
		return "", fmt.Errorf("identity: revoke sessions: %w", err)
	}
	token, err := s.mintSession(ctx, q, sess.UserID)
	if err != nil {
		return "", err
	}
	if err := s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
		Action: "auth.password_changed", SubjectKind: "user", SubjectID: sess.UserID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("identity: commit: %w", err)
	}
	return token, nil
}
