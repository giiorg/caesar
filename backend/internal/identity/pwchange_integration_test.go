//go:build integration

package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestChangePassword_ThrottlesAndAuditsWrongCurrentPassword(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	user, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.MintSessionForTest(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	// Five wrong guesses: all unauthenticated-classed, each audited.
	for i := 0; i < 5; i++ {
		_, err := svc.ChangePassword(ctx, sess, "wrong current pw!", "a brand new password")
		if xerrors.ClassOf(err) != xerrors.ClassUnauthenticated {
			t.Fatalf("attempt %d: class = %v, want unauthenticated", i+1, xerrors.ClassOf(err))
		}
	}
	var failed int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_entries WHERE action = 'auth.password_change_failed'").Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 5 {
		t.Fatalf("failed audits = %d, want 5", failed)
	}
	var limited int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_entries WHERE action = 'auth.rate_limited' AND detail->>'scope' = 'pwchange'").Scan(&limited); err != nil {
		t.Fatal(err)
	}
	if limited != 1 {
		t.Fatalf("rate_limited audits = %d, want 1 (transition only)", limited)
	}

	// Locked now — even the CORRECT current password is refused.
	if _, err := svc.ChangePassword(ctx, sess, "a long enough password", "a brand new password"); xerrors.ClassOf(err) != xerrors.ClassRateLimited {
		t.Fatalf("locked class = %v, want rate limited", xerrors.ClassOf(err))
	}

	// Lock expires (5th failure locks for 30s); success resets the scope.
	fake.Advance(31 * time.Second)
	if _, err := svc.ChangePassword(ctx, sess, "a long enough password", "a brand new password"); err != nil {
		t.Fatalf("after lock expiry: %v", err)
	}
	var rateRows int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM login_rate WHERE scope LIKE 'pwchange:%'").Scan(&rateRows); err != nil {
		t.Fatal(err)
	}
	if rateRows != 0 {
		t.Fatalf("pwchange rate rows after success = %d, want 0", rateRows)
	}
}
