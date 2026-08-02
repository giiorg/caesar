//go:build integration

package identity

import (
	"context"
	"fmt"
	"sync"
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

func TestLogin_ConcurrentFirstFailuresBothCount(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	email := "concurrent@example.com"
	ip := "192.0.2.1"
	accountScope := "account:" + email

	// Two goroutines concurrently attempt login with wrong password.
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, _, _ = svc.Login(ctx, email, "wrong password", ip)
		}()
	}
	wg.Wait()

	// Both failures must be counted in the login_rate row.
	rows, err := pool.Query(ctx,
		"SELECT failures FROM login_rate WHERE scope = $1",
		accountScope,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("login_rate row not found")
	}
	var failures int32
	if err := rows.Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 2 {
		t.Fatalf("concurrent failures = %d, want 2", failures)
	}
}

func TestLogin_OversizedPasswordIsRateLimitedAndAudited(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	// Login with a password way over the limit (>512 chars).
	oversized := string(make([]byte, 600))
	for i := range oversized {
		oversized := []rune(oversized)
		oversized[i] = 'x'
	}
	_, _, err := svc.Login(ctx, "root@example.com", oversized, "192.0.2.2")
	wantClass(t, err, xerrors.ClassUnauthenticated)

	// IP rate counter incremented.
	rows, err := pool.Query(ctx,
		"SELECT failures FROM login_rate WHERE scope = $1",
		"ip:192.0.2.2",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("login_rate row for IP not found")
	}
	var failures int32
	if err := rows.Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("ip failures = %d, want 1", failures)
	}

	// Audit entry recorded.
	auditRows, aerr := audit.New(pool).ListAuditEntriesByAction(ctx, "auth.login_failed")
	if aerr != nil || len(auditRows) != 1 {
		t.Fatalf("auth.login_failed audit rows = %d (want 1), err %v", len(auditRows), aerr)
	}
}
