//go:build integration

package assets_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestRepos_CRUDWithGuards(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	modelCred, err := svc.CreateCredential(ctx, admin, "ant", assets.CredentialModelKey, []byte("sk"))
	if err != nil {
		t.Fatal(err)
	}

	repo, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "giiorg/caesar", CredentialID: forgeCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.DefaultBranch != "main" {
		t.Fatalf("default branch = %q, want main default", repo.DefaultBranch)
	}

	// Guards.
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "giiorg/caesar", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatal("duplicate (project, forge, slug) must conflict")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "no-slash", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("slug without owner segment must be invalid")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: "bitbucket", Slug: "a/b", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("unknown forge must be invalid")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("model_key credential on a repo must be invalid")
	}
	if _, err := svc.CreateRepo(ctx, admin, uuid.New(), assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown project must be not found")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: uuid.New(),
	}); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown credential must be not found")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: forgeCred.ID, DefaultBranch: "release branch",
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("default branch with whitespace must be invalid")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: forgeCred.ID, DefaultBranch: strings.Repeat("x", 201),
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("default branch over max length must be invalid")
	}

	// GitLab nested-group slug is legal.
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitLab, Slug: "group/sub/proj", CredentialID: forgeCred.ID,
	}); err != nil {
		t.Fatal(err)
	}

	branch := "trunk"
	upd, err := svc.UpdateRepo(ctx, admin, repo.ID, assets.RepoUpdate{DefaultBranch: &branch})
	if err != nil || upd.DefaultBranch != "trunk" || upd.Slug != "giiorg/caesar" {
		t.Fatalf("update = %+v, %v", upd, err)
	}
	if _, err := svc.UpdateRepo(ctx, admin, repo.ID, assets.RepoUpdate{CredentialID: &modelCred.ID}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("switching to a model_key credential must be invalid")
	}

	repos, err := svc.ListRepos(ctx, p.ID)
	if err != nil || len(repos) != 2 {
		t.Fatalf("list = %d, %v", len(repos), err)
	}
	if _, err := svc.GetRepo(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteRepo(ctx, admin, repo.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteRepo(ctx, admin, repo.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}

	for action, want := range map[string]int{"repo.created": 2, "repo.updated": 1, "repo.deleted": 1} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}

// TestDeleteRepo_RefusedWhileTasksExist pins the RESTRICT refusal an operator
// actually meets. The task row goes in raw: assets never imports the execution
// spine (arch law), so this package has no intake to create one through, and
// tasks.workflow_version_id is satisfiable only by a real workflow version.
func TestDeleteRepo_RefusedWhileTasksExist(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "giiorg/caesar", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second repo on the same project, referenced by nothing: it holds the
	// refusal to THIS repo's tasks. Without it, "delete always conflicts" and
	// "delete conflicts when a task points here" are the same test.
	spare, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "giiorg/spare", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTaskOn(t, pool, p.ID, repo.ID, admin.UserID)

	err = svc.DeleteRepo(ctx, admin, repo.ID)
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("class = %v (%v), want conflict while tasks exist", xerrors.ClassOf(err), err)
	}
	if msg := err.Error(); !strings.Contains(msg, "tasks") {
		t.Fatalf("message = %q, want it to name tasks", msg)
	}
	// Documentation, not a guard: the FK violation aborts the transaction
	// (measured — a write placed after it fails with 25P02), so the rollback
	// guarantees this whichever way the error is classified. An audit-count
	// assertion here would restate the same rollback and read as coverage of
	// the audit path, so there isn't one.
	if _, err := svc.GetRepo(ctx, repo.ID); err != nil {
		t.Fatalf("repo must survive a refused delete: %v", err)
	}

	if err := svc.DeleteRepo(ctx, admin, spare.ID); err != nil {
		t.Fatalf("unreferenced repo must still delete: %v", err)
	}
}

// seedTaskOn inserts the workflows → workflow_versions → tasks chain a
// tasks.repo_id reference needs, with the minimum each NOT NULL and CHECK
// accepts.
func seedTaskOn(t *testing.T, pool *pgxpool.Pool, projectID, repoID, requesterID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var versionID uuid.UUID
	err := pool.QueryRow(ctx, `
		WITH w AS (INSERT INTO workflows (name) VALUES ('wf') RETURNING id)
		INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		SELECT w.id, 1, 'h', '{}' FROM w RETURNING id`).Scan(&versionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		VALUES ($1, $2, $3, 'a task', $4, now())`,
		projectID, repoID, versionID, requesterID); err != nil {
		t.Fatal(err)
	}
}
