package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows/store"
)

// Workflow names a family of immutable definition versions (spec §2).
type Workflow struct {
	ID            uuid.UUID
	Name          string
	Description   string
	LatestVersion int32
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Version is one immutable, content-hashed definition. Document holds
// the stored JSON (nil in list results).
type Version struct {
	ID          uuid.UUID
	WorkflowID  uuid.UUID
	Version     int32
	ContentHash string
	Document    []byte
	CreatedAt   time.Time
}

// WorkflowUpdate is a partial update; nil fields are left unchanged.
type WorkflowUpdate struct {
	Name        *string
	Description *string
}

// Validation pins (plan header; byte counts, house convention).
const (
	maxWorkflowNameLen = 100
	maxWorkflowDescLen = 500
)

func validateWorkflowName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxWorkflowNameLen {
		return "", xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "name", Message: fmt.Sprintf("must be 1-%d characters", maxWorkflowNameLen),
		})
	}
	return name, nil
}

func validateWorkflowDescription(desc string) error {
	if len(desc) > maxWorkflowDescLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "description", Message: fmt.Sprintf("must be at most %d characters", maxWorkflowDescLen),
		})
	}
	return nil
}

func workflowFromRow(id uuid.UUID, name, description string, createdAt, updatedAt time.Time, latest int32) Workflow {
	return Workflow{ID: id, Name: name, Description: description, LatestVersion: latest, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

func versionFromRow(r store.WorkflowVersion) Version {
	return Version{ID: r.ID, WorkflowID: r.WorkflowID, Version: r.Version, ContentHash: r.ContentHash, Document: r.Document, CreatedAt: r.CreatedAt}
}

func (s *Service) CreateWorkflow(ctx context.Context, actor authn.Principal, name, description string) (Workflow, error) {
	name, err := validateWorkflowName(name)
	if err != nil {
		return Workflow{}, err
	}
	if err := validateWorkflowDescription(description); err != nil {
		return Workflow{}, err
	}
	var w Workflow
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).CreateWorkflow(ctx, store.CreateWorkflowParams{Name: name, Description: description})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "workflow name already taken")
		}
		if err != nil {
			return fmt.Errorf("workflows: create: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.created", SubjectKind: "workflow", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		w = workflowFromRow(row.ID, row.Name, row.Description, row.CreatedAt, row.UpdatedAt, 0)
		return nil
	})
	if err != nil {
		return Workflow{}, err
	}
	return w, nil
}

func (s *Service) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	rows, err := store.New(s.pool).ListWorkflows(ctx)
	if err != nil {
		return nil, fmt.Errorf("workflows: list: %w", err)
	}
	out := make([]Workflow, 0, len(rows))
	for _, r := range rows {
		out = append(out, workflowFromRow(r.ID, r.Name, r.Description, r.CreatedAt, r.UpdatedAt, r.LatestVersion))
	}
	return out, nil
}

func (s *Service) GetWorkflow(ctx context.Context, id uuid.UUID) (Workflow, error) {
	r, err := store.New(s.pool).GetWorkflow(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workflow{}, xerrors.E(xerrors.ClassNotFound, "no such workflow")
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("workflows: get: %w", err)
	}
	return workflowFromRow(r.ID, r.Name, r.Description, r.CreatedAt, r.UpdatedAt, r.LatestVersion), nil
}

func (s *Service) UpdateWorkflow(ctx context.Context, actor authn.Principal, id uuid.UUID, upd WorkflowUpdate) (Workflow, error) {
	var w Workflow
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetWorkflowForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such workflow")
		}
		if err != nil {
			return fmt.Errorf("workflows: get for update: %w", err)
		}
		name, description := current.Name, current.Description
		if upd.Name != nil {
			if name, err = validateWorkflowName(*upd.Name); err != nil {
				return err
			}
		}
		if upd.Description != nil {
			if err := validateWorkflowDescription(*upd.Description); err != nil {
				return err
			}
			description = *upd.Description
		}
		row, err := q.UpdateWorkflow(ctx, store.UpdateWorkflowParams{ID: id, Name: name, Description: description, UpdatedAt: s.clock.Now()})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "workflow name already taken")
		}
		if err != nil {
			return fmt.Errorf("workflows: update: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.updated", SubjectKind: "workflow", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		w = workflowFromRow(row.ID, row.Name, row.Description, row.CreatedAt, row.UpdatedAt, 0)
		return nil
	})
	if err != nil {
		return Workflow{}, err
	}
	// LatestVersion is derived; refetch keeps the response honest.
	return s.GetWorkflow(ctx, w.ID)
}

