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

// seedProject inserts a project row directly — identity never imports the
// assets context, and grants only need the FK target to exist.
func seedProject(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO projects (name) VALUES ($1) RETURNING id", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGrants_SetListDeleteWithGuards(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
		SessionIdleTTL: time.Hour, SessionAbsoluteTTL: 2 * time.Hour,
	})
	admin, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	member, err := svc.CreateUser(ctx, identity.Principal{UserID: admin.ID, Role: identity.RoleAdmin}, "m@example.com", "a long enough password", identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: identity.RoleAdmin}
	project := seedProject(t, pool, "p")

	if err := svc.SetGrant(ctx, actor, project, member.ID, identity.GrantRun); err != nil {
		t.Fatal(err)
	}
	// Upsert raises in place.
	if err := svc.SetGrant(ctx, actor, project, member.ID, identity.GrantManage); err != nil {
		t.Fatal(err)
	}
	grants, err := svc.ListGrants(ctx, project)
	if err != nil || len(grants) != 1 || grants[0].Level != identity.GrantManage || grants[0].Email != "m@example.com" {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
	// Authorize sees it.
	if ok, err := svc.Authorize(ctx, identity.Principal{UserID: member.ID, Role: identity.RoleMember}, project, identity.GrantManage); err != nil || !ok {
		t.Fatalf("authorize = %v, %v", ok, err)
	}

	// Guards.
	if err := svc.SetGrant(ctx, actor, project, member.ID, "owner"); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("bad level must be invalid")
	}
	if err := svc.SetGrant(ctx, actor, project, admin.ID, identity.GrantView); xerrors.ClassOf(err) != xerrors.ClassForbidden {
		t.Fatal("changing own grant must be forbidden")
	}
	if err := svc.SetGrant(ctx, actor, project, uuid.New(), identity.GrantView); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown user must be not found")
	}
	if err := svc.SetGrant(ctx, actor, uuid.New(), member.ID, identity.GrantView); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown project must be not found (fk backstop)")
	}

	if err := svc.DeleteGrant(ctx, actor, project, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGrant(ctx, actor, project, member.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}

	for _, action := range []string{"grant.set", "grant.deleted"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_entries WHERE action = $1", action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (action == "grant.set" && n != 2) || (action == "grant.deleted" && n != 1) {
			t.Errorf("%s audits = %d", action, n)
		}
	}
}
