//go:build integration

package identity

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const testPassword = "a long enough password"

func newTestService(t *testing.T) (*Service, *pgxpool.Pool, *clock.Fake) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	svc := NewService(pool, fake, audit.NewRecorder(fake), Params{
		SessionIdleTTL:     14 * 24 * time.Hour,
		SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	return svc, pool, fake
}

func wantClass(t *testing.T, err error, want xerrors.Class) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := xerrors.ClassOf(err); got != want {
		t.Fatalf("class = %v, want %v (err: %v)", got, want, err)
	}
}

func TestBootstrap_CreatesFirstAdminOnceOnly(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)

	admin, err := svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != RoleAdmin || !admin.Active {
		t.Fatalf("bootstrap user = %+v", admin)
	}

	_, err = svc.Bootstrap(ctx, "second@example.com", testPassword)
	wantClass(t, err, xerrors.ClassConflict)

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "user.bootstrapped")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ActorUserID.Valid {
		t.Fatalf("bootstrap audit rows = %+v (want one, system actor)", rows)
	}
}

func TestCreateUser_ValidatesAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin, err := svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	actor := Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}

	tests := []struct {
		name      string
		email, pw string
		role      Role
		wantClass xerrors.Class
	}{
		{"bad email", "not-an-email", testPassword, RoleMember, xerrors.ClassInvalid},
		{"short password", "ok@example.com", "tooshort", RoleMember, xerrors.ClassInvalid},
		{"bad role", "ok@example.com", testPassword, Role("root"), xerrors.ClassInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreateUser(ctx, actor, tt.email, tt.pw, tt.role)
			wantClass(t, err, tt.wantClass)
		})
	}

	member, err := svc.CreateUser(ctx, actor, "Member@Example.com", testPassword, RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate detection is case-insensitive (unique on lower(email)).
	_, err = svc.CreateUser(ctx, actor, "member@example.COM", testPassword, RoleMember)
	wantClass(t, err, xerrors.ClassConflict)

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "user.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].ActorUserID.Valid || rows[0].ActorUserID.UUID != admin.ID {
		t.Fatalf("user.created audit = %+v", rows)
	}
	if rows[0].SubjectID != member.ID.String() {
		t.Fatalf("subject = %q, want member id", rows[0].SubjectID)
	}
}