func (s *Service) DeleteWorkflow(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteWorkflow(ctx, id)
		// Versions cascade, but a task pins one with ON DELETE RESTRICT.
		if isFKViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "workflow has runs; delete is blocked")
		}
		if err != nil {
			return fmt.Errorf("workflows: delete: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such workflow")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.deleted", SubjectKind: "workflow", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

// CreateVersion is the single write door (spec §4): strict parse, full
// §2 validation, canonicalize, hash, then an insert serialized on the
// workflow row lock. Posting content that already exists for the
// workflow returns the existing version with created=false — the door
// is idempotent on content (plan header, decision 6).
func (s *Service) CreateVersion(ctx context.Context, actor authn.Principal, workflowID uuid.UUID, raw []byte) (Version, bool, error) {
	doc, err := ParseDocument(raw)
	if err != nil {
		return Version{}, false, err
	}
	if violations := Validate(doc); len(violations) > 0 {
		e := xerrors.E(xerrors.ClassInvalid, "definition failed validation")
		for _, v := range violations {
			e = e.WithFields(xerrors.FieldError{Field: v.Path, Message: v.Message})
		}
		return Version{}, false, e
	}
	canonical, hash := doc.Canonical(), doc.Hash()

	var out Version
	created := false
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.GetWorkflowForUpdate(ctx, workflowID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return xerrors.E(xerrors.ClassNotFound, "no such workflow")
			}
			return fmt.Errorf("workflows: lock workflow: %w", err)
		}
		existing, err := q.GetVersionByHash(ctx, store.GetVersionByHashParams{WorkflowID: workflowID, ContentHash: hash})
		if err == nil {
			out = versionFromRow(existing)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("workflows: version by hash: %w", err)
		}
		next, err := q.NextVersionNumber(ctx, workflowID)
		if err != nil {
			return fmt.Errorf("workflows: next version: %w", err)
		}
		row, err := q.InsertVersion(ctx, store.InsertVersionParams{
			WorkflowID: workflowID, Version: next, ContentHash: hash, Document: canonical,
		})
		if err != nil {
			return fmt.Errorf("workflows: insert version: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "workflow.version_created", SubjectKind: "workflow_version", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"workflow_id": workflowID.String(), "version": next, "content_hash": hash},
		}); err != nil {
			return err
		}
		out = versionFromRow(row)
		created = true
		return nil
	})
	if err != nil {
		return Version{}, false, err
	}
	return out, created, nil
}

func (s *Service) ListVersions(ctx context.Context, workflowID uuid.UUID) ([]Version, error) {
	if _, err := s.GetWorkflow(ctx, workflowID); err != nil {
		return nil, err
	}
	rows, err := store.New(s.pool).ListVersions(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("workflows: list versions: %w", err)
	}
	out := make([]Version, 0, len(rows))
	for _, r := range rows {
		out = append(out, Version{ID: r.ID, WorkflowID: r.WorkflowID, Version: r.Version, ContentHash: r.ContentHash, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (s *Service) GetVersion(ctx context.Context, workflowID uuid.UUID, version int32) (Version, error) {
	r, err := store.New(s.pool).GetVersion(ctx, store.GetVersionParams{WorkflowID: workflowID, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	if err != nil {
		return Version{}, fmt.Errorf("workflows: get version: %w", err)
	}
	return versionFromRow(r), nil
}

// GetVersionByID resolves a pinned version directly — the execution
// spine's re-run and plan-loading path (wired through app's adapters).
func (s *Service) GetVersionByID(ctx context.Context, id uuid.UUID) (Version, error) {
	r, err := store.New(s.pool).GetVersionByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	if err != nil {
		return Version{}, fmt.Errorf("workflows: get version by id: %w", err)
	}
	return versionFromRow(r), nil
}
