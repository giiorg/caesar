package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// adminGuardLockID serializes admin-count guard transactions (Bootstrap and
// UpdateUser) to prevent concurrent bypass of the last-admin check.
const adminGuardLockID = 1

// Params are the deployment-tunable knobs (config supplies them in app
// wiring; defaults pinned in the plan header).
type Params struct {
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration
}

type Service struct {
	pool   *pgxpool.Pool
	clock  clock.Clock
	audit  *audit.Recorder
	params Params
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder, p Params) *Service {
	return &Service{pool: pool, clock: c, audit: rec, params: p}
}

// UserUpdate is a partial admin update; nil fields are left unchanged.
type UserUpdate struct {
	Role   *Role
	Active *bool
}

func userFromRow(row store.User) User {
	return User{
		ID:        row.ID,
		Email:     row.Email,
		Role:      Role(row.Role),
		Active:    row.Active,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func validateEmail(email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "email", Message: "not a valid email address"})
	}
	return nil
}

func validatePassword(pw string) error {
	if len(pw) < MinPasswordLen || len(pw) > MaxPasswordLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{
				Field:   "password",
				Message: fmt.Sprintf("must be between %d and %d characters", MinPasswordLen, MaxPasswordLen),
			})
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Bootstrap creates the first admin. It refuses once any admin row exists
// (spec §6); `caesar admin bootstrap` is its only caller besides tests.
func (s *Service) Bootstrap(ctx context.Context, email, password string) (User, error) {
	if err := validateEmail(email); err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}

	var user User
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", adminGuardLockID); err != nil {
			return fmt.Errorf("identity: advisory lock: %w", err)
		}
		q := store.New(tx)

		admins, err := q.CountAdmins(ctx)
		if err != nil {
			return fmt.Errorf("identity: count admins: %w", err)
		}
		if admins > 0 {
			return xerrors.E(xerrors.ClassConflict, "an admin already exists; bootstrap runs once")
		}
		row, err := q.CreateUser(ctx, store.CreateUserParams{
			Email: email, PasswordHash: hash, Role: string(RoleAdmin),
		})
		if err != nil {
			return fmt.Errorf("identity: create admin: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Action: "user.bootstrapped", SubjectKind: "user", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"email": email},
		}); err != nil {
			return err
		}
		user = userFromRow(row)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// CreateUser is admin-invite-only user creation. The API layer enforces
// that actor is an admin; the service records it as the audit actor.
func (s *Service) CreateUser(ctx context.Context, actor Principal, email, password string, role Role) (User, error) {
	if err := validateEmail(email); err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	if !role.Valid() {
		return User{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "role", Message: "must be admin or member"})
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}

	var user User
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)

		row, err := q.CreateUser(ctx, store.CreateUserParams{
			Email: email, PasswordHash: hash, Role: string(role),
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "email already registered")
		}
		if err != nil {
			return fmt.Errorf("identity: create user: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
			Action: "user.created", SubjectKind: "user", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"email": email, "role": string(role)},
		}); err != nil {
			return err
		}
		user = userFromRow(row)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := store.New(s.pool).ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity: list users: %w", err)
	}
	users := make([]User, 0, len(rows))
	for _, r := range rows {
		users = append(users, userFromRow(r))
	}
	return users, nil
}

func (s *Service) GetUser(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := store.New(s.pool).GetUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, xerrors.E(xerrors.ClassNotFound, "no such user")
	}
	if err != nil {
		return User{}, fmt.Errorf("identity: get user: %w", err)
	}
	return userFromRow(row), nil
}

// UpdateUser applies an admin's role/active change. Guards: an admin never
// modifies their own row (lockout prevention), and the last active admin
// can be neither demoted nor deactivated. Task 9 extends this to revoke
// the target's sessions on privilege change.
func (s *Service) UpdateUser(ctx context.Context, actor Principal, id uuid.UUID, upd UserUpdate) (User, error) {
	if actor.UserID == id {
		return User{}, xerrors.E(xerrors.ClassForbidden, "cannot change own role or active flag")
	}
	if upd.Role != nil && !upd.Role.Valid() {
		return User{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "role", Message: "must be admin or member"})
	}

	var user User
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", adminGuardLockID); err != nil {
			return fmt.Errorf("identity: advisory lock: %w", err)
		}
		q := store.New(tx)

		current, err := q.GetUserByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such user")
		}
		if err != nil {
			return fmt.Errorf("identity: get user: %w", err)
		}

		newRole := Role(current.Role)
		if upd.Role != nil {
			newRole = *upd.Role
		}
		newActive := current.Active
		if upd.Active != nil {
			newActive = *upd.Active
		}

		losesAdmin := Role(current.Role) == RoleAdmin && current.Active && (newRole != RoleAdmin || !newActive)
		if losesAdmin {
			activeAdmins, err := q.CountActiveAdmins(ctx)
			if err != nil {
				return fmt.Errorf("identity: count active admins: %w", err)
			}
			if activeAdmins <= 1 {
				return xerrors.E(xerrors.ClassConflict, "cannot remove the last active admin")
			}
		}

		row, err := q.UpdateUserRoleActive(ctx, store.UpdateUserRoleActiveParams{
			ID: id, Role: string(newRole), Active: newActive, UpdatedAt: s.clock.Now(),
		})
		if err != nil {
			return fmt.Errorf("identity: update user: %w", err)
		}
		if newRole != Role(current.Role) || newActive != current.Active {
			now := s.clock.Now()
			if err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: id, RevokedAt: &now}); err != nil {
				return fmt.Errorf("identity: revoke sessions: %w", err)
			}
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
			Action: "user.updated", SubjectKind: "user", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"role": string(newRole), "active": newActive},
		}); err != nil {
			return err
		}
		user = userFromRow(row)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Service) GrantsForUser(ctx context.Context, userID uuid.UUID) ([]Grant, error) {
	rows, err := store.New(s.pool).GrantsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("identity: grants: %w", err)
	}
	grants := make([]Grant, 0, len(rows))
	for _, r := range rows {
		grants = append(grants, Grant{ProjectID: r.ProjectID, Level: GrantLevel(r.Level)})
	}
	return grants, nil
}

// Authorize is the single RBAC check (spec §6): global admin passes
// everything; members need a grant row at or above min on the project.
func (s *Service) Authorize(ctx context.Context, p Principal, projectID uuid.UUID, min GrantLevel) (bool, error) {
	if p.Role == RoleAdmin {
		return true, nil
	}
	level, err := store.New(s.pool).GetGrantLevel(ctx, store.GetGrantLevelParams{
		UserID: p.UserID, ProjectID: projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("identity: grant lookup: %w", err)
	}
	return GrantLevel(level).AtLeast(min), nil
}

// MintSessionForTest mints a session outside a login flow. Test seam only.
func (s *Service) MintSessionForTest(ctx context.Context, userID uuid.UUID) (string, error) {
	var token string
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		token, err = s.mintSession(ctx, store.New(tx), userID)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}
