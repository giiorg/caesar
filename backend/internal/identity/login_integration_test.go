//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const loginIP = "203.0.113.9"

func TestLogin_SuccessMintsSessionAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	user, token, err := svc.Login(ctx, "Root@Example.com", testPassword, loginIP) // case-insensitive email
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserID != user.ID {
		t.Fatal("session principal mismatch")
	}
	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "auth.login")
	if err != nil || len(rows) != 1 {
		t.Fatalf("auth.login audit rows = %d, err %v", len(rows), err)
	}
}

func TestLogin_WrongPasswordAndUnknownEmailAreIndistinguishable(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	_, _, errWrong := svc.Login(ctx, "root@example.com", "totally wrong pw", loginIP)
	_, _, errUnknown := svc.Login(ctx, "ghost@example.com", "totally wrong pw", loginIP)
	wantClass(t, errWrong, xerrors.ClassUnauthenticated)
	wantClass(t, errUnknown, xerrors.ClassUnauthenticated)
	if errWrong.Error() != errUnknown.Error() {
		t.Fatalf("credential errors must be identical: %q vs %q", errWrong, errUnknown)
	}
}

func TestLogin_AccountLockoutAndRecovery(t *testing.T) {
	ctx := context.Background()
	svc, pool, fake := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		_, _, err := svc.Login(ctx, "root@example.com", "wrong password!!", loginIP)
		wantClass(t, err, xerrors.ClassUnauthenticated)
	}
	// 5 consecutive failures → locked even with the CORRECT password.
	_, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP)
	wantClass(t, err, xerrors.ClassRateLimited)

	rows, aerr := audit.New(pool).ListAuditEntriesByAction(ctx, "auth.rate_limited")
	if aerr != nil || len(rows) != 1 {
		t.Fatalf("auth.rate_limited audit rows = %d (want 1: on transition only), err %v", len(rows), aerr)
	}

	// Lock at 5 failures is 30s; past it, login succeeds and resets.
	fake.Advance(31 * time.Second)
	if _, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP); err != nil {
		t.Fatal(err)
	}
	// Counter was reset: 4 fresh failures don't lock.
	for i := 0; i < 4; i++ {
		_, _, err := svc.Login(ctx, "root@example.com", "wrong password!!", loginIP)
		wantClass(t, err, xerrors.ClassUnauthenticated)
	}
	if _, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP); err != nil {
		t.Fatal(err)
	}
}

func TestLogin_IPLockoutSpansAccounts(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	// 30 failures from one IP across many (unknown) accounts.
	for i := 0; i < 30; i++ {
		email := fmt.Sprintf("spray%d@example.com", i)
		_, _, err := svc.Login(ctx, email, "wrong password!!", loginIP)
		wantClass(t, err, xerrors.ClassUnauthenticated)
	}
	_, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP)
	wantClass(t, err, xerrors.ClassRateLimited)

	// A different IP is unaffected.
	if _, _, err := svc.Login(ctx, "root@example.com", testPassword, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
}
