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
