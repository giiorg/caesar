package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// ProjectGrant is one grant row on a project, joined with the user email
// for the admin surface.
type ProjectGrant struct {
	UserID uuid.UUID
	Email  string
	Level  GrantLevel
}

func grantFKTarget(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// SetGrant upserts (user, project) → level. Actors never modify their own
// row (mirrors UpdateUser's self-guard: no self-escalation, no
// accidental self-lockout). Project/user existence errors surface as
// not-found via the FK constraint names.
func (s *Service) SetGrant(ctx context.Context, actor Principal, projectID, userID uuid.UUID, level GrantLevel) error {
	if !level.Valid() {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "level", Message: "must be view, run, approve, or manage"})
	}
	if actor.UserID == userID {
		return xerrors.E(xerrors.ClassForbidden, "cannot change own grant")
	}
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := store.New(tx).UpsertGrant(ctx, store.UpsertGrantParams{
			UserID: userID, ProjectID: projectID, Level: string(level),
		})
		if constraint, fk := grantFKTarget(err); fk {
			if constraint == "grants_user_id_fkey" {
				return xerrors.E(xerrors.ClassNotFound, "no such user")
			}
			return xerrors.E(xerrors.ClassNotFound, "no such project")
		}
		if err != nil {
			return fmt.Errorf("identity: set grant: %w", err)
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
			Action: "grant.set", SubjectKind: "grant", SubjectID: projectID.String() + ":" + userID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"level": string(level)},
		})
	})
}

func (s *Service) DeleteGrant(ctx context.Context, actor Principal, projectID, userID uuid.UUID) error {
	if actor.UserID == userID {
		return xerrors.E(xerrors.ClassForbidden, "cannot change own grant")
	}
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteGrant(ctx, store.DeleteGrantParams{UserID: userID, ProjectID: projectID})
		if err != nil {
			return fmt.Errorf("identity: delete grant: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such grant")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
			Action: "grant.deleted", SubjectKind: "grant", SubjectID: projectID.String() + ":" + userID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

func (s *Service) ListGrants(ctx context.Context, projectID uuid.UUID) ([]ProjectGrant, error) {
	rows, err := store.New(s.pool).ListProjectGrants(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("identity: list grants: %w", err)
	}
	out := make([]ProjectGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProjectGrant{UserID: r.UserID, Email: r.Email, Level: GrantLevel(r.Level)})
	}
	return out, nil
}