func TestUpdateUser_GuardsAndRoleChange(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	member, err := svc.CreateUser(ctx, actor, "m@example.com", testPassword, RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	// Admin cannot touch their own role/active (lockout guard).
	adminRole := RoleMember
	_, err = svc.UpdateUser(ctx, actor, admin.ID, UserUpdate{Role: &adminRole})
	wantClass(t, err, xerrors.ClassForbidden)

	// Promote the member.
	toAdmin := RoleAdmin
	updated, err := svc.UpdateUser(ctx, actor, member.ID, UserUpdate{Role: &toAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != RoleAdmin {
		t.Fatalf("role = %s", updated.Role)
	}

	// Unknown user → not found.
	_, err = svc.UpdateUser(ctx, actor, uuid.New(), UserUpdate{Role: &toAdmin})
	wantClass(t, err, xerrors.ClassNotFound)
}

func TestUpdateUser_LastActiveAdminGuard(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	second, err := svc.CreateUser(ctx, actor, "second@example.com", testPassword, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	// Deactivate the second admin: fine (root remains).
	inactive := false
	if _, err := svc.UpdateUser(ctx, actor, second.ID, UserUpdate{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	// Now try to demote root via the (still-active-in-principal) second
	// admin — the guard must count *active* admins, so demoting the last
	// active admin fails even when another, deactivated admin row exists.
	secondActor := Principal{UserID: second.ID, Email: second.Email, Role: RoleAdmin}
	demote := RoleMember
	_, err = svc.UpdateUser(ctx, secondActor, admin.ID, UserUpdate{Role: &demote})
	wantClass(t, err, xerrors.ClassConflict)
}

func TestAuthorize_LadderAndAdminBypass(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	member, err := svc.CreateUser(ctx, actor, "m@example.com", testPassword, RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	var projectID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('demo') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants (user_id, project_id, level) VALUES ($1, $2, 'run')`, member.ID, projectID); err != nil {
		t.Fatal(err)
	}

	memberP := Principal{UserID: member.ID, Email: member.Email, Role: RoleMember}
	tests := []struct {
		name string
		p    Principal
		min  GrantLevel
		want bool
	}{
		{"member at granted level", memberP, GrantRun, true},
		{"member below granted level", memberP, GrantView, true},
		{"member above granted level", memberP, GrantApprove, false},
		{"admin bypasses grants", actor, GrantManage, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Authorize(ctx, tt.p, projectID, tt.min)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Authorize = %v, want %v", got, tt.want)
			}
		})
	}

	// No grant row at all → false, not an error.
	ok, err := svc.Authorize(ctx, memberP, uuid.New(), GrantView)
	if err != nil || ok {
		t.Fatalf("ungranted project: ok=%v err=%v", ok, err)
	}

	grants, err := svc.GrantsForUser(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].Level != GrantRun || grants[0].ProjectID != projectID {
		t.Fatalf("grants = %+v", grants)
	}
}

func TestBootstrap_ConcurrentCallsCreateOneAdmin(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)

	// Start signal and wait group for coordinating concurrent calls.
	var start sync.WaitGroup
	start.Add(2)
	var done sync.WaitGroup
	done.Add(2)

	var result1, result2 User
	var err1, err2 error

	// Goroutine 1: bootstrap first admin.
	go func() {
		defer done.Done()
		start.Done()
		start.Wait()
		result1, err1 = svc.Bootstrap(ctx, "admin1@example.com", testPassword)
	}()

	// Goroutine 2: bootstrap second admin concurrently.
	go func() {
		defer done.Done()
		start.Done()
		start.Wait()
		result2, err2 = svc.Bootstrap(ctx, "admin2@example.com", testPassword)
	}()

	done.Wait()

	// Exactly one must succeed, the other must fail with ClassConflict.
	successCount := 0
	if err1 == nil {
		successCount++
	}
	if err2 == nil {
		successCount++
	}
	if successCount != 1 {
		t.Fatalf("expected exactly one success, got %d (err1=%v, err2=%v)", successCount, err1, err2)
	}

	// The one that succeeded must have role admin.
	if err1 == nil && result1.Role != RoleAdmin {
		t.Fatalf("bootstrap 1: role = %s, want admin", result1.Role)
	}
	if err2 == nil && result2.Role != RoleAdmin {
		t.Fatalf("bootstrap 2: role = %s, want admin", result2.Role)
	}

	// The one that failed must have ClassConflict.
	if err1 != nil {
		wantClass(t, err1, xerrors.ClassConflict)
	}
	if err2 != nil {
		wantClass(t, err2, xerrors.ClassConflict)
	}

	// Exactly one admin row must exist.
	var adminCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&adminCount); err != nil {
		t.Fatal(err)
	}
	if adminCount != 1 {
		t.Fatalf("admin count = %d, want 1", adminCount)
	}
}

func TestUpdateUser_ConcurrentDemotionsKeepOneActiveAdmin(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)

	// Bootstrap admin A.
	admin, _ := svc.Bootstrap(ctx, "adminA@example.com", testPassword)
	actor := Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}

	// Create admin B.
	adminB, err := svc.CreateUser(ctx, actor, "adminB@example.com", testPassword, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	// Concurrent UpdateUser calls:
	// - Admin A demotes Admin B
	// - Admin B demotes Admin A (using outdated principal, still marked as admin in principal)
	var start sync.WaitGroup
	start.Add(2)
	var done sync.WaitGroup
	done.Add(2)

	var errA, errB error
	demote := RoleMember

	// Goroutine 1: A demotes B.
	go func() {
		defer done.Done()
		start.Done()
		start.Wait()
		_, errA = svc.UpdateUser(ctx, actor, adminB.ID, UserUpdate{Role: &demote})
	}()

	// Goroutine 2: B (using outdated principal) demotes A.
	go func() {
		defer done.Done()
		start.Done()
		start.Wait()
		bActor := Principal{UserID: adminB.ID, Email: adminB.Email, Role: RoleAdmin}
		_, errB = svc.UpdateUser(ctx, bActor, admin.ID, UserUpdate{Role: &demote})
	}()

	done.Wait()

	// Exactly one must succeed, the other must fail with ClassConflict.
	successCount := 0
	if errA == nil {
		successCount++
	}
	if errB == nil {
		successCount++
	}
	if successCount != 1 {
		t.Fatalf("expected exactly one success, got %d (errA=%v, errB=%v)", successCount, errA, errB)
	}

	// The one that failed must have ClassConflict.
	if errA != nil {
		wantClass(t, errA, xerrors.ClassConflict)
	}
	if errB != nil {
		wantClass(t, errB, xerrors.ClassConflict)
	}

	// At least one user must still be admin + active.
	var activeAdminCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND active`).Scan(&activeAdminCount); err != nil {
		t.Fatal(err)
	}
	if activeAdminCount < 1 {
		t.Fatalf("active admin count = %d, want >= 1", activeAdminCount)
	}
}
