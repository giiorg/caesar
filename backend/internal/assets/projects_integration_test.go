//go:build integration

package assets_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestProjects_CRUDWithAudit(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	p, err := svc.CreateProject(ctx, admin, "  caesar  ", "dogfood project")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "caesar" || p.Description != "dogfood project" {
		t.Fatalf("project = %+v (name must be trimmed)", p)
	}

	if _, err := svc.CreateProject(ctx, admin, "caesar", ""); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("duplicate name class = %v, want conflict", xerrors.ClassOf(err))
	}
	if _, err := svc.CreateProject(ctx, admin, "", ""); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("empty name must be invalid")
	}

	name := "caesar-renamed"
	upd, err := svc.UpdateProject(ctx, admin, p.ID, assets.ProjectUpdate{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "caesar-renamed" || upd.Description != "dogfood project" {
		t.Fatalf("update = %+v (unset fields must persist)", upd)
	}

	got, err := svc.GetProject(ctx, p.ID)
	if err != nil || got.Name != "caesar-renamed" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	all, err := svc.ListProjects(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %d projects, %v", len(all), err)
	}
	some, err := svc.ListProjectsByIDs(ctx, []uuid.UUID{p.ID})
	if err != nil || len(some) != 1 {
		t.Fatalf("list by ids = %d, %v", len(some), err)
	}
	if some, err := svc.ListProjectsByIDs(ctx, nil); err != nil || len(some) != 0 {
		t.Fatalf("empty ids must list nothing, got %d, %v", len(some), err)
	}

	if err := svc.DeleteProject(ctx, admin, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProject(ctx, admin, p.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}
	if _, err := svc.GetProject(ctx, p.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("get after delete must be not found")
	}

	for action, want := range map[string]int{"project.created": 1, "project.updated": 1, "project.deleted": 1} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}

func TestDeleteProject_RefusedWhileReposExist(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	p, err := svc.CreateProject(ctx, admin, "with-repo", "")
	if err != nil {
		t.Fatal(err)
	}
	// Raw rows: the repos/credentials services arrive in later tasks.
	credID := uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext) VALUES ($1, 'gh', 'forge_token', 'test', '\\x00', '\\x00')",
		credID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO repos (project_id, forge_kind, slug, credential_id) VALUES ($1, 'github', 'giiorg/caesar', $2)",
		p.ID, credID); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteProject(ctx, admin, p.ID); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("class = %v, want conflict while repos exist", xerrors.ClassOf(err))
	}
	ok, err := svc.ProjectExists(ctx, p.ID)
	if err != nil || !ok {
		t.Fatalf("project must survive: %v %v", ok, err)
	}
}
