package assets

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func repoFromRow(r store.Repo) Repo {
	return Repo{
		ID: r.ID, ProjectID: r.ProjectID, Forge: ForgeKind(r.ForgeKind), Slug: r.Slug,
		DefaultBranch: r.DefaultBranch, CredentialID: r.CredentialID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// NewRepo is the create payload; empty DefaultBranch means "main".
type NewRepo struct {
	Forge         ForgeKind
	Slug          string
	DefaultBranch string
	CredentialID  uuid.UUID
}

// RepoUpdate is a partial update; nil fields are left unchanged.
type RepoUpdate struct {
	Slug          *string
	DefaultBranch *string
	CredentialID  *uuid.UUID
}

func validateSlug(slug string) error {
	if len(slug) > maxSlugLen || !slugPattern.MatchString(slug) {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "slug", Message: "must be owner/name (letters, digits, . _ -; gitlab groups may nest)",
		})
	}
	return nil
}

func validateBranch(branch string) error {
	if branch == "" || len(branch) > maxBranchLen || strings.ContainsAny(branch, " \t\r\n") {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "default_branch", Message: fmt.Sprintf("must be 1-%d bytes without whitespace", maxBranchLen),
		})
	}
	return nil
}

// requireCredentialKindIn loads the referenced credential in-tx and
// rejects a kind outside the accepted set — repos take a forge_token,
// while each harness kind accepts the credential kinds its container can
// actually authenticate with.
func requireCredentialKindIn(ctx context.Context, q *store.Queries, id uuid.UUID, wants ...CredentialKind) error {
	kind, err := getCredentialKind(ctx, q, id)
	if err != nil {
		return err
	}
	if slices.Contains(wants, kind) {
		return nil
	}
	return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
		Field: "credential_id", Message: "credential kind must be one of " + joinKinds(wants),
	})
}

func (s *Service) CreateRepo(ctx context.Context, actor authn.Principal, projectID uuid.UUID, in NewRepo) (Repo, error) {
	if !in.Forge.Valid() {
		return Repo{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "forge", Message: "must be github or gitlab"})
	}
	if err := validateSlug(in.Slug); err != nil {
		return Repo{}, err
	}
	branch := in.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	if err := validateBranch(branch); err != nil {
		return Repo{}, err
	}

	var repo Repo
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := requireCredentialKindIn(ctx, q, in.CredentialID, CredentialForgeToken); err != nil {
			return err
		}
		row, err := q.CreateRepo(ctx, store.CreateRepoParams{
			ProjectID: projectID, ForgeKind: string(in.Forge), Slug: in.Slug,
			DefaultBranch: branch, CredentialID: in.CredentialID,
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "repo already registered on this project")
		}
		if constraint, fk := fkViolation(err); fk {
			if constraint == "repos_project_id_fkey" {
				return xerrors.E(xerrors.ClassNotFound, "no such project")
			}
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: create repo: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "repo.created", SubjectKind: "repo", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"project_id": projectID.String(), "forge": string(in.Forge), "slug": in.Slug},
		}); err != nil {
			return err
		}
		repo = repoFromRow(row)
		return nil
	})
	if err != nil {
		return Repo{}, err
	}
	return repo, nil
}

func (s *Service) ListRepos(ctx context.Context, projectID uuid.UUID) ([]Repo, error) {
	rows, err := store.New(s.pool).ListReposByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("assets: list repos: %w", err)
	}
	out := make([]Repo, 0, len(rows))
	for _, r := range rows {
		out = append(out, repoFromRow(r))
	}
	return out, nil
}

func (s *Service) GetRepo(ctx context.Context, id uuid.UUID) (Repo, error) {
	row, err := store.New(s.pool).GetRepo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Repo{}, xerrors.E(xerrors.ClassNotFound, "no such repo")
	}
	if err != nil {
		return Repo{}, fmt.Errorf("assets: get repo: %w", err)
	}
	return repoFromRow(row), nil
}

func (s *Service) UpdateRepo(ctx context.Context, actor authn.Principal, id uuid.UUID, upd RepoUpdate) (Repo, error) {
	var repo Repo
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetRepo(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such repo")
		}
		if err != nil {
			return fmt.Errorf("assets: get repo: %w", err)
		}
		slug, branch, credID := current.Slug, current.DefaultBranch, current.CredentialID
		if upd.Slug != nil {
			if err := validateSlug(*upd.Slug); err != nil {
				return err
			}
			slug = *upd.Slug
		}
		if upd.DefaultBranch != nil {
			if err := validateBranch(*upd.DefaultBranch); err != nil {
				return err
			}
			branch = *upd.DefaultBranch
		}
		if upd.CredentialID != nil {
			if err := requireCredentialKindIn(ctx, q, *upd.CredentialID, CredentialForgeToken); err != nil {
				return err
			}
			credID = *upd.CredentialID
		}
		row, err := q.UpdateRepo(ctx, store.UpdateRepoParams{
			ID: id, Slug: slug, DefaultBranch: branch, CredentialID: credID, UpdatedAt: s.clock.Now(),
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "repo already registered on this project")
		}
		if _, fk := fkViolation(err); fk {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: update repo: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "repo.updated", SubjectKind: "repo", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"slug": slug},
		}); err != nil {
			return err
		}
		repo = repoFromRow(row)
		return nil
	})
	if err != nil {
		return Repo{}, err
	}
	return repo, nil
}

// repoInUse turns the RESTRICT constraint Postgres reported into a refusal an
// operator can act on. Exactly three foreign keys reference repos.id, all
// ON DELETE RESTRICT: tasks.repo_id, approved_patches.repo_id and
// publish_intents.repo_id.
//
// Only the tasks branch is reachable from a fixture, and that is a fact about
// the schema rather than a gap in the tests: every publish row descends from a
// run, every run from a task, and nothing in this system deletes a task — so a
// repo holding publish records always holds tasks too, and when more than one
// RESTRICT is violated Postgres reports whichever it checks first. The publish
// wording is therefore a backstop that stays correct if that ever changes; it
// is pinned by the unit test rather than by an integration fixture.
func repoInUse(constraint string) error {
	if constraint == "tasks_repo_id_fkey" {
		return xerrors.E(xerrors.ClassConflict, "this repository is used by existing tasks")
	}
	return xerrors.E(xerrors.ClassConflict, "this repository has publish records; it cannot be deleted")
}

func (s *Service) DeleteRepo(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteRepo(ctx, id)
		// Before the generic wrap, and before n == 0: a refused delete removes
		// no row, so reading n first would report a live repo as missing.
		if constraint, fk := fkViolation(err); fk {
			return repoInUse(constraint)
		}
		if err != nil {
			return fmt.Errorf("assets: delete repo: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such repo")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "repo.deleted", SubjectKind: "repo", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}
