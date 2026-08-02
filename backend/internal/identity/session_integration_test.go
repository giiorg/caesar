//go:build integration

package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// Login lands in Task 10; until then sessions are minted through the
// MintSessionForTest seam, and everything else here exercises the public
// surface this task ships: AuthenticateSession, Logout, ChangePassword.

func bootstrapWithSession(t *testing.T) (*identity.Service, identity.AuthSession, string, *clock.Fake) {
	t.Helper()
	ctx := context.Background()
	svc, _, fake := newTestService(t)
	admin, err := svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.MintSessionForTest(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return svc, sess, token, fake
}

func TestAuthenticateSession_Lifecycle(t *testing.T) {
	ctx := context.Background()
	svc, sess, token, fake := bootstrapWithSession(t)

	if sess.Email != "root@example.com" || sess.Role != identity.RoleAdmin {
		t.Fatalf("principal = %+v", sess.Principal)
	}

	// Garbage token → unauthenticated.
	_, err := svc.AuthenticateSession(ctx, "not-a-real-token")
	wantClass(t, err, xerrors.ClassUnauthenticated)

	// Idle expiry: advance past 14 days without touching.
	fake.Advance(14*24*time.Hour + time.Minute)
	_, err = svc.AuthenticateSession(ctx, token)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}

func TestAuthenticateSession_IdleRefreshKeepsSessionAlive(t *testing.T) {
	ctx := context.Background()
	svc, _, token, fake := bootstrapWithSession(t)

	// Touch every 7 days: idle window (14d) never trips, absolute (90d) does.
	for day := 0; day < 84; day += 7 {
		fake.Advance(7 * 24 * time.Hour)
		if _, err := svc.AuthenticateSession(ctx, token); err != nil {
			t.Fatalf("day %d: %v", day+7, err)
		}
	}
	fake.Advance(7 * 24 * time.Hour) // day 91 > 90d absolute
	_, err := svc.AuthenticateSession(ctx, token)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}

func TestLogout_RevokesImmediately(t *testing.T) {
	ctx := context.Background()
	svc, sess, token, _ := bootstrapWithSession(t)

	if err := svc.Logout(ctx, sess); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AuthenticateSession(ctx, token)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}

func TestChangePassword_RotatesAndRevokesEverythingElse(t *testing.T) {
	ctx := context.Background()
	svc, sess, oldToken, _ := bootstrapWithSession(t)

	// A second device.
	otherToken, err := svc.MintSessionForTest(ctx, sess.UserID)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong current password refused.
	_, err = svc.ChangePassword(ctx, sess, "wrong current pw!", "a brand new password")
	wantClass(t, err, xerrors.ClassUnauthenticated)

	newToken, err := svc.ChangePassword(ctx, sess, testPassword, "a brand new password")
	if err != nil {
		t.Fatal(err)
	}
	for _, dead := range []string{oldToken, otherToken} {
		if _, err := svc.AuthenticateSession(ctx, dead); err == nil {
			t.Fatal("old sessions must be revoked after password change")
		}
	}
	if _, err := svc.AuthenticateSession(ctx, newToken); err != nil {
		t.Fatalf("replacement session: %v", err)
	}
}

func TestUpdateUser_PrivilegeChangeRevokesSessions(t *testing.T) {
	ctx := context.Background()
	svc, adminSess, _, _ := bootstrapWithSession(t)

	member, err := svc.CreateUser(ctx, adminSess.Principal, "m@example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	memberToken, err := svc.MintSessionForTest(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}

	toAdmin := identity.RoleAdmin
	if _, err := svc.UpdateUser(ctx, adminSess.Principal, member.ID, identity.UserUpdate{Role: &toAdmin}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AuthenticateSession(ctx, memberToken)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}
