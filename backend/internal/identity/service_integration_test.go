//go:build integration

package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const testPassword = "a long enough password"

func newTestService(t *testing.T) (*identity.Service, *pgxpool.Pool, *clock.Fake) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
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
	if admin.Role != identity.RoleAdmin || !admin.Active {
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
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}

	tests := []struct {
		name      string
		email, pw string
		role      identity.Role
		wantClass xerrors.Class
	}{
		{"bad email", "not-an-email", testPassword, identity.RoleMember, xerrors.ClassInvalid},
		{"short password", "ok@example.com", "tooshort", identity.RoleMember, xerrors.ClassInvalid},
		{"bad role", "ok@example.com", testPassword, identity.Role("root"), xerrors.ClassInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreateUser(ctx, actor, tt.email, tt.pw, tt.role)
			wantClass(t, err, tt.wantClass)
		})
	}

	member, err := svc.CreateUser(ctx, actor, "Member@Example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate detection is case-insensitive (unique on lower(email)).
	_, err = svc.CreateUser(ctx, actor, "member@example.COM", testPassword, identity.RoleMember)
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
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	member, err := svc.CreateUser(ctx, actor, "m@example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	// Admin cannot touch their own role/active (lockout guard).
	adminRole := identity.RoleMember
	_, err = svc.UpdateUser(ctx, actor, admin.ID, identity.UserUpdate{Role: &adminRole})
	wantClass(t, err, xerrors.ClassForbidden)

	// Promote the member.
	toAdmin := identity.RoleAdmin
	updated, err := svc.UpdateUser(ctx, actor, member.ID, identity.UserUpdate{Role: &toAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != identity.RoleAdmin {
		t.Fatalf("role = %s", updated.Role)
	}

	// Unknown user → not found.
	_, err = svc.UpdateUser(ctx, actor, uuid.New(), identity.UserUpdate{Role: &toAdmin})
	wantClass(t, err, xerrors.ClassNotFound)
}

func TestUpdateUser_LastActiveAdminGuard(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	second, err := svc.CreateUser(ctx, actor, "second@example.com", testPassword, identity.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	// Deactivate the second admin: fine (root remains).
	inactive := false
	if _, err := svc.UpdateUser(ctx, actor, second.ID, identity.UserUpdate{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	// Now try to demote root via the (still-active-in-principal) second
	// admin — the guard must count *active* admins, so demoting the last
	// active admin fails even when another, deactivated admin row exists.
	secondActor := identity.Principal{UserID: second.ID, Email: second.Email, Role: identity.RoleAdmin}
	demote := identity.RoleMember
	_, err = svc.UpdateUser(ctx, secondActor, admin.ID, identity.UserUpdate{Role: &demote})
	wantClass(t, err, xerrors.ClassConflict)
}

func TestAuthorize_LadderAndAdminBypass(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	member, err := svc.CreateUser(ctx, actor, "m@example.com", testPassword, identity.RoleMember)
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

	memberP := identity.Principal{UserID: member.ID, Email: member.Email, Role: identity.RoleMember}
	tests := []struct {
		name string
		p    identity.Principal
		min  identity.GrantLevel
		want bool
	}{
		{"member at granted level", memberP, identity.GrantRun, true},
		{"member below granted level", memberP, identity.GrantView, true},
		{"member above granted level", memberP, identity.GrantApprove, false},
		{"admin bypasses grants", actor, identity.GrantManage, true},
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
	ok, err := svc.Authorize(ctx, memberP, uuid.New(), identity.GrantView)
	if err != nil || ok {
		t.Fatalf("ungranted project: ok=%v err=%v", ok, err)
	}

	grants, err := svc.GrantsForUser(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].Level != identity.GrantRun || grants[0].ProjectID != projectID {
		t.Fatalf("grants = %+v", grants)
	}
}
