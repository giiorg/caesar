package assets

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func projectFromRow(r store.Project) Project {
	return Project{ID: r.ID, Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// ProjectUpdate is a partial update; nil fields are left unchanged.
type ProjectUpdate struct {
	Name        *string
	Description *string
}

func (s *Service) CreateProject(ctx context.Context, actor authn.Principal, name, description string) (Project, error) {
	name, err := validateName("name", name)
	if err != nil {
		return Project{}, err
	}
	if err := validateDescription(description); err != nil {
		return Project{}, err
	}

	var p Project
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).CreateProject(ctx, store.CreateProjectParams{Name: name, Description: description})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "project name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: create project: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "project.created", SubjectKind: "project", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		p = projectFromRow(row)
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Service) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := store.New(s.pool).ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("assets: list projects: %w", err)
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectFromRow(r))
	}
	return out, nil
}

func (s *Service) ListProjectsByIDs(ctx context.Context, ids []uuid.UUID) ([]Project, error) {
	if len(ids) == 0 {
		return []Project{}, nil
	}
	rows, err := store.New(s.pool).ListProjectsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("assets: list projects by ids: %w", err)
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectFromRow(r))
	}
	return out, nil
}

func (s *Service) GetProject(ctx context.Context, id uuid.UUID) (Project, error) {
	row, err := store.New(s.pool).GetProject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, xerrors.E(xerrors.ClassNotFound, "no such project")
	}
	if err != nil {
		return Project{}, fmt.Errorf("assets: get project: %w", err)
	}
	return projectFromRow(row), nil
}

func (s *Service) UpdateProject(ctx context.Context, actor authn.Principal, id uuid.UUID, upd ProjectUpdate) (Project, error) {
	var p Project
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetProject(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such project")
		}
		if err != nil {
			return fmt.Errorf("assets: get project: %w", err)
		}
		name, description := current.Name, current.Description
		if upd.Name != nil {
			if name, err = validateName("name", *upd.Name); err != nil {
				return err
			}
		}
		if upd.Description != nil {
			if err := validateDescription(*upd.Description); err != nil {
				return err
			}
			description = *upd.Description
		}
		row, err := q.UpdateProject(ctx, store.UpdateProjectParams{
			ID: id, Name: name, Description: description, UpdatedAt: s.clock.Now(),
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "project name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: update project: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "project.updated", SubjectKind: "project", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		p = projectFromRow(row)
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Service) DeleteProject(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteProject(ctx, id)
		if _, fk := fkViolation(err); fk {
			return xerrors.E(xerrors.ClassConflict, "project still has repos")
		}
		if err != nil {
			return fmt.Errorf("assets: delete project: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such project")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "project.deleted", SubjectKind: "project", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

// ProjectExists is the identity API's ProjectChecker port (grant
// endpoints 404 on unknown projects without importing assets' tables).
func (s *Service) ProjectExists(ctx context.Context, id uuid.UUID) (bool, error) {
	ok, err := store.New(s.pool).ProjectExists(ctx, id)
	if err != nil {
		return false, fmt.Errorf("assets: project exists: %w", err)
	}
	return ok, nil
}
